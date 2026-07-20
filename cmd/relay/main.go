package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	"github.com/bortolidiego/relay/internal/agent"
	"github.com/bortolidiego/relay/internal/keychain"
	"github.com/bortolidiego/relay/internal/pairing"
	"github.com/bortolidiego/relay/internal/tunnel"
	"github.com/bortolidiego/relay/shared/contracts"
	qrcode "github.com/skip2/go-qrcode"
)

var version = "relay-m2"

// CLI expõe os comandos do Marco 2.
type CLI struct {
	Setup   SetupCmd   `cmd:"" help:"Inicializa sessão e identidade no Keychain."`
	Share   ShareCmd   `cmd:"" help:"Gera oferta QR one-time para emparelhamento."`
	Pair    PairCmd    `cmd:"" help:"Valida uma oferta lida do QR; o pareamento real é feito pela PWA."`
	Status  StatusCmd  `cmd:"" help:"Consulta status do agente local."`
	Stop    StopCmd    `cmd:"" help:"Para o agente local."`
	Devices DevicesCmd `cmd:"" help:"Lista dispositivos emparelhados."`
	Revoke  RevokeCmd  `cmd:"" help:"Revoga um dispositivo."`
}

// SharedFlags usadas por comandos que precisam do agente.
type SharedFlags struct {
	Addr       string `env:"RELAY_ADDR" default:"127.0.0.1:24109" help:"Endereço do agente Relay."`
	SessionID  string `env:"RELAY_SESSION_ID" help:"ID da sessão Relay."`
	LocalToken string `env:"RELAY_LOCAL_TOKEN" help:"Override explícito do token local; uso normal recupera do Keychain."`
}

type SetupCmd struct {
	SessionID string `arg:"" required:"" help:"ID da sessão."`
	HostName  string `arg:"" optional:"" help:"Nome amigável do host."`
	BasePath  string `arg:"" optional:"" help:"Caminho base do sandbox."`
	Frontmost bool   `help:"Marca a sessão como janela frontmost detectada pelo usuário."`
	WindowID  string `help:"Identificador da janela nativa, quando conhecido."`
	PID       int    `env:"RELAY_TARGET_PID" help:"PID alvo da sessão; default seguro é o processo pai."`

	TunnelEnabled   bool   `help:"Habilita Cloudflare Tunnel no share."`
	TunnelName      string `env:"RELAY_TUNNEL_NAME" default:"relay-diego" help:"Nome do tunnel."`
	TunnelHostname  string `env:"RELAY_TUNNEL_HOSTNAME" default:"relay.kbtech.com.br" help:"Hostname público do tunnel."`
	TunnelToken     string `env:"RELAY_TUNNEL_TOKEN" help:"Token do Cloudflare Tunnel (preferir env)."`
	TunnelURL       string `env:"RELAY_TUNNEL_URL" default:"http://127.0.0.1:24109" help:"URL local para o qual o tunnel aponta."`
}

func (s *SetupCmd) Run(ctx *kong.Context) error {
	name := s.HostName
	if name == "" {
		h, _ := os.Hostname()
		name = h
	}
	base := s.BasePath
	if base == "" {
		base, _ = os.Getwd()
	}
	tunCfg := tunnel.Config{
		Enabled:  s.TunnelEnabled,
		Name:     s.TunnelName,
		Hostname: s.TunnelHostname,
		URL:      s.TunnelURL,
		Token:    s.TunnelToken,
	}
	tunCfg.Normalize()
	store := keychain.DefaultStore()
	if tunCfg.Enabled {
		if err := saveTunnelConfig(store, s.SessionID, tunCfg); err != nil {
			return err
		}
	}
	cfg := agent.Config{
		Addr:      "127.0.0.1:24109",
		SessionID: s.SessionID,
		HostName:  name,
		BasePath:  base,
		Store:     store,
		Metadata:  buildSessionMetadata(s.SessionID, s.WindowID, s.Frontmost, s.PID),
		Tunnel:    tunCfg,
	}
	ag, err := agent.New(cfg)
	if err != nil {
		return err
	}
	if err := ag.Start(); err != nil {
		return err
	}
	time.Sleep(100 * time.Millisecond)
	fmt.Printf("Sessão inicializada: %s\n", s.SessionID)
	fmt.Printf("Host: %s (%s)\n", name, ag.Registry().HostID())
	fmt.Printf("Agente: http://%s\n", ag.ListenAddr())
	fmt.Printf("Sandbox: %s\n", base)
	if tunCfg.Enabled {
		fmt.Printf("Tunnel: habilitado (%s -> %s)\n", tunCfg.Hostname, tunCfg.URL)
	}
	fmt.Println("Identidade e token local salvos no Keychain. O agente continua rodando.")
	<-ag.Done()
	return nil
}

type ShareCmd struct {
	SharedFlags
	Frontmost bool   `help:"Marca a sessão como frontmost no descritor."`
	WindowID  string `help:"Identificador da janela nativa, quando conhecido."`
	PID       int    `env:"RELAY_TARGET_PID" help:"PID alvo da sessão; default seguro é o processo pai."`
	QROut     string `help:"Caminho do PNG QR. Default: relay-pair-<sessao>.png no cwd."`
	NoTunnel  bool   `help:"Não inicia o tunnel mesmo que configurado."`
}

func (s *ShareCmd) Run(ctx *kong.Context) error {
	if s.SessionID == "" {
		s.SessionID = defaultSessionID()
	}
	if s.SessionID == "" {
		return fmt.Errorf("RELAY_SESSION_ID necessário quando CODEX_THREAD_ID/MAESTRI_TERMINAL_ID não existem")
	}
	store := keychain.DefaultStore()
	token, err := resolveLocalToken(s.LocalToken, s.SessionID, store)
	if err != nil {
		return err
	}
	if !s.NoTunnel {
		if err := requestTunnelStart(s.Addr, token); err != nil {
			if errors.Is(err, tunnel.ErrCloudflaredMissing) {
				fmt.Fprintf(os.Stderr, "Aviso: cloudflared não encontrado. Tunnel não iniciado. Instale com 'brew install cloudflared' para acesso remoto.\n")
			} else if errors.Is(err, tunnel.ErrTokenMissing) {
				fmt.Fprintf(os.Stderr, "Aviso: token do tunnel não configurado. Tunnel não iniciado. Use setup --tunnel-enabled --tunnel-token=... ou RELAY_TUNNEL_TOKEN.\n")
			} else if errors.Is(err, tunnel.ErrTunnelDisabled) || isAgentTunnelDisabled(err) {
				// silencioso quando desabilitado
			} else {
				return err
			}
		}
	}
	meta := buildSessionMetadata(s.SessionID, s.WindowID, s.Frontmost, s.PID)
	metaBody, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	if _, err := postAgent(s.Addr, "/api/metadata", metaBody, token); err != nil {
		return err
	}
	body, err := postAgent(s.Addr, "/api/offer", nil, token)
	if err != nil {
		return err
	}
	var env contracts.SignedEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return err
	}
	if _, err := pairing.VerifySignedOffer(env, time.Now()); err != nil {
		return err
	}
	qrPath := s.QROut
	if qrPath == "" {
		qrPath = defaultQRPath(s.SessionID)
	}
	if err := writeQRCode(qrPath, string(body)); err != nil {
		return err
	}
	fmt.Println(string(body))
	fmt.Println(string(env.Payload))
	fmt.Printf("QR local one-time: %s\n", qrPath)
	fmt.Printf("Payload local one-time gerado para %s. Expira em 2 minutos.\n", s.SessionID)
	return nil
}

type PairCmd struct {
	SharedFlags
	Offer string `arg:"" required:"" help:"JSON da oferta lida do QR."`
}

func (p *PairCmd) Run(ctx *kong.Context) error {
	var env contracts.SignedEnvelope
	if err := json.Unmarshal([]byte(p.Offer), &env); err != nil {
		return err
	}
	offer, err := pairing.VerifySignedOffer(env, time.Now())
	if err != nil {
		return err
	}
	fmt.Printf("Oferta válida para %s (%s). Use a PWA para gerar chaves WebCrypto e concluir o pareamento real.\n", offer.HostName, offer.HostID)
	return nil
}

type StatusCmd struct {
	SharedFlags
}

func (s *StatusCmd) Run(ctx *kong.Context) error {
	token, err := resolveLocalToken(s.LocalToken, s.SessionID, keychain.DefaultStore())
	if err != nil {
		return err
	}
	body, err := getAgent(s.Addr, "/api/status", token)
	if err != nil {
		return err
	}
	fmt.Println(string(body))
	return nil
}

type StopCmd struct {
	SharedFlags
}

func (s *StopCmd) Run(ctx *kong.Context) error {
	store := keychain.DefaultStore()
	token, err := resolveLocalToken(s.LocalToken, s.SessionID, store)
	if err != nil {
		return err
	}
	body, err := postAgent(s.Addr, "/api/stop", nil, token)
	if err != nil {
		return err
	}
	fmt.Println(string(body))
	return nil
}

type DevicesCmd struct {
	SharedFlags
}

func (d *DevicesCmd) Run(ctx *kong.Context) error {
	token, err := resolveLocalToken(d.LocalToken, d.SessionID, keychain.DefaultStore())
	if err != nil {
		return err
	}
	body, err := getAgent(d.Addr, "/api/devices", token)
	if err != nil {
		return err
	}
	fmt.Println(string(body))
	return nil
}

type RevokeCmd struct {
	SharedFlags
	DeviceID string `arg:"" required:"" help:"ID do dispositivo a revogar."`
}

func (r *RevokeCmd) Run(ctx *kong.Context) error {
	token, err := resolveLocalToken(r.LocalToken, r.SessionID, keychain.DefaultStore())
	if err != nil {
		return err
	}
	body, err := postAgent(r.Addr, "/api/revoke?device_id="+r.DeviceID, nil, token)
	if err != nil {
		return err
	}
	fmt.Println(string(body))
	return nil
}

func getAgent(addr, path, localToken string) ([]byte, error) {
	url := "http://" + addr + path
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if localToken != "" {
		req.Header.Set("X-Relay-Local-Token", localToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("agente não responde em %s: %w", addr, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return b, nil
}

func postAgent(addr, path string, data []byte, localToken string) ([]byte, error) {
	url := "http://" + addr + path
	var body io.Reader
	if data != nil {
		body = strings.NewReader(string(data))
	}
	req, _ := http.NewRequest(http.MethodPost, url, body)
	req.Header.Set("Content-Type", "application/json")
	if localToken != "" {
		req.Header.Set("X-Relay-Local-Token", localToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return b, nil
}

func defaultSessionID() string {
	if v := os.Getenv("CODEX_THREAD_ID"); v != "" {
		return "codex-" + v
	}
	if v := os.Getenv("MAESTRI_TERMINAL_ID"); v != "" {
		return "maestri-" + v
	}
	return ""
}

func resolveLocalToken(override, sessionID string, store keychain.Store) (string, error) {
	if override != "" {
		return override, nil
	}
	if sessionID == "" {
		return "", fmt.Errorf("session_id necessário para recuperar token local do Keychain")
	}
	token, err := agent.LoadLocalToken(store, sessionID)
	if err != nil {
		return "", fmt.Errorf("token local não encontrado no Keychain para %s: %w", sessionID, err)
	}
	return token, nil
}

const tunnelKey = "relay-tunnel-config"

var errTunnelConfigMissing = errors.New("configuração de tunnel não encontrada")

func tunnelAccount(sessionID string) string { return "host-" + sessionID }

func saveTunnelConfig(store keychain.Store, sessionID string, cfg tunnel.Config) error {
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return store.SaveSecret(tunnelKey, tunnelAccount(sessionID), b)
}

func loadTunnelConfig(store keychain.Store, sessionID string) (tunnel.Config, error) {
	b, err := store.LoadSecret(tunnelKey, tunnelAccount(sessionID))
	if err != nil {
		return tunnel.Config{}, errTunnelConfigMissing
	}
	var cfg tunnel.Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return tunnel.Config{}, err
	}
	cfg.Normalize()
	return cfg, nil
}

func requestTunnelStart(addr, token string) error {
	body, err := postAgent(addr, "/api/tunnel/start", nil, token)
	if err != nil {
		return fmt.Errorf("falha ao solicitar início do tunnel: %w", err)
	}
	var resp struct {
		Error  string `json:"error"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return err
	}
	if resp.Error != "" {
		if strings.Contains(resp.Error, "token do Cloudflare Tunnel") {
			return tunnel.ErrTokenMissing
		}
		if strings.Contains(resp.Error, "cloudflared não encontrado") {
			return tunnel.ErrCloudflaredMissing
		}
		return errors.New(resp.Error)
	}
	if resp.Status == "started" || resp.Status == "already running" {
		return nil
	}
	return fmt.Errorf("tunnel status inesperado: %s", resp.Status)
}

func isAgentTunnelDisabled(err error) bool {
	return err != nil && strings.Contains(err.Error(), "tunnel desabilitado")
}

func defaultQRPath(sessionID string) string {
	safe := regexp.MustCompile(`[^A-Za-z0-9._-]+`).ReplaceAllString(sessionID, "-")
	if safe == "" {
		safe = "session"
	}
	return filepath.Join(".", "relay-pair-"+safe+".png")
}

func writeQRCode(path, payload string) error {
	if payload == "" {
		return fmt.Errorf("payload QR vazio")
	}
	return qrcode.WriteFile(payload, qrcode.Medium, 320, path)
}

func buildSessionMetadata(sessionID, windowID string, frontmost bool, targetPID int) contracts.SessionMetadata {
	pid := targetPID
	if pid <= 0 {
		pid = os.Getppid()
	}
	meta := contracts.SessionMetadata{
		Harness:         contracts.HarnessNative,
		NativeSessionID: sessionID,
		PID:             &pid,
		Frontmost:       frontmost,
	}
	if codex := os.Getenv("CODEX_THREAD_ID"); codex != "" {
		meta.Harness = contracts.HarnessCodex
		meta.CodexThreadID = &codex
		meta.NativeSessionID = codex
	}
	if maestri := os.Getenv("MAESTRI_TERMINAL_ID"); maestri != "" {
		if meta.Harness == contracts.HarnessNative {
			meta.Harness = contracts.HarnessMaestri
			meta.NativeSessionID = maestri
		}
		meta.MaestriTerminalID = &maestri
	}
	if windowID != "" {
		meta.WindowID = &windowID
	}
	return meta
}

func main() {
	var cli CLI
	ctx := kong.Parse(&cli,
		kong.Name("relay"),
		kong.Description("Relay CLI — Marco 2"),
		kong.UsageOnError(),
		kong.Vars{"version": version},
	)
	if err := ctx.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// openBrowser é utilitário opcional para o helper macOS.
func openBrowser(url string) error {
	return exec.Command("open", url).Start()
}

var _ = context.Background()

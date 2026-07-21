package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/alecthomas/kong"
	"github.com/bortolidiego/relay/internal/agent"
	"github.com/bortolidiego/relay/internal/keychain"
	"github.com/bortolidiego/relay/internal/pairing"
	"github.com/bortolidiego/relay/internal/tlsutil"
	"github.com/bortolidiego/relay/internal/tunnel"
	"github.com/bortolidiego/relay/shared/contracts"
	qrcode "github.com/skip2/go-qrcode"
)

var version = "remotecli-0.1"

// CLI — Remote CliControl (comando: remotecli).
type CLI struct {
	// Comandos principais (produto)
	Relay       RelayCmd       `cmd:"" help:"Liga o Remote CliControl no Mac e mostra QR se o celular ainda não estiver pareado."`
	Here        HereCmd        `cmd:"" help:"Registra ESTE terminal na lista do celular (qualquer CLI). Sem QR se já estiver pareado."`
	PhoneBridge PhoneBridgeCmd `cmd:"" name:"phone-bridge" help:"Loop do terminal ponte Maestri (rcli-phone) — entrega mensagens do celular na conversa."`
	EnsureBridge EnsureBridgeCmd `cmd:"" name:"ensure-bridge" help:"Garante o terminal rcli-phone no canvas Maestri (maestro)."`

	// Acesso (LAN / tunnel do usuário / hosted)
	Access AccessCmd `cmd:"" help:"Modo de acesso: lan (padrão), tunnel (Cloudflare do usuário) ou hosted (roadmap)."`

	// Operação / legado
	Serve   ServeCmd   `cmd:"" help:"Sobe o agente em foreground (daemon)."`
	Setup   SetupCmd   `cmd:"" help:"Configura sessão/tunnel e sobe o agente."`
	Share   ShareCmd   `cmd:"" hidden:"" help:"Alias legado de 'remotecli relay'."`
	Pair    PairCmd    `cmd:"" help:"Valida uma oferta lida do QR."`
	Status  StatusCmd  `cmd:"" help:"Status do agente local."`
	Stop    StopCmd    `cmd:"" help:"Para o agente local."`
	Devices DevicesCmd `cmd:"" help:"Lista dispositivos emparelhados."`
	Revoke  RevokeCmd  `cmd:"" help:"Revoga um dispositivo."`
}

// AccessCmd — configura como o celular alcança o agente.
//
//	remotecli access              # mostra modo atual
//	remotecli access lan          # só Wi‑Fi (zero config)
//	remotecli access tunnel --token TOKEN [--hostname meudominio.com]
//	remotecli access hosted --url https://…  # roadmap
type AccessCmd struct {
	Mode     string `arg:"" optional:"" help:"lan | tunnel | hosted (vazio = mostrar atual)."`
	Token    string `name:"token" env:"REMOTECLI_TUNNEL_TOKEN" help:"Token do Cloudflare Tunnel (modo tunnel)."`
	Hostname string `name:"hostname" env:"REMOTECLI_TUNNEL_HOSTNAME" help:"Hostname público opcional (só documentação/exibição)."`
	URL      string `name:"url" env:"REMOTECLI_TUNNEL_URL" help:"URL local exposta pelo tunnel (default http://127.0.0.1:24109)."`
	Hosted   string `name:"hosted-url" env:"REMOTECLI_HOSTED_URL" help:"URL do serviço hosted (modo hosted)."`
	Name     string `name:"name" env:"REMOTECLI_TUNNEL_NAME" help:"Nome amigável do tunnel (default remotecli)."`
}

func (c *AccessCmd) Run(ctx *kong.Context) error {
	store := keychain.DefaultStore()
	mode := strings.ToLower(strings.TrimSpace(c.Mode))
	if mode == "" {
		return printAccessStatus(store)
	}
	switch mode {
	case tunnel.ModeLAN, "local", "wifi":
		cfg := tunnel.DefaultConfig()
		cfg.Mode = tunnel.ModeLAN
		cfg.Enabled = false
		if err := saveAccessConfig(store, cfg); err != nil {
			return err
		}
		fmt.Println("Modo de acesso: LAN (só Wi‑Fi local).")
		fmt.Println("Celular na mesma rede → remotecli relay → escaneie o QR.")
		fmt.Println("Nenhuma conta Cloudflare necessária.")
		return nil
	case tunnel.ModeTunnel, "cloudflare", "cf":
		token := strings.TrimSpace(c.Token)
		if token == "" {
			token = strings.TrimSpace(os.Getenv("RELAY_TUNNEL_TOKEN"))
		}
		if token == "" {
			return fmt.Errorf("modo tunnel exige --token (ou env REMOTECLI_TUNNEL_TOKEN).\n\nComo obter:\n  1) Conta em https://dash.cloudflare.com\n  2) Zero Trust → Networks → Tunnels → Create\n  3) Copie o token do conector\n  4) remotecli access tunnel --token SEU_TOKEN\n  5) brew install cloudflared  (se ainda não tiver)")
		}
		cfg := tunnel.DefaultConfig()
		cfg.Mode = tunnel.ModeTunnel
		cfg.Enabled = true
		cfg.Token = token
		if c.Hostname != "" {
			cfg.Hostname = c.Hostname
		}
		if c.URL != "" {
			cfg.URL = c.URL
		}
		if c.Name != "" {
			cfg.Name = c.Name
		}
		cfg.Normalize()
		if err := saveAccessConfig(store, cfg); err != nil {
			return err
		}
		fmt.Println("Modo de acesso: TUNNEL (Cloudflare do usuário).")
		if cfg.Hostname != "" {
			fmt.Printf("Hostname (referência): %s\n", cfg.Hostname)
		}
		fmt.Printf("URL local exposta: %s\n", cfg.URL)
		fmt.Println("Token salvo no Keychain. Rode: remotecli relay")
		fmt.Println("Requisito: cloudflared no PATH (brew install cloudflared).")
		return nil
	case tunnel.ModeHosted, "cloud", "saas":
		url := strings.TrimSpace(c.Hosted)
		if url == "" {
			return fmt.Errorf("modo hosted exige --hosted-url (ex: https://relay.seudominio.com).\nAinda em roadmap: por enquanto use 'lan' ou 'tunnel'.")
		}
		cfg := tunnel.DefaultConfig()
		cfg.Mode = tunnel.ModeHosted
		cfg.HostedURL = url
		cfg.Enabled = false
		cfg.Normalize()
		if err := saveAccessConfig(store, cfg); err != nil {
			return err
		}
		fmt.Println("Modo de acesso: HOSTED (salvo — implementação em roadmap).")
		fmt.Printf("URL: %s\n", url)
		fmt.Println("Por enquanto o agente ainda sobe em LAN; o relay central virá depois.")
		return nil
	default:
		return fmt.Errorf("modo desconhecido %q — use: lan | tunnel | hosted", c.Mode)
	}
}

func printAccessStatus(store keychain.Store) error {
	cfg, err := loadAccessConfig(store)
	if err != nil {
		cfg = tunnel.DefaultConfig()
	}
	cfg.Normalize()
	fmt.Println("Remote CliControl — modo de acesso")
	fmt.Println()
	switch cfg.AccessMode() {
	case tunnel.ModeTunnel:
		fmt.Println("  Atual: TUNNEL (Cloudflare do usuário)")
		if cfg.Hostname != "" {
			fmt.Printf("  Hostname: %s\n", cfg.Hostname)
		} else {
			fmt.Println("  Hostname: (não definido — opcional)")
		}
		fmt.Printf("  URL local: %s\n", cfg.URL)
		if cfg.Token != "" {
			fmt.Println("  Token: configurado (Keychain/env)")
		} else {
			fmt.Println("  Token: AUSENTE — configure com remotecli access tunnel --token …")
		}
	case tunnel.ModeHosted:
		fmt.Println("  Atual: HOSTED (roadmap)")
		fmt.Printf("  URL: %s\n", cfg.HostedURL)
	default:
		fmt.Println("  Atual: LAN (só Wi‑Fi local) ← padrão")
		fmt.Println("  Zero config de cloud. Celular na mesma rede.")
	}
	fmt.Println()
	fmt.Println("Trocar:")
	fmt.Println("  remotecli access lan")
	fmt.Println("  remotecli access tunnel --token TOKEN [--hostname seu.dominio]")
	fmt.Println("  remotecli access hosted --hosted-url https://…")
	return nil
}

// PhoneBridgeCmd processa a fila de mensagens do celular → sessão Maestri.
type PhoneBridgeCmd struct {
	Once bool `help:"Processa a fila uma vez e sai (para loops externos)."`
}

func (c *PhoneBridgeCmd) Run(ctx *kong.Context) error {
	if c.Once {
		n, err := agent.ProcessOutboxOnce()
		if err != nil {
			return err
		}
		if n > 0 {
			fmt.Printf("bridge: entregues %d\n", n)
		}
		return nil
	}
	fmt.Println("remotecli phone-bridge · entregando mensagens do celular nas sessões…")
	for {
		n, err := agent.ProcessOutboxOnce()
		if err != nil {
			fmt.Fprintf(os.Stderr, "bridge: %v\n", err)
		}
		if n > 0 {
			fmt.Printf("bridge: entregues %d mensagem(ns)\n", n)
		}
		time.Sleep(800 * time.Millisecond)
	}
}

// EnsureBridgeCmd recruta rcli-phone se faltar (requer maestro).
type EnsureBridgeCmd struct{}

func (c *EnsureBridgeCmd) Run(ctx *kong.Context) error {
	return ensurePhoneBridge()
}

// SharedFlags usadas por comandos que precisam do agente.
type SharedFlags struct {
	Addr       string `env:"RELAY_ADDR" default:"127.0.0.1:24109" help:"Endereço do agente Relay para clientes (fallback para 127.0.0.1 se bind for 0.0.0.0)."`
	SessionID  string `env:"RELAY_SESSION_ID" help:"ID da sessão Relay."`
	LocalToken string `env:"RELAY_LOCAL_TOKEN" help:"Override explícito do token local; uso normal recupera do Keychain."`
}

// ServeFlags comuns entre serve e setup.
type ServeFlags struct {
	SessionID string `arg:"" optional:"" env:"RELAY_SESSION_ID" help:"ID da sessão. Resolve de env se omitido."`
	HostName  string `arg:"" optional:"" help:"Nome amigável do host."`
	BasePath  string `arg:"" optional:"" help:"Caminho base do sandbox."`
	Frontmost bool   `help:"Marca a sessão como janela frontmost detectada pelo usuário."`
	WindowID  string `help:"Identificador da janela nativa, quando conhecido."`
	PID       int    `env:"RELAY_TARGET_PID" help:"PID alvo da sessão; default seguro é o processo pai."`

	TunnelEnabled  bool   `help:"Habilita Cloudflare Tunnel (modo tunnel). Preferir: remotecli access tunnel."`
	TunnelName     string `env:"REMOTECLI_TUNNEL_NAME" help:"Nome do tunnel (default remotecli)."`
	TunnelHostname string `env:"REMOTECLI_TUNNEL_HOSTNAME" help:"Hostname público opcional (exibição)."`
	TunnelToken    string `env:"REMOTECLI_TUNNEL_TOKEN" help:"Token do Cloudflare Tunnel."`
	TunnelURL      string `env:"REMOTECLI_TUNNEL_URL" help:"URL local do agente (default http://127.0.0.1:24109)."`
}

type ServeCmd struct {
	ServeFlags
}

func (s *ServeCmd) Run(ctx *kong.Context) error {
	return runServe(s.SessionID, s.HostName, s.BasePath, s.WindowID, s.Frontmost, s.PID,
		resolveTunnelConfig(s.TunnelEnabled, s.TunnelName, s.TunnelHostname, s.TunnelURL, s.TunnelToken), true)
}

type SetupCmd struct {
	ServeFlags
}

func runServe(sessionID, hostName, basePath, windowID string, frontmost bool, pid int, tunCfg tunnel.Config, block bool) error {
	sessionID = resolveSessionID(sessionID)
	name := hostName
	if name == "" {
		h, _ := os.Hostname()
		name = h
	}
	base := basePath
	if base == "" {
		base, _ = os.Getwd()
	}
	store := keychain.DefaultStore()
	// Preferência salva (remotecli access …) + flags da linha de comando.
	tunCfg = mergeTunnelWithSaved(store, tunCfg)
	tunCfg.Normalize()
	cfg := agent.Config{
		Addr:      defaultAgentAddr(),
		SessionID: sessionID,
		HostName:  name,
		BasePath:  base,
		Store:     store,
		Metadata:  buildSessionMetadata(sessionID, windowID, frontmost, pid),
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
	if block {
		fmt.Printf("Sessão inicializada: %s\n", sessionID)
		fmt.Printf("Host: %s (%s)\n", name, ag.Registry().HostID())
		fmt.Printf("Agente: https://%s (ou LAN)\n", ag.ListenAddr())
		fmt.Printf("Sandbox: %s\n", base)
		printAccessBanner(tunCfg)
		fmt.Println("Identidade e token local salvos no Keychain. O agente continua rodando.")
		<-ag.Done()
	}
	return nil
}

func (s *SetupCmd) Run(ctx *kong.Context) error {
	return runServe(s.SessionID, s.HostName, s.BasePath, s.WindowID, s.Frontmost, s.PID,
		resolveTunnelConfig(s.TunnelEnabled, s.TunnelName, s.TunnelHostname, s.TunnelURL, s.TunnelToken), true)
}

// SyncFlags flags comuns a remotecli relay / remotecli here / share.
type SyncFlags struct {
	SharedFlags
	Frontmost bool   `help:"Marca a sessão como frontmost no descritor."`
	WindowID  string `help:"Identificador da janela nativa, quando conhecido."`
	PID       int    `env:"RELAY_TARGET_PID" help:"PID alvo da sessão; default seguro é o processo pai."`
	QROut     string `help:"Caminho do PNG QR. Default: relay-pair-<sessao>.png no cwd."`
	NoTunnel  bool   `help:"Não inicia o tunnel mesmo que configurado."`
	NoStart   bool   `help:"Não sobe o agente se ele estiver parado; erro claro."`
	NoOpen    bool   `help:"Não abre o PNG do QR no Preview (padrão: abre no macOS)."`
	ForceQR   bool   `help:"Sempre mostra QR, mesmo se o celular já estiver pareado."`
}

// RelayCmd: remotecli relay — liga o serviço + QR se necessário.
type RelayCmd struct {
	SyncFlags
}

func (c *RelayCmd) Run(ctx *kong.Context) error {
	return runSyncSession(c.SyncFlags, syncModeRelay)
}

// HereCmd: remotecli here — registra ESTE terminal (sem QR se já pareado).
type HereCmd struct {
	SyncFlags
}

func (c *HereCmd) Run(ctx *kong.Context) error {
	return runSyncSession(c.SyncFlags, syncModeHere)
}

// ShareCmd: alias legado de remotecli relay.
type ShareCmd struct {
	SyncFlags
}

func (s *ShareCmd) Run(ctx *kong.Context) error {
	return runSyncSession(s.SyncFlags, syncModeRelay)
}

type syncMode int

const (
	syncModeRelay syncMode = iota // QR se ninguém pareado (ou ForceQR)
	syncModeHere                  // só metadata se já pareado; QR só se 1º dispositivo
)

func runSyncSession(s SyncFlags, mode syncMode) error {
	selfExe, err := os.Executable()
	if err != nil {
		return err
	}
	s.SessionID = resolveSessionID(s.SessionID)
	label := "relay"
	if mode == syncModeHere {
		label = "here"
	}
	fmt.Printf("remotecli %s · sessão: %s\n", label, s.SessionID)
	cAddr := clientAddr(s.Addr)
	if s.NoStart {
		if !agentHealthy(cAddr) {
			return fmt.Errorf("agente não está no ar; rode: remotecli relay")
		}
	} else {
		startedAgent, err := ensureAgentRunning(selfExe, s.SessionID, cAddr, defaultAgentStartTimeout)
		if err != nil {
			return err
		}
		if startedAgent {
			fmt.Println("Agente iniciado em background.")
		}
	}

	store := keychain.DefaultStore()
	token, err := ensureLocalToken(s.LocalToken, s.SessionID, store, defaultAgentStartTimeout)
	if err != nil {
		return err
	}

	// Banner de modo de acesso (LAN / tunnel / hosted)
	accCfg, accErr := loadAccessConfig(store)
	if accErr != nil {
		accCfg = tunnel.DefaultConfig()
	}
	printAccessBanner(accCfg)

	// Tunnel só sobe no modo tunnel. Em LAN/hosted não chama a API (evita 400 sem token).
	if !s.NoTunnel && accCfg.AccessMode() == tunnel.ModeTunnel {
		if err := requestTunnelStart(cAddr, token); err != nil {
			if errors.Is(err, tunnel.ErrCloudflaredMissing) {
				fmt.Fprintf(os.Stderr, "Aviso: cloudflared não encontrado. Tunnel não iniciado.\n")
				fmt.Fprintf(os.Stderr, "  brew install cloudflared\n")
			} else if errors.Is(err, tunnel.ErrTokenMissing) || strings.Contains(err.Error(), "token do Cloudflare Tunnel") {
				fmt.Fprintf(os.Stderr, "Aviso: token do tunnel não configurado.\n")
				fmt.Fprintf(os.Stderr, "  remotecli access tunnel --token SEU_TOKEN\n")
			} else if errors.Is(err, tunnel.ErrTunnelDisabled) || isAgentTunnelDisabled(err) {
				// desabilitado: silencioso
			} else {
				return err
			}
		} else {
			fmt.Println("Tunnel Cloudflare: pedido de start enviado ao agente.")
		}
	}
	meta := buildSessionMetadata(s.SessionID, s.WindowID, s.Frontmost, s.PID)
	metaBody, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	if _, err := postAgent(cAddr, "/api/metadata", metaBody, token); err != nil {
		return err
	}

	// Garante ponte Maestri (rcli-phone) para o celular falar na conversa de verdade.
	if os.Getenv("MAESTRI_TERMINAL_ID") != "" {
		if err := ensurePhoneBridge(); err != nil {
			fmt.Fprintf(os.Stderr, "Aviso: ponte phone-bridge: %v\n", err)
		}
	}

	paired, _ := agentHasPairedDevice(cAddr, token)
	// QR só se ninguém pareado ainda (ou --force-qr).
	needQR := s.ForceQR || !paired
	if !needQR {
		if mode == syncModeHere {
			fmt.Println("Terminal registrado na lista do celular (sem novo QR).")
			fmt.Println("No iPhone: abra o app e escolha esta sessão.")
		} else {
			fmt.Println("Mac já tem celular pareado — sessão sincronizada.")
			fmt.Println("Use: remotecli relay --force-qr   para novo QR.")
		}
		return nil
	}

	body, err := postAgent(cAddr, "/api/offer", nil, token)
	if err != nil {
		return err
	}
	var offerResp struct {
		Payload   []byte `json:"payload"`
		Signature []byte `json:"signature"`
		SignerKey []byte `json:"signer_key"`
		ClaimCode string `json:"claim_code"`
	}
	if err := json.Unmarshal(body, &offerResp); err != nil {
		return err
	}
	env := contracts.SignedEnvelope{
		Payload:   offerResp.Payload,
		Signature: offerResp.Signature,
		SignerKey: offerResp.SignerKey,
	}
	offer, err := pairing.VerifySignedOffer(env, time.Now())
	if err != nil {
		return err
	}
	qrPath := s.QROut
	if qrPath == "" {
		qrPath = defaultQRPath(s.SessionID)
	}
	qrURL := buildClaimURL(offer.Endpoint, offerResp.ClaimCode)
	if err := writeQRCode(qrPath, qrURL); err != nil {
		return err
	}
	fmt.Println()
	fmt.Println("Escaneie com o celular (mesma Wi‑Fi):")
	fmt.Println()
	if isInteractiveTerminal() {
		if err := printQRTerminal(qrURL); err != nil {
			fmt.Fprintf(os.Stderr, "Aviso: QR no terminal falhou: %v\n", err)
		}
		fmt.Println()
	} else {
		fmt.Println("(Terminal sem suporte visual ao QR — use o PNG ou o link abaixo.)")
		fmt.Println()
	}
	if !s.NoOpen {
		if err := openQRImage(qrPath); err != nil {
			fmt.Fprintf(os.Stderr, "Aviso: não abri o PNG automaticamente: %v\n", err)
		} else {
			fmt.Println("QR aberto em janela (Preview) — escaneie essa imagem.")
		}
	}
	fmt.Printf("Link: %s\n", qrURL)
	fmt.Printf("Arquivo: %s\n", qrPath)
	fmt.Println("Oferta expira em 2 minutos. No iPhone: aceite o certificado HTTPS e toque em Parear.")
	return nil
}

func agentHasPairedDevice(addr, token string) (bool, error) {
	b, err := getAgent(addr, "/api/devices", token)
	if err != nil {
		return false, err
	}
	var devices []any
	if err := json.Unmarshal(b, &devices); err != nil {
		// status autenticado às vezes devolve objeto — tenta campo devices
		var wrap struct {
			Devices []any `json:"devices"`
		}
		if err2 := json.Unmarshal(b, &wrap); err2 != nil {
			return false, err
		}
		return len(wrap.Devices) > 0, nil
	}
	return len(devices) > 0, nil
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

// localHTTPClient aceita o certificado autoassinado do agente (só CLI ↔ localhost/LAN).
func localHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: tlsutil.InsecureTLSConfig(),
		},
	}
}

func agentBaseURL(addr string) string {
	// Cliente sempre HTTPS (agente de produção). Testes unitários não usam estas helpers.
	return "https://" + addr
}

func getAgent(addr, path, localToken string) ([]byte, error) {
	u := agentBaseURL(addr) + path
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	if localToken != "" {
		req.Header.Set("X-Relay-Local-Token", localToken)
	}
	resp, err := localHTTPClient().Do(req)
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
	u := agentBaseURL(addr) + path
	var body io.Reader
	if data != nil {
		body = strings.NewReader(string(data))
	}
	req, _ := http.NewRequest(http.MethodPost, u, body)
	req.Header.Set("Content-Type", "application/json")
	if localToken != "" {
		req.Header.Set("X-Relay-Local-Token", localToken)
	}
	resp, err := localHTTPClient().Do(req)
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

const defaultAgentStartTimeout = 10 * time.Second

func defaultAgentAddr() string {
	if v := os.Getenv("RELAY_BIND_ADDR"); v != "" {
		return v
	}
	if v := os.Getenv("RELAY_ADDR"); v != "" {
		return v
	}
	return "0.0.0.0:24109"
}

// clientAddr converte endereço de bind em endereço para clientes locais.
func clientAddr(bind string) string {
	host, port, err := net.SplitHostPort(bind)
	if err != nil {
		return "127.0.0.1:24109"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		return "127.0.0.1:" + port
	}
	return bind
}

func resolveSessionID(id string) string {
	if id != "" {
		return id
	}
	return defaultSessionID()
}

func defaultSessionID() string {
	if v := os.Getenv("CODEX_THREAD_ID"); v != "" {
		return "codex-" + v
	}
	if v := os.Getenv("MAESTRI_TERMINAL_ID"); v != "" {
		return "maestri-" + v
	}
	return "default"
}

var (
	agentStartFunc = defaultAgentStart
	agentHealthy   = defaultAgentHealthy
)

func defaultAgentStart(selfExe, sessionID string) (*os.Process, error) {
	cmd := exec.Command(selfExe, "serve", sessionID)
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
	if err := cmd.Start(); err == nil {
		return cmd.Process, nil
	} else {
		// fallback nohup com log em tmp
		logPath := filepath.Join(os.TempDir(), fmt.Sprintf("relay-serve-%s.log", sessionID))
		logFile, errOpen := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if errOpen != nil {
			return nil, fmt.Errorf("falha ao iniciar agente (%w) e ao abrir log fallback: %v", err, errOpen)
		}
		cmd2 := exec.Command("nohup", selfExe, "serve", sessionID)
		cmd2.Stdout = logFile
		cmd2.Stderr = logFile
		cmd2.Stdin = nil
		if cmd2.SysProcAttr == nil {
			cmd2.SysProcAttr = &syscall.SysProcAttr{}
		}
		cmd2.SysProcAttr.Setsid = true
		if err2 := cmd2.Start(); err2 != nil {
			_ = logFile.Close()
			return nil, fmt.Errorf("falha ao iniciar agente (%w) e fallback nohup (%v); log: %s", err, err2, logPath)
		}
		return cmd2.Process, nil
	}
}

func defaultAgentHealthy(addr string) bool {
	client := &http.Client{
		Timeout: 800 * time.Millisecond,
		Transport: &http.Transport{
			TLSClientConfig: tlsutil.InsecureTLSConfig(),
		},
	}
	resp, err := client.Get(agentBaseURL(addr) + "/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func ensureAgentRunning(selfExe, sessionID, addr string, timeout time.Duration) (bool, error) {
	if agentHealthy(addr) {
		return false, nil
	}
	proc, err := agentStartFunc(selfExe, sessionID)
	if err != nil {
		return false, fmt.Errorf("falha ao iniciar agente: %w", err)
	}
	if err := proc.Release(); err != nil {
		return false, fmt.Errorf("falha ao liberar processo agente: %w", err)
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if agentHealthy(addr) {
			return true, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return true, fmt.Errorf("agente não respondeu em %s após %v", addr, timeout)
}

func ensureLocalToken(override, sessionID string, store keychain.Store, timeout time.Duration) (string, error) {
	if override != "" {
		return override, nil
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if token, err := agent.LoadLocalToken(store, sessionID); err == nil && token != "" {
			return token, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	if agentHealthy(clientAddr("0.0.0.0:24109")) {
		return "", fmt.Errorf("agente está no ar mas token local não encontrado; rode: remotecli relay no Mac para reiniciar o token")
	}
	return "", fmt.Errorf("token local não encontrado no Keychain; rode: remotecli relay no Mac")
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

const (
	tunnelKey         = "relay-tunnel-config"
	accessAccountHost = "host" // config de acesso é por máquina, não por sessão CLI
)

var errTunnelConfigMissing = errors.New("configuração de acesso não encontrada")

func tunnelAccount(sessionID string) string { return "host-" + sessionID } // legado

func saveAccessConfig(store keychain.Store, cfg tunnel.Config) error {
	cfg.Normalize()
	// Não persistir token em plain se vier só de env? Persistimos para o agente subir sozinho.
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return store.SaveSecret(tunnelKey, accessAccountHost, b)
}

func loadAccessConfig(store keychain.Store) (tunnel.Config, error) {
	// Conta canônica + legado por session
	for _, account := range []string{accessAccountHost, "host-default"} {
		b, err := store.LoadSecret(tunnelKey, account)
		if err != nil {
			continue
		}
		var cfg tunnel.Config
		if err := json.Unmarshal(b, &cfg); err != nil {
			continue
		}
		cfg.Normalize()
		return cfg, nil
	}
	return tunnel.Config{}, errTunnelConfigMissing
}

// saveTunnelConfig mantém API antiga (sessionID ignorado — config por host).
func saveTunnelConfig(store keychain.Store, sessionID string, cfg tunnel.Config) error {
	_ = sessionID
	return saveAccessConfig(store, cfg)
}

func loadTunnelConfig(store keychain.Store, sessionID string) (tunnel.Config, error) {
	_ = sessionID
	return loadAccessConfig(store)
}

func resolveTunnelConfig(enabled bool, name, hostname, urlStr, token string) tunnel.Config {
	cfg := tunnel.DefaultConfig()
	if enabled || token != "" {
		cfg.Mode = tunnel.ModeTunnel
		cfg.Enabled = true
	}
	if name != "" {
		cfg.Name = name
	}
	if hostname != "" {
		cfg.Hostname = hostname
	}
	if urlStr != "" {
		cfg.URL = urlStr
	}
	if token != "" {
		cfg.Token = token
	}
	cfg.Normalize()
	return cfg
}

func mergeTunnelWithSaved(store keychain.Store, flags tunnel.Config) tunnel.Config {
	saved, err := loadAccessConfig(store)
	if err != nil {
		flags.Normalize()
		return flags
	}
	// Salvo como base; flags explícitas sobrescrevem.
	out := saved
	if flags.Token != "" {
		out.Token = flags.Token
		out.Mode = tunnel.ModeTunnel
		out.Enabled = true
	}
	if flags.Hostname != "" {
		out.Hostname = flags.Hostname
	}
	if flags.URL != "" && flags.URL != tunnel.DefaultURL {
		out.URL = flags.URL
	} else if flags.URL != "" && out.URL == "" {
		out.URL = flags.URL
	}
	if flags.Name != "" && flags.Name != tunnel.DefaultName {
		out.Name = flags.Name
	}
	if flags.Enabled {
		out.Enabled = true
		out.Mode = tunnel.ModeTunnel
	}
	if flags.HostedURL != "" {
		out.HostedURL = flags.HostedURL
		out.Mode = tunnel.ModeHosted
	}
	if flags.Mode != "" {
		out.Mode = flags.Mode
	}
	out.Normalize()
	return out
}

func printAccessBanner(cfg tunnel.Config) {
	cfg.Normalize()
	switch cfg.AccessMode() {
	case tunnel.ModeTunnel:
		host := cfg.Hostname
		if host == "" {
			host = "(hostname no painel Cloudflare)"
		}
		fmt.Printf("Acesso: TUNNEL → %s (token do usuário)\n", host)
	case tunnel.ModeHosted:
		fmt.Printf("Acesso: HOSTED → %s (roadmap)\n", cfg.HostedURL)
	default:
		fmt.Println("Acesso: LAN (mesma Wi‑Fi). Remoto: remotecli access tunnel --token …")
	}
}

func requestTunnelStart(addr, token string) error {
	body, err := postAgent(addr, "/api/tunnel/start", nil, token)
	if err != nil {
		// postAgent inclui o body no erro em 4xx — classifica sem falhar o here/relay.
		msg := err.Error()
		if strings.Contains(msg, "token do Cloudflare Tunnel") {
			return tunnel.ErrTokenMissing
		}
		if strings.Contains(msg, "cloudflared não encontrado") {
			return tunnel.ErrCloudflaredMissing
		}
		if strings.Contains(msg, "tunnel desabilitado") {
			return tunnel.ErrTunnelDisabled
		}
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
		if strings.Contains(resp.Error, "tunnel desabilitado") {
			return tunnel.ErrTunnelDisabled
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

// printQRTerminal desenha o QR com módulos largos (██ / espaços) — legível no Terminal.app.
// Observação: o chat do Maestri/Grok costuma “amassar” esses caracteres; use o PNG nesses casos.
func printQRTerminal(content string) error {
	if content == "" {
		return fmt.Errorf("conteúdo QR vazio")
	}
	// Low = menos módulos; link curto (?c=XXXX) já reduz o tamanho.
	q, err := qrcode.New(content, qrcode.Low)
	if err != nil {
		return err
	}
	q.DisableBorder = false
	bitmap := q.Bitmap()
	// quiet zone extra + módulo 2 colunas de largura (mais fácil de focar a câmera)
	for _, row := range bitmap {
		var b strings.Builder
		for _, black := range row {
			if black {
				b.WriteString("██")
			} else {
				b.WriteString("  ")
			}
		}
		fmt.Println(b.String())
	}
	return nil
}

func isInteractiveTerminal() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

func openQRImage(path string) error {
	if path == "" {
		return fmt.Errorf("caminho vazio")
	}
	// macOS: Preview; em outros SO só deixa o arquivo no disco.
	if _, err := exec.LookPath("open"); err != nil {
		return err
	}
	cmd := exec.Command("open", path)
	return cmd.Start()
}

func buildOfferURL(endpoint string, envelope []byte) string {
	u := endpoint
	if u == "" {
		u = "https://127.0.0.1:24109"
	}
	return u + "/?offer=" + url.QueryEscape(string(envelope))
}

// buildClaimURL monta QR curto: o envelope completo fica no agente até o claim.
func buildClaimURL(endpoint, claimCode string) string {
	u := endpoint
	if u == "" {
		u = "https://127.0.0.1:24109"
	}
	if claimCode == "" {
		return u + "/"
	}
	return strings.TrimRight(u, "/") + "/?c=" + url.QueryEscape(claimCode)
}

func buildSessionMetadata(sessionID, windowID string, frontmost bool, targetPID int) contracts.SessionMetadata {
	pid := targetPID
	if pid <= 0 {
		pid = os.Getppid()
	}
	cwd, _ := os.Getwd()
	meta := contracts.SessionMetadata{
		Harness:         contracts.HarnessNative,
		NativeSessionID: sessionID,
		PID:             &pid,
		Frontmost:       frontmost,
		Cwd:             cwd,
		SessionKey:      sessionID,
		Title:           deriveCLITitle(sessionID, cwd),
	}
	if codex := os.Getenv("CODEX_THREAD_ID"); codex != "" {
		meta.Harness = contracts.HarnessCodex
		meta.CodexThreadID = &codex
		meta.SessionKey = "codex-" + codex
		meta.Title = "Codex"
	}
	if maestri := os.Getenv("MAESTRI_TERMINAL_ID"); maestri != "" {
		if meta.Harness == contracts.HarnessNative {
			meta.Harness = contracts.HarnessMaestri
			meta.SessionKey = "maestri-" + maestri
		}
		meta.MaestriTerminalID = &maestri
		meta.MaestriCLI = os.Getenv("MAESTRI_CLI")
		if meta.MaestriCLI == "" {
			meta.MaestriCLI = "maestri"
		}
		meta.MaestriSocket = os.Getenv("MAESTRI_SOCKET")
		meta.MaestriAgentName = discoverMaestriAgentName(meta.MaestriCLI)
		if meta.Title == "" || meta.Title == sessionID {
			if meta.MaestriAgentName != "" {
				meta.Title = meta.MaestriAgentName
			} else {
				meta.Title = "Maestri:" + maestri
			}
		}
	}
	if windowID != "" {
		meta.WindowID = &windowID
	}
	return meta
}

func deriveCLITitle(sessionID, cwd string) string {
	if sessionID != "default" {
		return sessionID
	}
	if cwd != "" {
		base := filepath.Base(cwd)
		if base != "" && base != "/" {
			return base
		}
	}
	return "Terminal"
}

const phoneBridgeAgent = "rcli-phone"

func copyFile(src, dst string) error {
	in, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, in, 0o755)
}

func ensurePhoneBridge() error {
	cli := os.Getenv("MAESTRI_CLI")
	if cli == "" {
		cli = "maestri"
	}
	out, err := exec.Command(cli, "list").CombinedOutput()
	if err != nil {
		return fmt.Errorf("maestri list: %w", err)
	}
	list := string(out)
	if strings.Contains(list, `"`+phoneBridgeAgent+`"`) || strings.Contains(list, phoneBridgeAgent) {
		// tenta conectar orquestrador ↔ bridge (idempotente se já ligado)
		self := parseMaestriAgentName(list)
		if self != "" && self != phoneBridgeAgent {
			_ = exec.Command(cli, "connect", self, phoneBridgeAgent).Run()
		}
		return nil
	}
	// Recruta terminal ponte: só processa outbox (sem modelo AI caro).
	selfExe, err := os.Executable()
	if err != nil {
		return err
	}
	// Nota: ~/.local/bin/remotecli phone-bridge é morto (SIGKILL) neste Mac;
	// usamos cópia rcli-bridge no mesmo dir.
	bridgeBin := selfExe
	if dir := filepath.Dir(selfExe); dir != "" {
		alt := filepath.Join(dir, "rcli-bridge")
		if err := copyFile(selfExe, alt); err == nil {
			bridgeBin = alt
		}
	}
	cmdLine := fmt.Sprintf("bash -lc 'while true; do %q phone-bridge --once; sleep 1; done'", bridgeBin)
	recruit := exec.Command(cli, "recruit", phoneBridgeAgent, "--command", cmdLine)
	recruit.Env = os.Environ()
	b, err := recruit.CombinedOutput()
	if err != nil {
		return fmt.Errorf("recruit %s: %w (%s)", phoneBridgeAgent, err, strings.TrimSpace(string(b)))
	}
	self := parseMaestriAgentName(list)
	if self == "" {
		self = parseMaestriAgentName(string(b))
	}
	if self != "" {
		_ = exec.Command(cli, "connect", self, phoneBridgeAgent).Run()
	}
	fmt.Printf("Ponte %s criada no Maestri (mensagens do celular → conversa real).\n", phoneBridgeAgent)
	return nil
}

// discoverMaestriAgentName parseia `maestri list` e retorna o nome do agente "You".
func discoverMaestriAgentName(cli string) string {
	if cli == "" {
		cli = "maestri"
	}
	cmd := exec.Command(cli, "list")
	cmd.Env = os.Environ()
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return parseMaestriAgentName(string(out))
}

func parseMaestriAgentName(output string) string {
	lines := strings.Split(output, "\n")
	inYou := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "You:") {
			inYou = true
			continue
		}
		if inYou {
			if strings.HasPrefix(trimmed, "Connected agents:") || trimmed == "" {
				break
			}
			if strings.HasPrefix(trimmed, "-") {
				name := extractMaestriNameField(trimmed)
				if name != "" {
					return name
				}
			}
		}
	}
	return ""
}

func extractMaestriNameField(line string) string {
	// Esperado: - name: "Codex #2", role: "..."  OU  - name: "RelayMulti"
	if !strings.Contains(line, "name:") {
		return ""
	}
	// extrai o primeiro valor entre aspas após name:
	idx := strings.Index(line, "name:")
	rest := line[idx+len("name:"):]
	q1 := strings.Index(rest, `"`)
	if q1 < 0 {
		return ""
	}
	rest = rest[q1+1:]
	q2 := strings.Index(rest, `"`)
	if q2 < 0 {
		return ""
	}
	return rest[:q2]
}

func localIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		if ipNet, ok := a.(*net.IPNet); ok && !ipNet.IP.IsLoopback() {
			if ipNet.IP.To4() != nil {
				return ipNet.IP.String()
			}
		}
	}
	return ""
}

func main() {
	var cli CLI
	ctx := kong.Parse(&cli,
		kong.Name("remotecli"),
		kong.Description("Remote CliControl — controle remoto de CLIs no Mac"),
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

package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
)

// Transport é a interface de transporte JSON-RPC (stdio, unix socket, etc).
type Transport interface {
	// Start inicia a conexão e encaminha mensagens lidas para onMessage.
	Start(ctx context.Context, onMessage func([]byte)) error
	// Send envia um payload já serializado.
	Send(ctx context.Context, data []byte) error
	// Stop encerra o transporte.
	Stop(ctx context.Context) error
	// Running indica se o transporte ainda está ativo.
	Running() bool
}

// messageCounter gera IDs JSON-RPC monotonicamente.
var messageCounter atomic.Uint64

func nextID() uint64 { return messageCounter.Add(1) }

// jsonRPCMessage é a forma canônica de uma mensagem JSON-RPC 2.0.
type jsonRPCMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      uint64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data,omitempty"`
}

func newRequest(id uint64, method string, params any) ([]byte, error) {
	p, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	return json.Marshal(jsonRPCMessage{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  p,
	})
}

func newNotification(method string, params any) ([]byte, error) {
	p, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	return json.Marshal(jsonRPCMessage{
		JSONRPC: "2.0",
		Method:  method,
		Params:  p,
	})
}

func newResponse(id uint64, result any) ([]byte, error) {
	var r json.RawMessage
	if result != nil {
		var err error
		r, err = json.Marshal(result)
		if err != nil {
			return nil, err
		}
	}
	return json.Marshal(jsonRPCMessage{
		JSONRPC: "2.0",
		ID:      id,
		Result:  r,
	})
}

// baseTransport contém mutex e estado de execução compartilhado entre transportes.
type baseTransport struct {
	mu      sync.RWMutex
	running bool
}

func (t *baseTransport) setRunning(v bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.running = v
}

func (t *baseTransport) Running() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.running
}

// SocketTransport conecta em um Unix domain socket.
type SocketTransport struct {
	baseTransport
	path   string
	conn   net.Conn
	reader *bufio.Reader
	writer *bufio.Writer
	muIO   sync.Mutex
}

// NewSocketTransport cria transporte para socket Unix.
func NewSocketTransport(path string) *SocketTransport {
	return &SocketTransport{path: path}
}

func (t *SocketTransport) Start(ctx context.Context, onMessage func([]byte)) error {
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "unix", t.path)
	if err != nil {
		return fmt.Errorf("codex socket dial %s: %w", t.path, err)
	}
	t.setRunning(true)
	t.muIO.Lock()
	t.conn = conn
	t.reader = bufio.NewReader(conn)
	t.writer = bufio.NewWriter(conn)
	t.muIO.Unlock()

	go t.readLoop(onMessage)
	return nil
}

func (t *SocketTransport) readLoop(onMessage func([]byte)) {
	defer t.setRunning(false)
	for t.Running() {
		line, err := t.reader.ReadBytes('\n')
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				// log.Printf("codex socket read error: %v", err)
			}
			return
		}
		if len(line) == 0 {
			continue
		}
		if onMessage != nil {
			onMessage(line)
		}
	}
}

func (t *SocketTransport) Send(ctx context.Context, data []byte) error {
	t.muIO.Lock()
	defer t.muIO.Unlock()
	if !t.Running() || t.writer == nil {
		return errors.New("transporte codex não conectado")
	}
	if _, err := t.writer.Write(data); err != nil {
		return err
	}
	if _, err := t.writer.WriteString("\n"); err != nil {
		return err
	}
	return t.writer.Flush()
}

func (t *SocketTransport) Stop(ctx context.Context) error {
	t.setRunning(false)
	t.muIO.Lock()
	defer t.muIO.Unlock()
	if t.conn != nil {
		return t.conn.Close()
	}
	return nil
}

// StdioTransport executa um comando e usa stdin/stdout para JSON-RPC.
type StdioTransport struct {
	baseTransport
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	reader *bufio.Reader
}

// NewStdioTransport inicia um processo filho e retorna o transporte stdio.
func NewStdioTransport(ctx context.Context, name string, args ...string) (*StdioTransport, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, err
	}
	t := &StdioTransport{
		cmd:    cmd,
		stdin:  stdin,
		stdout: stdout,
		reader: bufio.NewReader(stdout),
	}
	t.setRunning(true)
	return t, nil
}

func (t *StdioTransport) Start(ctx context.Context, onMessage func([]byte)) error {
	go t.readLoop(onMessage)
	go t.waitDone()
	return nil
}

func (t *StdioTransport) readLoop(onMessage func([]byte)) {
	defer t.setRunning(false)
	for t.Running() {
		line, err := t.reader.ReadBytes('\n')
		if err != nil {
			if !errors.Is(err, io.EOF) {
				// log.Printf("codex stdio read error: %v", err)
			}
			return
		}
		if len(line) == 0 {
			continue
		}
		if onMessage != nil {
			onMessage(line)
		}
	}
}

func (t *StdioTransport) waitDone() {
	_ = t.cmd.Wait()
	t.setRunning(false)
}

func (t *StdioTransport) Send(ctx context.Context, data []byte) error {
	if !t.Running() {
		return errors.New("transporte codex não conectado")
	}
	if _, err := t.stdin.Write(data); err != nil {
		return err
	}
	if _, err := t.stdin.Write([]byte("\n")); err != nil {
		return err
	}
	return nil
}

func (t *StdioTransport) Stop(ctx context.Context) error {
	t.setRunning(false)
	if t.stdin != nil {
		_ = t.stdin.Close()
	}
	if t.cmd != nil && t.cmd.Process != nil {
		_ = t.cmd.Process.Signal(os.Interrupt)
	}
	return nil
}

// FakeTransport é um transporte fake para testes.
type FakeTransport struct {
	baseTransport
	mu          sync.Mutex
	sent        [][]byte
	messages    chan []byte
	onSendCbs   []func([]byte)
	onSendOnce  []func([]byte)
}

// NewFakeTransport cria um transporte fake. bufferedMessages define a capacidade do canal de mensagens recebidas.
func NewFakeTransport(bufferedMessages int) *FakeTransport {
	return &FakeTransport{messages: make(chan []byte, bufferedMessages)}
}

func (t *FakeTransport) Start(ctx context.Context, onMessage func([]byte)) error {
	t.setRunning(true)
	go func() {
		for t.Running() {
			select {
			case <-ctx.Done():
				t.setRunning(false)
				return
			case msg := <-t.messages:
				if onMessage != nil {
					onMessage(msg)
				}
			}
		}
	}()
	return nil
}

func (t *FakeTransport) Send(ctx context.Context, data []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sent = append(t.sent, data)
	cbs := make([]func([]byte), len(t.onSendCbs))
	copy(cbs, t.onSendCbs)
	for _, cb := range t.onSendOnce {
		cbs = append(cbs, cb)
	}
	t.onSendOnce = nil
	for _, cb := range cbs {
		cb(data)
	}
	return nil
}

func (t *FakeTransport) Stop(ctx context.Context) error {
	t.setRunning(false)
	return nil
}

// Sent retorna uma cópia das mensagens enviadas.
func (t *FakeTransport) Sent() [][]byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([][]byte, len(t.sent))
	copy(out, t.sent)
	return out
}

// InjectMessage simula uma mensagem recebida do app-server.
func (t *FakeTransport) InjectMessage(msg []byte) {
	t.messages <- msg
}

// OnSend registra um callback persistente para mensagens enviadas.
func (t *FakeTransport) OnSend(cb func([]byte)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.onSendCbs = append(t.onSendCbs, cb)
}

// OnSendOnce registra um callback de uma única vez para a próxima mensagem enviada.
func (t *FakeTransport) OnSendOnce(cb func([]byte)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.onSendOnce = append(t.onSendOnce, cb)
}

// DefaultSocketPath retorna o path padrão do socket Codex.
func DefaultSocketPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex", "ipc", "ipc.sock")
}

// TransportFactory cria o transporte real a partir da variável RELAY_CODEX_TRANSPORT.
func TransportFactory(ctx context.Context) (Transport, error) {
	mode := os.Getenv("RELAY_CODEX_TRANSPORT")
	switch mode {
	case "stdio", "":
		return NewStdioTransport(ctx, "codex", "app-server", "--listen", "stdio://")
	case "socket":
		path := DefaultSocketPath()
		if path == "" {
			return nil, errors.New("não foi possível determinar home dir para socket codex")
		}
		return NewSocketTransport(path), nil
	default:
		return nil, fmt.Errorf("RELAY_CODEX_TRANSPORT inválido: %q (use stdio ou socket)", mode)
	}
}

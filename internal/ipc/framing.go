// Package ipc implementa comunicação Go↔helper Swift por Unix domain socket.
//
// Framing binário/documentado:
//   [4 bytes length big-endian][1 byte message type][payload]
//
// Tipos de mensagem:
//   0x01 H264 NAL/access unit (payload = H264 annexb ou start-code + NAL)
//   0x02 display geometry JSON (payload = UTF-8 JSON do DisplayGeometry)
//   0x03 input event JSON (payload = UTF-8 JSON de mouse/keyboard)
//   0x04 clipboard text (payload = UTF-8 text)
//   0x05 ping / 0x06 pong (payload vazio)
//   0x07 auth challenge response (payload = HMAC-SHA256 de segredo + nonce)
//
// Handshake inicial: o helper conecta, o servidor Go envia nonce de 16 bytes,
// helper responde com type 0x07 contendo HMAC-SHA256(segredo, nonce).
package ipc

import (
	"bufio"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/bortolidiego/relay/internal/geometry"
)

const (
	MsgTypeH264      byte = 0x01
	MsgTypeGeometry  byte = 0x02
	MsgTypeInput     byte = 0x03
	MsgTypeClipboard byte = 0x04
	MsgTypePing      byte = 0x05
	MsgTypePong      byte = 0x06
	MsgTypeAuth      byte = 0x07
)

// Frame representa uma mensagem do protocolo IPC.
type Frame struct {
	Type    byte
	Payload []byte
}

// ReadFrame lê um frame de um Reader.
func ReadFrame(r io.Reader) (*Frame, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(header[:])
	if length == 0 {
		return nil, errors.New("frame vazio")
	}
	if length > 64*1024*1024 {
		return nil, errors.New("frame muito grande")
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return &Frame{Type: body[0], Payload: body[1:]}, nil
}

// WriteFrame escreve um frame em um Writer.
func WriteFrame(w io.Writer, msgType byte, payload []byte) error {
	length := 1 + len(payload)
	if length > 64*1024*1024 {
		return errors.New("payload muito grande")
	}
	buf := make([]byte, 4+length)
	binary.BigEndian.PutUint32(buf[0:4], uint32(length))
	buf[4] = msgType
	copy(buf[5:], payload)
	_, err := w.Write(buf)
	return err
}

// AuthResponse gera HMAC-SHA256(secret, nonce) para handshake.
func AuthResponse(secret, nonce []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write(nonce)
	return mac.Sum(nil)
}

// SecureSocketDir cria diretório seguro 0700 para o socket.
func SecureSocketDir(base string) (string, error) {
	if base == "" {
		base, _ = os.UserCacheDir()
		if base == "" {
			base = os.TempDir()
		}
	}
	// Verifica se o caminho base é muito longo para unix socket (max ~104 chars no macOS).
	if len(base) > 80 {
		return "", errors.New("caminho base muito longo para unix socket")
	}
	dir := filepath.Join(base, "relay-ipc")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return "", err
	}
	if len(dir) > 100 {
		return "", errors.New("caminho do socket muito longo")
	}
	return dir, nil
}

// Server escuta conexões IPC autenticadas.
type Server struct {
	mu         sync.RWMutex
	secret     []byte
	listener   net.Listener
	onH264     func([]byte)
	onGeometry func(geometry.DisplayGeometry)
	onInput    func([]byte)
	onClipboard func([]byte)
	clients    map[net.Conn]struct{}
}

// NewServer cria servidor IPC.
func NewServer(secret []byte) *Server {
	return &Server{
		secret:  append([]byte(nil), secret...),
		clients: make(map[net.Conn]struct{}),
	}
}

// Listen inicia o socket Unix em diretório seguro.
func (s *Server) Listen(dir string) (string, error) {
	dir, err := SecureSocketDir(dir)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "relay.sock")
	_ = os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		return "", err
	}
	if err := os.Chmod(path, 0600); err != nil {
		l.Close()
		os.Remove(path)
		return "", err
	}
	s.listener = l
	go s.acceptLoop()
	return path, nil
}

func (s *Server) acceptLoop() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *Server) handle(conn net.Conn) {
	defer s.removeClient(conn)
	defer conn.Close()

	if err := s.handshake(conn); err != nil {
		return
	}

	s.addClient(conn)
	reader := bufio.NewReader(conn)
	for {
		frame, err := ReadFrame(reader)
		if err != nil {
			return
		}
		s.dispatch(frame)
	}
}

func (s *Server) handshake(conn net.Conn) error {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	if _, err := conn.Write(nonce); err != nil {
		return err
	}
	reader := bufio.NewReader(conn)
	frame, err := ReadFrame(reader)
	if err != nil {
		return err
	}
	if frame.Type != MsgTypeAuth {
		return errors.New("handshake: tipo inesperado")
	}
	expected := AuthResponse(s.secret, nonce)
	if !hmac.Equal(expected, frame.Payload) {
		return errors.New("handshake: segredo inválido")
	}
	return nil
}

func (s *Server) dispatch(frame *Frame) {
	switch frame.Type {
	case MsgTypeH264:
		s.mu.RLock()
		cb := s.onH264
		s.mu.RUnlock()
		if cb != nil {
			cb(append([]byte(nil), frame.Payload...))
		}
	case MsgTypeGeometry:
		var g geometry.DisplayGeometry
		if err := json.Unmarshal(frame.Payload, &g); err == nil {
			s.mu.RLock()
			cb := s.onGeometry
			s.mu.RUnlock()
			if cb != nil {
				cb(g)
			}
		}
	case MsgTypeInput:
		s.mu.RLock()
		cb := s.onInput
		s.mu.RUnlock()
		if cb != nil {
			cb(append([]byte(nil), frame.Payload...))
		}
	case MsgTypeClipboard:
		s.mu.RLock()
		cb := s.onClipboard
		s.mu.RUnlock()
		if cb != nil {
			cb(append([]byte(nil), frame.Payload...))
		}
	case MsgTypePing:
		// noop; pong enviado pelo client se necessário
	}
}

func (s *Server) addClient(conn net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clients[conn] = struct{}{}
}

func (s *Server) removeClient(conn net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.clients, conn)
}

// BroadcastGeometry envia geometria para todos os helpers conectados.
func (s *Server) BroadcastGeometry(g geometry.DisplayGeometry) error {
	b, err := json.Marshal(g)
	if err != nil {
		return err
	}
	return s.broadcast(MsgTypeGeometry, b)
}

// BroadcastInput envia evento de input para helper.
func (s *Server) BroadcastInput(payload []byte) error {
	return s.broadcast(MsgTypeInput, payload)
}

// BroadcastClipboard envia clipboard para helper.
func (s *Server) BroadcastClipboard(payload []byte) error {
	return s.broadcast(MsgTypeClipboard, payload)
}

func (s *Server) broadcast(msgType byte, payload []byte) error {
	s.mu.RLock()
	clients := make([]net.Conn, 0, len(s.clients))
	for c := range s.clients {
		clients = append(clients, c)
	}
	s.mu.RUnlock()
	var firstErr error
	for _, c := range clients {
		if err := WriteFrame(c, msgType, payload); err != nil {
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// Close finaliza o servidor.
func (s *Server) Close() error {
	if s.listener != nil {
		return s.listener.Close()
	}
	return nil
}

// OnH264 registra callback.
func (s *Server) OnH264(cb func([]byte)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onH264 = cb
}

// OnGeometry registra callback.
func (s *Server) OnGeometry(cb func(geometry.DisplayGeometry)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onGeometry = cb
}

// OnInput registra callback.
func (s *Server) OnInput(cb func([]byte)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onInput = cb
}

// OnClipboard registra callback.
func (s *Server) OnClipboard(cb func([]byte)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onClipboard = cb
}

// Addr retorna o endereço do socket.
func (s *Server) Addr() string {
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return ""
}

// Ensure imports used.
var _ = fmt.Sprintf
var _ = time.Now

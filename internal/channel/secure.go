// Package channel implementa envelope cifrado AES-256-GCM com AAD e replay guard para DataChannels Relay.
package channel

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

const (
	// TagSize é o tamanho da tag GCM.
	TagSize = 16
	// NonceSize é o tamanho do nonce fixo de 12 bytes.
	NonceSize = 12
	// HeaderSize = 4 (len) + 1 (msg type) + 4 (seq) + 12 (nonce).
	HeaderSize = 21
)

// MessageType identifica o tipo de payload no canal.
type MessageType byte

const (
	MsgTypeControl   MessageType = 0x01
	MsgTypeClipboard MessageType = 0x02
	MsgTypeFile      MessageType = 0x03
	MsgTypeGeometry  MessageType = 0x04
	MsgTypeInput     MessageType = 0x05
)

// AADLabels definem o contexto de autenticação.
const (
	AADLabelControl   = "relay-ctl-v1"
	AADLabelClipboard = "relay-clip-v1"
	AADLabelFile      = "relay-file-v1"
	AADLabelGeometry  = "relay-geom-v1"
	AADLabelInput     = "relay-input-v1"
)

// SecureChannel cifra/decifra mensagens e evita replay.
type SecureChannel struct {
	key        []byte
	deviceID   string
	sessionID  string
	channelID  string
	seqOut     uint32
	seen       map[uint32]time.Time
	seenMu     sync.Mutex
	maxAge     time.Duration
	windowSize int
}

// NewSecureChannel cria canal seguro. A chave deve ter 32 bytes.
func NewSecureChannel(key []byte, deviceID, sessionID, channelID string) (*SecureChannel, error) {
	if len(key) != 32 {
		return nil, errors.New("chave AES deve ter 32 bytes")
	}
	if deviceID == "" || sessionID == "" || channelID == "" {
		return nil, errors.New("deviceID, sessionID e channelID são obrigatórios")
	}
	return &SecureChannel{
		key:        append([]byte(nil), key...),
		deviceID:   deviceID,
		sessionID:  sessionID,
		channelID:  channelID,
		seen:       make(map[uint32]time.Time),
		maxAge:     5 * time.Minute,
		windowSize: 1024,
	}, nil
}

// Encrypt gera envelope cifrado: [4 bytes len][1 byte type][4 bytes seq][12 bytes nonce][ciphertext+tag].
// AAD = label + deviceID + sessionID + channelID.
func (s *SecureChannel) Encrypt(msgType MessageType, plaintext []byte) ([]byte, error) {
	if len(plaintext) > 64*1024*1024 {
		return nil, errors.New("payload muito grande")
	}
	label := aadLabel(msgType)
	if label == "" {
		return nil, fmt.Errorf("tipo de mensagem inválido: %d", msgType)
	}
	aad := buildAAD(label, s.deviceID, s.sessionID, s.channelID)
	nonce := make([]byte, NonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	s.seqOut++
	seq := s.seqOut
	ciphertext := gcm.Seal(nil, nonce, plaintext, aad)
	if len(ciphertext) < TagSize {
		return nil, errors.New("ciphertext muito curto")
	}
	total := HeaderSize + len(ciphertext)
	out := make([]byte, 4+total)
	binary.BigEndian.PutUint32(out[0:4], uint32(total))
	out[4] = byte(msgType)
	binary.BigEndian.PutUint32(out[5:9], seq)
	copy(out[9:21], nonce)
	copy(out[21:], ciphertext)
	return out, nil
}

// DecryptedMessage é o resultado de uma decifração bem-sucedida.
type DecryptedMessage struct {
	Type      MessageType
	Sequence  uint32
	Plaintext []byte
}

// Decrypt valida AAD, decifra e rejeita replay.
func (s *SecureChannel) Decrypt(data []byte) (*DecryptedMessage, error) {
	if len(data) < 4+HeaderSize+TagSize {
		return nil, errors.New("mensagem muito curta")
	}
	frameLen := binary.BigEndian.Uint32(data[0:4])
	if int(frameLen)+4 != len(data) {
		return nil, errors.New("tamanho de frame inconsistente")
	}
	msgType := MessageType(data[4])
	label := aadLabel(msgType)
	if label == "" {
		return nil, fmt.Errorf("tipo de mensagem inválido: %d", msgType)
	}
	seq := binary.BigEndian.Uint32(data[5:9])
	nonce := data[9:21]
	cipherLen := int(frameLen) - HeaderSize
	if cipherLen < TagSize || 21+cipherLen > len(data) {
		return nil, errors.New("tamanho de ciphertext inválido")
	}
	ciphertext := data[21 : 21+cipherLen]
	aad := buildAAD(label, s.deviceID, s.sessionID, s.channelID)
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, fmt.Errorf("decifração falhou: %w", err)
	}
	if err := s.checkReplay(seq); err != nil {
		return nil, err
	}
	return &DecryptedMessage{Type: msgType, Sequence: seq, Plaintext: plaintext}, nil
}

func (s *SecureChannel) checkReplay(seq uint32) error {
	s.seenMu.Lock()
	defer s.seenMu.Unlock()
	now := time.Now()
	if _, ok := s.seen[seq]; ok {
		return fmt.Errorf("replay detectado: seq=%d", seq)
	}
	s.seen[seq] = now
	if len(s.seen) > s.windowSize {
		for k, v := range s.seen {
			if now.Sub(v) > s.maxAge {
				delete(s.seen, k)
			}
		}
	}
	return nil
}

func buildAAD(label, deviceID, sessionID, channelID string) []byte {
	// AAD fixo e pequeno: label + device + session + channel.
	b := make([]byte, 0, len(label)+len(deviceID)+len(sessionID)+len(channelID)+4)
	b = append(b, []byte(label)...)
	b = append(b, 0)
	b = append(b, []byte(deviceID)...)
	b = append(b, 0)
	b = append(b, []byte(sessionID)...)
	b = append(b, 0)
	b = append(b, []byte(channelID)...)
	return b
}

func aadLabel(t MessageType) string {
	switch t {
	case MsgTypeControl:
		return AADLabelControl
	case MsgTypeClipboard:
		return AADLabelClipboard
	case MsgTypeFile:
		return AADLabelFile
	case MsgTypeGeometry:
		return AADLabelGeometry
	case MsgTypeInput:
		return AADLabelInput
	}
	return ""
}

// ConstantTimeCompare compara slices de bytes em tempo constante.
func ConstantTimeCompare(a, b []byte) bool {
	return subtle.ConstantTimeCompare(a, b) == 1
}

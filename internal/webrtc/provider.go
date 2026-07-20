// Package webrtc contém abstrações de ICE/TURN para o transporte Relay.
package webrtc

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
)

// ICEProvider fornece configuração ICE (servidores STUN/TURN).
type ICEProvider interface {
	Servers() ([]webrtc.ICEServer, error)
}

// TURNProvider fornece credenciais TURN de curta duração.
type TURNProvider interface {
	ICECredentials(ctx context.Context) (username, password string, urls []string, ttl time.Duration, err error)
}

// DefaultICEProvider retorna STUN públicos seguros para descoberta de candidatos
// em LAN/WAN. Não inclui TURN; TURN real fica para Marco 4.
type DefaultICEProvider struct{}

func (DefaultICEProvider) Servers() ([]webrtc.ICEServer, error) {
	return []webrtc.ICEServer{
		{URLs: []string{"stun:stun.cloudflare.com:3478"}},
		{URLs: []string{"stun:stun.l.google.com:19302"}},
	}, nil
}

// StaticICEProvider retorna uma lista fixa sem secrets dinâmicos.
type StaticICEProvider struct {
	mu      sync.RWMutex
	servers []webrtc.ICEServer
}

// NewStaticICEProvider cria provider com servidores pré-configurados.
func NewStaticICEProvider(servers []webrtc.ICEServer) *StaticICEProvider {
	return &StaticICEProvider{servers: append([]webrtc.ICEServer(nil), servers...)}
}

func (s *StaticICEProvider) Servers() ([]webrtc.ICEServer, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]webrtc.ICEServer(nil), s.servers...), nil
}

// ShortLivedTURNProvider fornece credenciais TURN temporárias de um secret mestre configurado.
type ShortLivedTURNProvider struct {
	mu       sync.RWMutex
	baseURLs []string
	secret   []byte
	issuer   string
}

// NewShortLivedTURNProvider cria provider. secret deve ter pelo menos 32 bytes.
func NewShortLivedTURNProvider(baseURLs []string, secret []byte, issuer string) (*ShortLivedTURNProvider, error) {
	if len(secret) < 32 {
		return nil, errors.New("secret TURN deve ter pelo menos 32 bytes")
	}
	return &ShortLivedTURNProvider{
		baseURLs: append([]string(nil), baseURLs...),
		secret:   append([]byte(nil), secret...),
		issuer:   issuer,
	}, nil
}

func (p *ShortLivedTURNProvider) ICECredentials(ctx context.Context) (string, string, []string, time.Duration, error) {
	// Stub: em marco futuro gera token HMAC/expires. Aqui retorna erro intencional para não expor secret.
	return "", "", nil, 0, errors.New("TURN real desabilitado no Marco 3")
}

// CompositeICEProvider combina default + TURN quando configurado.
type CompositeICEProvider struct {
	base []webrtc.ICEServer
	turn TURNProvider
}

// NewCompositeICEProvider cria provider composto.
func NewCompositeICEProvider(base ICEProvider, turn TURNProvider) (*CompositeICEProvider, error) {
	if base == nil {
		base = DefaultICEProvider{}
	}
	servers, err := base.Servers()
	if err != nil {
		return nil, err
	}
	if len(servers) == 0 {
		return nil, errors.New("base ICE provider vazio")
	}
	return &CompositeICEProvider{base: servers, turn: turn}, nil
}

func (c *CompositeICEProvider) Servers() ([]webrtc.ICEServer, error) {
	// Aplica base + TURN se configurado. TURN real é Marco 4.
	servers := append([]webrtc.ICEServer(nil), c.base...)
	if c.turn != nil {
		// Stub: em Marco 4 chamar c.turn.ICECredentials(...).
		// Hoje mantemos TURN fora para não expor secrets.
	}
	return servers, nil
}

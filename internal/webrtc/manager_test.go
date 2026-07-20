package webrtc

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bortolidiego/relay/internal/channel"
	"github.com/bortolidiego/relay/internal/geometry"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/require"
)

func TestPeerManagerCreateOfferAnswer(t *testing.T) {
	key := make([]byte, 32)
	key[0] = 1
	host, err := NewPeerManager(Config{DeviceID: "dev-web", PeerID: "host", SessionID: "s1", SharedKey: key, ICEProvider: DefaultICEProvider{}, Initiator: true})
	require.NoError(t, err)
	defer host.Close()

	client, err := NewPeerManager(Config{DeviceID: "dev-web", PeerID: "client", SessionID: "s1", SharedKey: key, ICEProvider: DefaultICEProvider{}, Initiator: false})
	require.NoError(t, err)
	defer client.Close()

	require.NoError(t, ConnectTo(host, client))

	require.Eventually(t, func() bool {
		return host.IsConnected() && client.IsConnected()
	}, 5*time.Second, 50*time.Millisecond)
}

func waitDataChannelOpen(t *testing.T, dc *webrtc.DataChannel) {
	t.Helper()
	require.Eventually(t, func() bool {
		return dc.ReadyState() == webrtc.DataChannelStateOpen
	}, 3*time.Second, 10*time.Millisecond)
}

func TestDataChannelEncryptedMessage(t *testing.T) {
	key := make([]byte, 32)
	key[0] = 2
	host, err := NewPeerManager(Config{DeviceID: "dev-web", PeerID: "host", SessionID: "s2", SharedKey: key, ICEProvider: DefaultICEProvider{}, Initiator: true})
	require.NoError(t, err)
	defer host.Close()

	client, err := NewPeerManager(Config{DeviceID: "dev-web", PeerID: "client", SessionID: "s2", SharedKey: key, ICEProvider: DefaultICEProvider{}, Initiator: false})
	require.NoError(t, err)
	defer client.Close()

	require.NoError(t, ConnectTo(host, client))

	require.Eventually(t, func() bool {
		return host.IsConnected() && client.IsConnected()
	}, 5*time.Second, 50*time.Millisecond)

	waitDataChannelOpen(t, host.clipboardChannel())
	waitDataChannelOpen(t, client.clipboardChannel())

	got := make(chan []byte, 1)
	client.OnClipboard(func(b []byte) { got <- b })

	require.NoError(t, host.SendClipboard([]byte("hello")))
	select {
	case b := <-got:
		require.Equal(t, "hello", string(b))
	case <-time.After(3 * time.Second):
		t.Fatal("não recebeu clipboard")
	}
}

func TestGeometryDispatch(t *testing.T) {
	key := make([]byte, 32)
	key[0] = 3
	host, err := NewPeerManager(Config{DeviceID: "dev-web", PeerID: "host", SessionID: "s3", SharedKey: key, ICEProvider: DefaultICEProvider{}, Initiator: true})
	require.NoError(t, err)
	defer host.Close()

	client, err := NewPeerManager(Config{DeviceID: "dev-web", PeerID: "client", SessionID: "s3", SharedKey: key, ICEProvider: DefaultICEProvider{}, Initiator: false})
	require.NoError(t, err)
	defer client.Close()

	require.NoError(t, ConnectTo(host, client))

	require.Eventually(t, func() bool {
		return host.IsConnected() && client.IsConnected()
	}, 5*time.Second, 50*time.Millisecond)

	waitDataChannelOpen(t, host.controlChannel())
	waitDataChannelOpen(t, client.controlChannel())

	recv := make(chan geometry.DisplayGeometry, 1)
	client.OnGeometry(func(g geometry.DisplayGeometry) { recv <- g })

	g := geometry.DisplayGeometry{Capture: geometry.Rect{0, 0, 2560, 1440}, Video: geometry.Rect{0, 0, 1280, 720}}
	require.NoError(t, host.SendGeometry(g))
	select {
	case got := <-recv:
		require.InDelta(t, 2560, got.Capture.Width, 0.001)
	case <-time.After(3 * time.Second):
		t.Fatal("não recebeu geometria")
	}
}

func TestPlaintextRejected(t *testing.T) {
	key := make([]byte, 32)
	key[0] = 4
	host, err := NewPeerManager(Config{DeviceID: "dev-web", PeerID: "host", SessionID: "s4", SharedKey: key, ICEProvider: DefaultICEProvider{}, Initiator: true})
	require.NoError(t, err)
	defer host.Close()

	client, err := NewPeerManager(Config{DeviceID: "dev-web", PeerID: "client", SessionID: "s4", SharedKey: key, ICEProvider: DefaultICEProvider{}, Initiator: false})
	require.NoError(t, err)
	defer client.Close()

	require.NoError(t, ConnectTo(host, client))

	require.Eventually(t, func() bool {
		return host.IsConnected() && client.IsConnected()
	}, 5*time.Second, 50*time.Millisecond)

	waitDataChannelOpen(t, host.clipboardChannel())
	waitDataChannelOpen(t, client.clipboardChannel())

	// Enviar plaintext no canal de clipboard deve ser rejeitado pelo receptor.
	seen := make(chan bool, 1)
	client.OnClipboard(func(_ []byte) { seen <- true })
	_ = host.SendRaw([]byte("plain"))
	select {
	case <-seen:
		t.Fatal("plaintext não deveria ter sido aceito")
	case <-time.After(500 * time.Millisecond):
		// ok
	}
}

func TestReplayRejectedOnDataChannel(t *testing.T) {
	key := make([]byte, 32)
	key[0] = 5
	host, err := NewPeerManager(Config{DeviceID: "dev-web", PeerID: "host", SessionID: "s5", SharedKey: key, ICEProvider: DefaultICEProvider{}, Initiator: true})
	require.NoError(t, err)
	defer host.Close()

	client, err := NewPeerManager(Config{DeviceID: "dev-web", PeerID: "client", SessionID: "s5", SharedKey: key, ICEProvider: DefaultICEProvider{}, Initiator: false})
	require.NoError(t, err)
	defer client.Close()

	require.NoError(t, ConnectTo(host, client))

	require.Eventually(t, func() bool {
		return host.IsConnected() && client.IsConnected()
	}, 5*time.Second, 50*time.Millisecond)

	waitDataChannelOpen(t, host.clipboardChannel())
	waitDataChannelOpen(t, client.clipboardChannel())

	enc, err := host.secure.Encrypt(channel.MsgTypeClipboard, []byte("replay"))
	require.NoError(t, err)

	require.NoError(t, host.SendRaw(enc))
	require.NoError(t, host.SendRaw(enc))

	count := atomic.Int32{}
	done := make(chan bool, 1)
	client.OnClipboard(func(_ []byte) {
		if count.Add(1) >= 2 {
			done <- true
		}
	})
	select {
	case <-done:
		t.Fatal("replay deveria ter sido rejeitado")
	case <-time.After(500 * time.Millisecond):
		require.Equal(t, int32(1), count.Load())
	}
}

func TestICEServersDefault(t *testing.T) {
	cfg := Config{DeviceID: "d", SessionID: "s", SharedKey: make([]byte, 32)}
	servers, err := cfg.ICEServers()
	require.NoError(t, err)
	require.NotEmpty(t, servers)
	urls := 0
	for _, s := range servers {
		urls += len(s.URLs)
	}
	require.GreaterOrEqual(t, urls, 2)
	require.Contains(t, servers[0].URLs, "stun:stun.cloudflare.com:3478")
}

func TestAddVideoTrackFailsWithNil(t *testing.T) {
	pm, err := NewPeerManager(Config{DeviceID: "dev", PeerID: "d", SessionID: "s", SharedKey: make([]byte, 32), ICEProvider: DefaultICEProvider{}, Initiator: true})
	require.NoError(t, err)
	defer pm.Close()
	require.Error(t, pm.AddVideoTrack(nil))
}

func TestRestartICE(t *testing.T) {
	key := make([]byte, 32)
	key[0] = 6
	host, err := NewPeerManager(Config{DeviceID: "dev-web", PeerID: "host", SessionID: "s6", SharedKey: key, ICEProvider: DefaultICEProvider{}, Initiator: true})
	require.NoError(t, err)
	defer host.Close()
	client, err := NewPeerManager(Config{DeviceID: "dev-web", PeerID: "client", SessionID: "s6", SharedKey: key, ICEProvider: DefaultICEProvider{}, Initiator: false})
	require.NoError(t, err)
	defer client.Close()
	require.NoError(t, ConnectTo(host, client))
	require.Eventually(t, func() bool { return host.IsConnected() && client.IsConnected() }, 5*time.Second, 50*time.Millisecond)
	require.NoError(t, host.RestartICE())
	require.NotNil(t, host.LocalDescription())
}

func TestShortLivedTURNProviderDisabled(t *testing.T) {
	p, err := NewShortLivedTURNProvider([]string{"turn:example.com"}, make([]byte, 32), "relay")
	require.NoError(t, err)
	_, _, _, _, err = p.ICECredentials(context.Background())
	require.Error(t, err)
}

func TestSecureChannelAADBinding(t *testing.T) {
	key := make([]byte, 32)
	key[0] = 7
	host, err := NewPeerManager(Config{DeviceID: "host", SessionID: "s7", SharedKey: key, ICEProvider: DefaultICEProvider{}})
	require.NoError(t, err)
	defer host.Close()

	// Trocar sessionID no secure channel simula canal errado.
	other, err := channel.NewSecureChannel(key, "host", "other", "webrtc")
	require.NoError(t, err)
	enc, err := other.Encrypt(channel.MsgTypeInput, []byte("x"))
	require.NoError(t, err)
	_, err = host.secure.Decrypt(enc)
	require.Error(t, err)
}

func TestGeometryJSON(t *testing.T) {
	b, err := json.Marshal(geometry.DisplayGeometry{Capture: geometry.Rect{0, 0, 1920, 1080}, Video: geometry.Rect{0, 0, 1280, 720}})
	require.NoError(t, err)
	require.Contains(t, string(b), "capture")
}

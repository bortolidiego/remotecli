package ipc

import (
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bortolidiego/relay/internal/geometry"
	"github.com/stretchr/testify/require"
)

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, WriteFrame(&buf, MsgTypeH264, []byte{0, 1, 2}))
	f, err := ReadFrame(&buf)
	require.NoError(t, err)
	require.Equal(t, MsgTypeH264, f.Type)
	require.Equal(t, []byte{0, 1, 2}, f.Payload)
}

func TestAuthResponse(t *testing.T) {
	secret := []byte("segredo")
	nonce := []byte("1234567890123456")
	resp := AuthResponse(secret, nonce)
	require.Len(t, resp, 32)
	require.Equal(t, resp, AuthResponse(secret, nonce))
	require.NotEqual(t, resp, AuthResponse([]byte("outro"), nonce))
}

func TestServerClientAuth(t *testing.T) {
	secret := []byte("segredo-compartilhado")
	srv := NewServer(secret)
	dir := "/tmp/relay-ipc-test-" + t.Name()
	_ = os.RemoveAll(dir)
	path, err := srv.Listen(dir)
	require.NoError(t, err)
	defer srv.Close()
	defer os.RemoveAll(dir)
	require.Equal(t, filepath.Join(dir, "relay-ipc", "relay.sock"), path)

	// simular client
	conn, err := net.Dial("unix", path)
	require.NoError(t, err)
	defer conn.Close()

	nonce := make([]byte, 16)
	_, err = conn.Read(nonce)
	require.NoError(t, err)
	require.NoError(t, WriteFrame(conn, MsgTypeAuth, AuthResponse(secret, nonce)))

	// enviar geometry
	g := geometry.DisplayGeometry{Capture: geometry.Rect{0, 0, 1920, 1080}, Video: geometry.Rect{0, 0, 1280, 720}}
	b, _ := json.Marshal(g)
	require.NoError(t, WriteFrame(conn, MsgTypeGeometry, b))

	recv := make(chan geometry.DisplayGeometry, 1)
	srv.OnGeometry(func(geom geometry.DisplayGeometry) { recv <- geom })
	select {
	case got := <-recv:
		require.InDelta(t, 1920, got.Capture.Width, 0.001)
	case <-time.After(2 * time.Second):
		t.Fatal("não recebeu geometria")
	}
}

func TestServerRejectsBadAuth(t *testing.T) {
	secret := []byte("segredo")
	srv := NewServer(secret)
	dir := "/tmp/relay-ipc-test-" + t.Name()
	_ = os.RemoveAll(dir)
	path, err := srv.Listen(dir)
	require.NoError(t, err)
	defer srv.Close()
	defer os.RemoveAll(dir)

	conn, err := net.Dial("unix", path)
	require.NoError(t, err)
	defer conn.Close()

	nonce := make([]byte, 16)
	_, err = conn.Read(nonce)
	require.NoError(t, err)
	require.NoError(t, WriteFrame(conn, MsgTypeAuth, AuthResponse([]byte("outro"), nonce)))

	// conexão deve ser fechada
	_, err = conn.Read(make([]byte, 1))
	require.Error(t, err)
}

func TestSecureSocketDir(t *testing.T) {
	dir, err := SecureSocketDir("/tmp/relay-secure-test")
	require.NoError(t, err)
	defer os.RemoveAll("/tmp/relay-secure-test")
	info, err := os.Stat(dir)
	require.NoError(t, err)
	require.True(t, info.IsDir())
	require.Equal(t, os.FileMode(0700), info.Mode().Perm())
}

func TestBroadcastInput(t *testing.T) {
	secret := []byte("segredo")
	srv := NewServer(secret)
	dir := "/tmp/relay-ipc-test-" + t.Name()
	_ = os.RemoveAll(dir)
	path, err := srv.Listen(dir)
	require.NoError(t, err)
	defer srv.Close()
	defer os.RemoveAll(dir)

	conn, err := net.Dial("unix", path)
	require.NoError(t, err)
	defer conn.Close()
	nonce := make([]byte, 16)
	_, err = conn.Read(nonce)
	require.NoError(t, err)
	require.NoError(t, WriteFrame(conn, MsgTypeAuth, AuthResponse(secret, nonce)))

	time.Sleep(50 * time.Millisecond)
	require.NoError(t, srv.BroadcastInput([]byte(`{"type":"click"}`)))

	frame, err := ReadFrame(conn)
	require.NoError(t, err)
	require.Equal(t, MsgTypeInput, frame.Type)
	require.JSONEq(t, `{"type":"click"}`, string(frame.Payload))
}

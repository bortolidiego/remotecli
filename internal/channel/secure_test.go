package channel

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEncryptDecrypt(t *testing.T) {
	ch, err := NewSecureChannel(make([]byte, 32), "dev", "sess", "chan")
	require.NoError(t, err)
	msg, err := ch.Encrypt(MsgTypeControl, []byte("hello"))
	require.NoError(t, err)
	require.Greater(t, len(msg), 4+HeaderSize)

	dec, err := ch.Decrypt(msg)
	require.NoError(t, err)
	require.Equal(t, MsgTypeControl, dec.Type)
	require.Equal(t, "hello", string(dec.Plaintext))
	require.Equal(t, uint32(1), dec.Sequence)
}

func TestReplayRejected(t *testing.T) {
	ch, err := NewSecureChannel(make([]byte, 32), "dev", "sess", "chan")
	require.NoError(t, err)
	msg, err := ch.Encrypt(MsgTypeControl, []byte("x"))
	require.NoError(t, err)
	_, err = ch.Decrypt(msg)
	require.NoError(t, err)
	_, err = ch.Decrypt(msg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "replay")
}

func TestDifferentKeysFail(t *testing.T) {
	ch1, err := NewSecureChannel(make([]byte, 32), "dev", "sess", "chan")
	require.NoError(t, err)
	key2 := make([]byte, 32)
	key2[31] = 1
	ch2, err := NewSecureChannel(key2, "dev", "sess", "chan")
	require.NoError(t, err)
	msg, err := ch1.Encrypt(MsgTypeControl, []byte("hello"))
	require.NoError(t, err)
	_, err = ch2.Decrypt(msg)
	require.Error(t, err)
}

func TestAADBinding(t *testing.T) {
	key := make([]byte, 32)
	ch1, err := NewSecureChannel(key, "dev", "sess", "chan")
	require.NoError(t, err)
	ch2, err := NewSecureChannel(key, "dev", "sess", "other")
	require.NoError(t, err)
	msg, err := ch1.Encrypt(MsgTypeControl, []byte("hello"))
	require.NoError(t, err)
	_, err = ch2.Decrypt(msg)
	require.Error(t, err)
}

func TestPlaintextRejected(t *testing.T) {
	ch, err := NewSecureChannel(make([]byte, 32), "dev", "sess", "chan")
	require.NoError(t, err)
	_, err = ch.Decrypt([]byte("plain text message"))
	require.Error(t, err)
}

func TestDifferentTypes(t *testing.T) {
	ch, err := NewSecureChannel(make([]byte, 32), "dev", "sess", "chan")
	require.NoError(t, err)
	for _, mt := range []MessageType{MsgTypeClipboard, MsgTypeFile, MsgTypeGeometry, MsgTypeInput} {
		msg, err := ch.Encrypt(mt, []byte{byte(mt)})
		require.NoError(t, err)
		dec, err := ch.Decrypt(msg)
		require.NoError(t, err)
		require.Equal(t, mt, dec.Type)
	}
}

func TestLargePayloadRejected(t *testing.T) {
	ch, err := NewSecureChannel(make([]byte, 32), "dev", "sess", "chan")
	require.NoError(t, err)
	_, err = ch.Encrypt(MsgTypeFile, bytes.Repeat([]byte{1}, 64*1024*1024+1))
	require.Error(t, err)
}

func TestSequencesIncrement(t *testing.T) {
	ch, err := NewSecureChannel(make([]byte, 32), "dev", "sess", "chan")
	require.NoError(t, err)
	var last uint32
	for i := 0; i < 5; i++ {
		msg, err := ch.Encrypt(MsgTypeControl, []byte("x"))
		require.NoError(t, err)
		dec, err := ch.Decrypt(msg)
		require.NoError(t, err)
		require.Greater(t, dec.Sequence, last)
		last = dec.Sequence
	}
}

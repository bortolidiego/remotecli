package keychain

import (
	"testing"

	"github.com/bortolidiego/relay/internal/crypto"
	"github.com/stretchr/testify/require"
)

func TestFakeStoreRoundTrip(t *testing.T) {
	store := NewFakeStore()
	id, err := crypto.GenerateIdentity()
	require.NoError(t, err)

	require.NoError(t, store.SaveIdentity("test", "acc1", id.SigningKey()))
	loaded, err := store.LoadIdentity("test", "acc1")
	require.NoError(t, err)
	require.Equal(t, id.SigningKey().X, loaded.X)

	ecdhPEM, err := crypto.EncodeECDHKeyToPEM(id.ECDHKey())
	require.NoError(t, err)
	require.NoError(t, store.SaveSecret("test-ecdh", "acc1", ecdhPEM))
	loadedECDH, err := store.LoadECDH("test-ecdh", "acc1")
	require.NoError(t, err)
	require.Equal(t, id.ECDHKey().PublicKey().Bytes(), loadedECDH.PublicKey().Bytes())

	require.NoError(t, store.DeleteIdentity("test", "acc1"))
	_, err = store.LoadIdentity("test", "acc1")
	require.Error(t, err)
}

func TestFakeStoreNilKey(t *testing.T) {
	store := NewFakeStore()
	require.Error(t, store.SaveIdentity("test", "acc1", nil))
}

func TestFakeStoreRegistryRoundTrip(t *testing.T) {
	store := NewFakeStore()
	data := []byte(`{"x":{"device_id":"x","name":"X"}}`)
	require.NoError(t, store.SaveRegistry("devices", "host-s1", data))
	loaded, err := store.LoadRegistry("devices", "host-s1")
	require.NoError(t, err)
	require.Equal(t, data, loaded)
}

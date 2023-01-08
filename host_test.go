package net

import (
	"context"
	"path"
	"testing"

	logging "github.com/ipfs/go-log"
	"github.com/stretchr/testify/require"
)

const testMaxMessageSize = 1 << 17

func init() {
	_ = logging.SetLogLevel("net", "debug")
}

func basicTestConfig(t *testing.T) *Config {
	// t.TempDir() is unique on every call. Don't reuse this config with multiple hosts.
	tmpDir := t.TempDir()
	return &Config{
		Ctx:       context.Background(),
		DataDir:   tmpDir,
		Port:      0, // OS randomized libp2p port
		KeyFile:   path.Join(tmpDir, "node.key"),
		Bootnodes: nil,
		ListenIP:  "127.0.0.1",
	}
}

func newHost(t *testing.T, cfg *Config) *Host {
	h, err := NewHost(cfg)
	require.NoError(t, err)
	t.Cleanup(func() {
		err = h.Stop()
		require.NoError(t, err)
	})
	return h
}

func TestNewHost(t *testing.T) {
	cfg := basicTestConfig(t)
	h := newHost(t, cfg)
	err := h.Start()

	addresses := h.Addresses()
	require.NotEmpty(t, addresses)
	for _, addr := range h.Addresses() {
		t.Logf(addr)
	}

	require.NoError(t, err)
}

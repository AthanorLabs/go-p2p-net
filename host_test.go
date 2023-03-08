package p2pnet

import (
	"context"
	"path"
	"testing"
	"time"

	logging "github.com/ipfs/go-log"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/require"
)

const testMaxMessageSize = 1 << 17

func init() {
	_ = logging.SetLogLevel("p2pnet", "debug")
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
	h := newHost(t, basicTestConfig(t))
	err := h.Start()
	require.NoError(t, err)

	addresses := h.Addresses()
	require.NotEmpty(t, addresses)
	for _, addr := range h.Addresses() {
		t.Log(addr)
	}
}

func TestAdvertiseDiscover(t *testing.T) {
	h1 := newHost(t, basicTestConfig(t))
	err := h1.Start()
	require.NoError(t, err)

	h1Addresses := h1.Addresses()
	require.NotEmpty(t, h1Addresses)

	cfgH2 := basicTestConfig(t)
	cfgH2.Bootnodes = []string{h1Addresses[0].String()}

	h2 := newHost(t, cfgH2)
	err = h2.Start()
	require.NoError(t, err)

	nameSpaces := []string{"", "one", "two", "three"}
	advertisedNamespaces := func() []string {
		return nameSpaces
	}
	h1.SetAdvertisedNamespacesFunc(advertisedNamespaces)

	// RefreshNamespaces only puts the namespaces to advertise into a channel. It
	// doesn't block until the advertisements are actually sent.
	time.Sleep(500 * time.Millisecond)

	for _, ns := range nameSpaces {
		peerIDs, err := h2.Discover(ns, time.Second*3)
		require.NoError(t, err)
		require.Len(t, peerIDs, 1)
		require.Equal(t, h1.PeerID(), peerIDs[0])
	}
}

func TestHost_ConnectToSelf(t *testing.T) {
	h := newHost(t, basicTestConfig(t))
	err := h.Start()
	require.NoError(t, err)

	err = h.Connect(context.Background(), peer.AddrInfo{ID: h.PeerID()})
	require.ErrorIs(t, err, errCannotConnectToSelf)
}

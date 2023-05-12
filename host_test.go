package p2pnet

import (
	"context"
	"path"
	"testing"
	"time"

	"github.com/fortytw2/leaktest"
	logging "github.com/ipfs/go-log"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/require"
)

const testMaxMessageSize = 1 << 17

func init() {
	_ = logging.SetLogLevel("p2pnet", "debug")
}

func basicTestConfig(t *testing.T, namespaces []string) *Config {
	// t.TempDir() is unique on every call. Don't reuse this config with multiple hosts.
	tmpDir := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	return &Config{
		Ctx:       ctx,
		DataDir:   tmpDir,
		Port:      0, // OS randomized libp2p port
		KeyFile:   path.Join(tmpDir, "node.key"),
		Bootnodes: nil,
		ListenIP:  "127.0.0.1",
		AdvertisedNamespacesFunc: func() []string {
			return namespaces
		},
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
	t.Cleanup(leaktest.Check(t))
	h := newHost(t, basicTestConfig(t, []string{""}))
	err := h.Start()
	require.NoError(t, err)

	addresses := h.Addresses()
	require.NotEmpty(t, addresses)
	for _, addr := range h.Addresses() {
		t.Log(addr)
	}
}

func TestAdvertiseDiscover(t *testing.T) {
	nameSpaces := []string{"", "one", "two", "three"}

	h1 := newHost(t, basicTestConfig(t, nameSpaces))
	err := h1.Start()
	require.NoError(t, err)

	h1Addresses := h1.Addresses()
	require.NotEmpty(t, h1Addresses)

	cfgH2 := basicTestConfig(t, nameSpaces)
	cfgH2.Bootnodes = []string{h1Addresses[0].String()}

	h2 := newHost(t, cfgH2)
	err = h2.Start()
	require.NoError(t, err)

	// h1's first advertisement attempt failed, as h2 was not yet online to
	// form a DHT with. Run h1's advertisement loop now instead of waiting.
	h1.Advertise()
	time.Sleep(testAdvertPropagationDelay)

	for _, ns := range nameSpaces {
		peerIDs, err := h2.Discover(ns, time.Second*3)
		require.NoError(t, err, "namespace=%q", ns)
		require.Len(t, peerIDs, 1, "namespace=%q", ns)
		require.Equal(t, h1.PeerID(), peerIDs[0], "namespace=%q", ns)
	}
}

func TestHost_ConnectToSelf(t *testing.T) {
	h := newHost(t, basicTestConfig(t, nil))
	err := h.Start()
	require.NoError(t, err)

	err = h.Connect(context.Background(), peer.AddrInfo{ID: h.PeerID()})
	require.ErrorIs(t, err, errCannotConnectToSelf)
}

// Package p2pnet implements p2p functionality for nodes using libp2p.
package p2pnet

import (
	"context"
	"fmt"
	"net"
	"os"
	"path"
	"sync"
	"sync/atomic"
	"time"

	badger "github.com/ipfs/go-ds-badger2"
	logging "github.com/ipfs/go-log"
	"github.com/libp2p/go-libp2p"
	kaddht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p-kad-dht/dual"
	libp2phost "github.com/libp2p/go-libp2p/core/host"
	libp2pnetwork "github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/protocol"
	libp2pdiscovery "github.com/libp2p/go-libp2p/p2p/discovery/routing"
	"github.com/libp2p/go-libp2p/p2p/host/peerstore/pstoreds"
	routedhost "github.com/libp2p/go-libp2p/p2p/host/routed"
	ma "github.com/multiformats/go-multiaddr"
)

var log = logging.Logger("p2pnet")

// Host represents a generic peer-to-peer node (ie. a host) that supports
// discovery via DHT.
type Host struct {
	ctx        context.Context
	cancel     context.CancelFunc
	protocolID string

	h         libp2phost.Host
	bootnodes []peer.AddrInfo
	discovery *discovery
	ds        *badger.Datastore
}

// Config is used to configure the network Host.
type Config struct {
	Ctx        context.Context
	DataDir    string
	Port       uint16
	KeyFile    string
	Bootnodes  []string
	ProtocolID string
	ListenIP   string
}

// QUIC will have better performance in high-bandwidth protocols if you increase a socket
// receive buffer (sysctl -w net.core.rmem_max=2500000). We have a low-bandwidth protocol,
// so setting this variable keeps a warning out of our logs. See this for more information:
// https://github.com/lucas-clemente/quic-go/wiki/UDP-Receive-Buffer-Size
func init() {
	_ = os.Setenv("QUIC_GO_DISABLE_RECEIVE_BUFFER_WARNING", "true")
}

// NewHost returns a new Host
func NewHost(cfg *Config) (*Host, error) {
	if cfg.DataDir == "" || cfg.KeyFile == "" {
		panic("required parameters not set")
	}

	key, err := loadKey(cfg.KeyFile)
	if err != nil {
		log.Debugf("failed to load libp2p key, generating key %s...", cfg.KeyFile)
		key, err = generateKey(0, cfg.KeyFile)
		if err != nil {
			return nil, err
		}
	}

	listenIP := net.ParseIP(cfg.ListenIP)
	if listenIP == nil {
		return nil, errInvalidListenIP
	}

	ds, err := badger.NewDatastore(path.Join(cfg.DataDir, "libp2p-datastore"), &badger.DefaultOptions)
	if err != nil {
		return nil, err
	}

	ps, err := pstoreds.NewPeerstore(cfg.Ctx, ds, pstoreds.DefaultOpts())
	if err != nil {
		return nil, err
	}

	// set libp2p host options
	opts := []libp2p.Option{
		libp2p.ListenAddrStrings(
			fmt.Sprintf("/ip4/%s/tcp/%d", listenIP.String(), cfg.Port),
			fmt.Sprintf("/ip4/%s/udp/%d/quic-v1", listenIP.String(), cfg.Port),
		),
		libp2p.Identity(key),
		libp2p.NATPortMap(),
		libp2p.EnableRelayService(),
		libp2p.EnableNATService(),
		libp2p.EnableHolePunching(),
		libp2p.Peerstore(ps),
	}

	// format bootnodes
	bns, err := stringsToAddrInfos(cfg.Bootnodes)
	if err != nil {
		return nil, fmt.Errorf("failed to format bootnodes: %w", err)
	}

	if len(bns) > 0 {
		opts = append(opts, libp2p.EnableAutoRelayWithStaticRelays(bns))
	}

	// create libp2p host instance
	basicHost, err := libp2p.New(opts...)
	if err != nil {
		return nil, err
	}

	// There is libp2p bug when calling `dual.New` with a cancelled context creating a panic,
	// so we need the extra guard below:
	// Panic:  https://github.com/jbenet/goprocess/blob/v0.1.4/impl-mutex.go#L99
	// Caller: https://github.com/libp2p/go-libp2p-kad-dht/blob/v0.17.0/dht.go#L222
	if cfg.Ctx.Err() != nil {
		return nil, err
	}

	// Note on ModeServer: The dual KAD DHT, by default, puts the LAN interface in server mode and
	// the WAN interface in ModeClient if it is behind a NAT firewall. In our case, even nodes behind
	// NAT firewalls should be servers, otherwise remote nodes will not be able to connect and list
	// their offers.
	dht, err := dual.New(cfg.Ctx, basicHost,
		dual.DHTOption(kaddht.BootstrapPeers(bns...)),
		dual.DHTOption(kaddht.Mode(kaddht.ModeServer)),
	)
	if err != nil {
		return nil, err
	}

	routedHost := routedhost.Wrap(basicHost, dht)

	ourCtx, cancel := context.WithCancel(cfg.Ctx)
	hst := &Host{
		ctx:        ourCtx,
		cancel:     cancel,
		protocolID: cfg.ProtocolID,
		h:          routedHost,
		ds:         ds,
		bootnodes:  bns,
		discovery: &discovery{
			ctx:         ourCtx,
			dht:         dht,
			h:           routedHost,
			rd:          libp2pdiscovery.NewRoutingDiscovery(dht),
			advertiseCh: make(chan struct{}),
		},
	}

	return hst, nil
}

// Start starts the bootstrap and discovery process.
func (h *Host) Start() error {
	for _, addr := range h.h.Addrs() {
		log.Info("started listening: address=", addr)
	}

	// ignore error - node should still be able to run without connecting to
	// bootstrap nodes (for now)
	if err := h.bootstrap(); err != nil {
		return err
	}

	go h.logPeers()

	return h.discovery.start()
}

func (h *Host) logPeers() {
	logPeersInterval := time.Minute * 5
	timer := time.NewTicker(logPeersInterval)

	for {
		log.Debugf("peer count: %d", len(h.h.Network().Peers()))

		select {
		case <-h.ctx.Done():
			return
		case <-timer.C:
		}
	}
}

// Stop closes host services and the libp2p host (host services first)
func (h *Host) Stop() error {
	h.cancel()

	if err := h.discovery.stop(); err != nil {
		return err
	}

	if err := h.h.Close(); err != nil {
		return fmt.Errorf("failed to close libp2p host: %w", err)
	}

	err := h.h.Peerstore().Close()
	if err != nil {
		return fmt.Errorf("failed to close peerstore: %w", err)
	}

	err = h.ds.Close()
	if err != nil {
		return fmt.Errorf("failed to close libp2p datastore: %w", err)
	}

	return nil
}

// RefreshNamespaces advertises in the DHT.
func (h *Host) RefreshNamespaces() {
	h.discovery.advertiseCh <- struct{}{}
}

// Addresses returns the list of multiaddress the host is listening on.
func (h *Host) Addresses() []ma.Multiaddr {
	return h.multiaddrs()
}

// PeerID returns the host's peer ID.
func (h *Host) PeerID() peer.ID {
	return h.h.ID()
}

// AddrInfo returns the host's AddrInfo.
func (h *Host) AddrInfo() peer.AddrInfo {
	return peer.AddrInfo{
		ID:    h.h.ID(),
		Addrs: h.h.Addrs(),
	}
}

// ConnectedPeers returns the multiaddresses of our currently connected peers.
func (h *Host) ConnectedPeers() []string {
	var peers []string
	for _, c := range h.h.Network().Conns() {
		// the remote multi addr returned is just the transport
		p := fmt.Sprintf("%s/p2p/%s", c.RemoteMultiaddr(), c.RemotePeer())
		peers = append(peers, p)
	}
	return peers
}

// Discover searches the DHT for peers that advertise that they provide the given string..
// It searches for up to `searchTime` duration of time.
func (h *Host) Discover(provides string, searchTime time.Duration) ([]peer.ID, error) {
	return h.discovery.discover(provides, searchTime)
}

// SetStreamHandler sets the stream handler for the given protocol ID.
func (h *Host) SetStreamHandler(pid string, handler func(libp2pnetwork.Stream)) {
	h.h.SetStreamHandler(protocol.ID(h.protocolID+pid), handler)
	log.Debugf("supporting protocol %s", protocol.ID(h.protocolID+pid))
}

// SetAdvertisedNamespacesFunc sets the function that is called to determine
// which namespaces should be advertised in the DHT. In most use cases, the
// passed function should, at minimum, return the empty ("") namespace.
func (h *Host) SetAdvertisedNamespacesFunc(fn func() []string) {
	h.discovery.setAdvertisedNamespacesFunc(fn)
}

// Connectedness returns the connectedness state of a given peer.
func (h *Host) Connectedness(who peer.ID) libp2pnetwork.Connectedness {
	return h.h.Network().Connectedness(who)
}

// Connect connects to the given peer.
func (h *Host) Connect(ctx context.Context, who peer.AddrInfo) error {
	if who.ID == h.PeerID() {
		return errCannotConnectToSelf
	}
	return h.h.Connect(ctx, who)
}

// NewStream opens a stream with the given peer on the given protocol ID.
func (h *Host) NewStream(ctx context.Context, p peer.ID, pid protocol.ID) (libp2pnetwork.Stream, error) {
	return h.h.NewStream(ctx, p, protocol.ID(h.protocolID)+pid)
}

// multiaddrs returns the local multiaddresses that we are listening on
func (h *Host) multiaddrs() []ma.Multiaddr {
	addr := h.AddrInfo()
	multiaddrs, err := peer.AddrInfoToP2pAddrs(&addr)
	if err != nil {
		// This shouldn't ever happen, but don't want to panic
		log.Errorf("failed to convert AddrInfo=%q to Multiaddr: %s", addr, err)
	}
	return multiaddrs
}

// bootstrap connects the host to the configured bootnodes
func (h *Host) bootstrap() error {
	if len(h.bootnodes) == 0 {
		log.Warnf("bootstrapping skipped, no bootnodes found")
		return nil
	}

	selfID := h.PeerID()

	failed := uint64(0)
	var wg sync.WaitGroup
	for _, bn := range h.bootnodes {
		if bn.ID == selfID {
			continue
		}
		h.h.Peerstore().AddAddrs(bn.ID, bn.Addrs, peerstore.PermanentAddrTTL)
		log.Debugf("bootstrapping to peer: %s (%s)", bn, h.h.Network().Connectedness(bn.ID))
		wg.Add(1)
		go func(p peer.AddrInfo) {
			defer wg.Done()
			err := h.h.Connect(h.ctx, p)
			if err != nil {
				log.Debugf("failed to bootstrap to peer %s: err=%s", p.ID, err)
				atomic.AddUint64(&failed, 1)
			}
			for _, c := range h.h.Network().ConnsToPeer(p.ID) {
				log.Debugf("connected to %s/p2p/%s", c.RemoteMultiaddr(), p.ID)
			}
		}(bn)
	}
	wg.Wait()

	if failed == uint64(len(h.bootnodes)) {
		return errFailedToBootstrap
	}

	return nil
}

package p2pnet

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p-kad-dht/dual"
	libp2pdiscovery "github.com/libp2p/go-libp2p/core/discovery"
	libp2phost "github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	libp2prouting "github.com/libp2p/go-libp2p/p2p/discovery/routing"
)

const (
	tryAdvertiseTimeout = time.Second * 30
	defaultAdvertiseTTL = time.Minute * 5
	defaultMinPeers     = 3  // TODO: make this configurable
	defaultMaxPeers     = 50 // TODO: make this configurable
)

// ShouldAdvertiseFunc is the type for a function that returns whether we should
// regularly advertise inside the advertisement loop.
// If it returns false, we don't advertise until the next loop iteration.
type ShouldAdvertiseFunc = func() bool

type discovery struct {
	ctx                 context.Context
	dht                 *dual.DHT
	h                   libp2phost.Host
	rd                  *libp2prouting.RoutingDiscovery
	advertiseCh         chan []string // signals to advertise
	shouldAdvertiseFunc ShouldAdvertiseFunc
}

func (d *discovery) setShouldAdvertiseFunc(fn ShouldAdvertiseFunc) {
	d.shouldAdvertiseFunc = fn
}

func (d *discovery) start() error {
	err := d.dht.Bootstrap(d.ctx)
	if err != nil {
		return fmt.Errorf("failed to bootstrap DHT: %w", err)
	}

	// wait to connect to bootstrap peers
	time.Sleep(time.Second)
	go d.advertiseLoop()
	go d.discoverLoop()

	log.Debug("discovery started!")
	return nil
}

func (d *discovery) stop() error {
	return d.dht.Close()
}

func (d *discovery) advertiseLoop() {
	var toAdvertise []string
	ttl := d.advertise(toAdvertise)

	for {
		select {
		case ta := <-d.advertiseCh:
			toAdvertise = ta
			ttl = d.advertise(toAdvertise)
		case <-time.After(ttl):
			// the DHT clears provider records (ie. who is advertising what content)
			// every 24 hours.
			// so, if we don't have any offers available for 24 hours, then we are
			// no longer present in the DHT as a provider.
			// otherwise, we'll be present, but no offers will be sent when peers
			// query us.
			//
			// this function is set in net/swapnet/host.go SetHandler().
			if d.shouldAdvertiseFunc != nil && !d.shouldAdvertiseFunc() {
				continue
			}

			ttl = d.advertise(toAdvertise)
		case <-d.ctx.Done():
			return
		}
	}
}

// advertise advertises that we provide XMR in the DHT.
// note: we only advertise that we are an XMR provider, but we don't
// advertise our specific offers.
// to find what our offers are, peers need to send us a QueryRequest
// over the query subprotocol.
// the return value is the amount of time the caller should wait before
// trying to advertise again.
func (d *discovery) advertise(toAdvertise []string) time.Duration {
	log.Debug("advertising in the DHT...")
	err := d.dht.Bootstrap(d.ctx)
	if err != nil {
		log.Warnf("failed to bootstrap DHT: err=%s", err)
		return tryAdvertiseTimeout
	}

	_, err = d.rd.Advertise(d.ctx, "")
	if err != nil {
		log.Debugf("failed to advertise in the DHT: err=%s", err)
		return tryAdvertiseTimeout
	}

	for _, provides := range toAdvertise {
		_, err = d.rd.Advertise(d.ctx, provides)
		if err != nil {
			log.Debugf("failed to advertise in the DHT: err=%s", err)
			return tryAdvertiseTimeout
		}
	}

	return defaultAdvertiseTTL
}

func (d *discovery) discoverLoop() {
	const discoverLoopDuration = time.Minute
	timer := time.NewTicker(discoverLoopDuration)

	for {
		select {
		case <-d.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			if len(d.h.Network().Peers()) >= defaultMinPeers {
				continue
			}

			// if our peer count is low, try to find some peers
			_, err := d.findPeers("", discoverLoopDuration)
			if err != nil {
				log.Errorf("failed to find peers: %s", err)
			}
		}
	}
}

func (d *discovery) findPeers(provides string, timeout time.Duration) ([]peer.ID, error) {
	peerCh, err := d.rd.FindPeers(
		d.ctx,
		provides,
		libp2pdiscovery.Limit(defaultMaxPeers),
	)
	if err != nil {
		return nil, err
	}

	ourPeerID := d.h.ID()
	var peerIDs []peer.ID

	ctx, cancel := context.WithTimeout(d.ctx, timeout)
	defer cancel()

	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return peerIDs, nil
			}
			return peerIDs, ctx.Err()
		case peer, ok := <-peerCh:
			if !ok {
				// channel was closed, no more peers to read
				return peerIDs, nil
			}
			if peer.ID == ourPeerID {
				continue
			}

			log.Debugf("found new peer via DHT: %s", peer)
			peerIDs = append(peerIDs, peer.ID)

			// found a peer, try to connect if we need more peers
			if len(d.h.Network().Peers()) < defaultMaxPeers {
				err = d.h.Connect(d.ctx, peer)
				if err != nil {
					log.Debugf("failed to connect to discovered peer %s: %s", peer.ID, err)
				}
			} else {
				d.h.Peerstore().AddAddrs(peer.ID, peer.Addrs, peerstore.PermanentAddrTTL)
			}
		}
	}
}

func (d *discovery) discover(
	provides string,
	searchTime time.Duration,
) ([]peer.ID, error) {
	log.Debugf("attempting to find DHT peers that provide [%s] for %vs",
		provides,
		searchTime.Seconds(),
	)

	return d.findPeers(provides, searchTime)
}

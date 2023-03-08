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

type discovery struct {
	ctx                  context.Context
	dht                  *dual.DHT
	h                    libp2phost.Host
	rd                   *libp2prouting.RoutingDiscovery
	advertiseCh          chan struct{} // signals to advertise
	advertisedNamespaces func() []string
}

// setAdvertisedNamespacesFunc sets the function used to query the list of
// namespaces to be advertised on every cycle of the advertisement loop. In most
// use cases, this function should always return the empty namespace ("") on top
// of any additional namespaces that should be advertised.
func (d *discovery) setAdvertisedNamespacesFunc(fn func() []string) {
	d.advertisedNamespaces = fn
}

func (d *discovery) getAdvertisedNamespaces() []string {
	if d.advertisedNamespaces == nil {
		return []string{""}
	}
	return d.advertisedNamespaces()
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
	ttl := time.Duration(0) // don't block on first loop iteration

	for {
		select {
		case <-d.ctx.Done():
			return
		case <-d.advertiseCh:
			// we've been asked to publish advertisements immediately, presumably
			// because a new namespace was added
		case <-time.After(ttl):
			// publish advertisements on regular interval
		}

		// The DHT clears provider records (ie. who is advertising what content)
		// every 24 hours. So, if we don't advertise a namespace for 24 hours,
		// then we are no longer present in the DHT under that namespace.
		ttl = d.advertise(d.getAdvertisedNamespaces())
	}
}

// advertise advertises the passed set of namespaces in the DHT.
func (d *discovery) advertise(namespaces []string) time.Duration {
	err := d.dht.Bootstrap(d.ctx)
	if err != nil {
		log.Warnf("failed to bootstrap DHT: %s", err)
		return tryAdvertiseTimeout
	}

	for _, provides := range namespaces {
		_, err = d.rd.Advertise(d.ctx, provides)
		if err != nil {
			log.Debugf("failed to advertise %q in the DHT: %s", provides, err)
			return tryAdvertiseTimeout
		}
		log.Debugf("advertised %q in the DHT", provides)
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
		case peerAddr, ok := <-peerCh:
			if !ok {
				// channel was closed, no more peers to read
				return peerIDs, nil
			}
			if peerAddr.ID == ourPeerID {
				continue
			}

			log.Debugf("found new peer via DHT: %s", peerAddr)
			peerIDs = append(peerIDs, peerAddr.ID)

			// found a peer, try to connect if we need more peers
			if len(d.h.Network().Peers()) < defaultMaxPeers {
				err = d.h.Connect(d.ctx, peerAddr)
				if err != nil {
					log.Debugf("failed to connect to discovered peer %s: %s", peerAddr.ID, err)
				}
			} else {
				d.h.Peerstore().AddAddrs(peerAddr.ID, peerAddr.Addrs, peerstore.PermanentAddrTTL)
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

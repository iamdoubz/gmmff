//go:build !js

package localmode

import (
	"context"
	"fmt"
	"time"

	"github.com/grandcat/zeroconf"
)

const (
	mdnsService  = "_gmmff._tcp"
	mdnsDomain   = "local."
	mdnsInstance = "gmmff"
)

// MDNSServer holds a running mDNS service registration.
type MDNSServer struct {
	server *zeroconf.Server
}

// RegisterMDNS advertises this gmmff instance on the local network via mDNS.
// Runs with a 3-second timeout — if mDNS setup fails or hangs (e.g. IPv6
// disabled at OS level) it returns an error without blocking startup.
func RegisterMDNS(port int, scheme string) (*MDNSServer, error) {
	type result struct {
		srv *zeroconf.Server
		err error
	}
	ch := make(chan result, 1)

	go func() {
		txt := []string{
			"version=1",
			fmt.Sprintf("scheme=%s", scheme),
		}
		srv, err := zeroconf.Register(mdnsInstance, mdnsService, mdnsDomain, port, txt, nil)
		ch <- result{srv, err}
	}()

	select {
	case r := <-ch:
		if r.err != nil {
			return nil, fmt.Errorf("local: mDNS register: %w", r.err)
		}
		return &MDNSServer{server: r.srv}, nil
	case <-time.After(3 * time.Second):
		return nil, fmt.Errorf("local: mDNS register timed out (IPv6 may be disabled)")
	}
}

// Shutdown deregisters this instance from mDNS.
func (m *MDNSServer) Shutdown() {
	if m != nil && m.server != nil {
		m.server.Shutdown()
	}
}

// PeerInfo holds information about a discovered gmmff peer.
type PeerInfo struct {
	Addr   string // e.g. "192.168.1.42:8787"
	Scheme string // "http" or "https"
}

// DiscoverPeers scans the local network for other gmmff instances.
// Runs with a hard timeout so IPv6 failures or slow networks never block.
func DiscoverPeers(ctx context.Context, scanDuration time.Duration) ([]PeerInfo, error) {
	type result struct {
		peers []PeerInfo
		err   error
	}
	ch := make(chan result, 1)

	go func() {
		peers, err := discoverPeers(ctx, scanDuration)
		ch <- result{peers, err}
	}()

	select {
	case r := <-ch:
		return r.peers, r.err
	case <-time.After(scanDuration + 500*time.Millisecond):
		return nil, nil // timeout — treat as no peers found
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func discoverPeers(ctx context.Context, scanDuration time.Duration) ([]PeerInfo, error) {
	entries := make(chan *zeroconf.ServiceEntry, 16)
	var peers []PeerInfo

	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		return nil, fmt.Errorf("local: mDNS resolver: %w", err)
	}

	scanCtx, cancel := context.WithTimeout(ctx, scanDuration)
	defer cancel()

	if err := resolver.Browse(scanCtx, mdnsService, mdnsDomain, entries); err != nil {
		return nil, fmt.Errorf("local: mDNS browse: %w", err)
	}

	for {
		select {
		case entry, ok := <-entries:
			if !ok {
				return peers, nil
			}
			if entry.Instance == mdnsInstance {
				continue
			}
			scheme := "http"
			for _, txt := range entry.Text {
				if txt == "scheme=https" {
					scheme = "https"
				}
			}
			addr := ""
			if len(entry.AddrIPv4) > 0 {
				addr = fmt.Sprintf("%s:%d", entry.AddrIPv4[0], entry.Port)
			} else if len(entry.AddrIPv6) > 0 {
				addr = fmt.Sprintf("[%s]:%d", entry.AddrIPv6[0], entry.Port)
			} else if entry.HostName != "" {
				addr = fmt.Sprintf("%s:%d", entry.HostName, entry.Port)
			}
			if addr != "" {
				peers = append(peers, PeerInfo{Addr: addr, Scheme: scheme})
			}
		case <-scanCtx.Done():
			return peers, nil
		}
	}
}

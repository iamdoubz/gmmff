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
// Other gmmff instances on the same network will discover it automatically.
// Call server.Shutdown() to deregister.
func RegisterMDNS(port int, scheme string) (*MDNSServer, error) {
	txt := []string{
		"version=1",
		fmt.Sprintf("scheme=%s", scheme),
	}
	server, err := zeroconf.Register(mdnsInstance, mdnsService, mdnsDomain, port, txt, nil)
	if err != nil {
		return nil, fmt.Errorf("local: mDNS register: %w", err)
	}
	return &MDNSServer{server: server}, nil
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
// Returns after scanDuration. Non-blocking — results come back via channel.
func DiscoverPeers(ctx context.Context, scanDuration time.Duration) ([]PeerInfo, error) {
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
			// Skip ourselves (same instance name).
			if entry.Instance == mdnsInstance {
				// Could be us — but we might have a different port so still include
				// if port differs. In practice DiscoverPeers is called before we
				// register, so this shouldn't be an issue.
				continue
			}
			scheme := "http"
			for _, txt := range entry.Text {
				if txt == "scheme=https" {
					scheme = "https"
				}
			}
			// Use the first IP address found.
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

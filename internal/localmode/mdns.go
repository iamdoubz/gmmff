//go:build !js

package localmode

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/betamos/zeroconf"
)

const (
	mdnsService  = "_gmmff._tcp"
	mdnsInstance = "gmmff"
)

// MDNSServer holds a running mDNS service registration.
type MDNSServer struct {
	client *zeroconf.Client
}

// RegisterMDNS advertises this gmmff instance on the local network via mDNS.
// Runs with a 3-second timeout — if mDNS setup fails or hangs (e.g. IPv6
// disabled at OS level) it returns an error without blocking startup.
func RegisterMDNS(port int, scheme string) (*MDNSServer, error) {
	select {
	case srv := <-RegisterMDNSAsync(port, scheme):
		if srv == nil {
			return nil, fmt.Errorf("local: mDNS register failed")
		}
		return srv, nil
	case <-time.After(3 * time.Second):
		return nil, fmt.Errorf("local: mDNS register timed out")
	}
}

// RegisterMDNSAsync starts mDNS registration in a goroutine and returns a
// channel that receives the result. Sends nil on failure.
func RegisterMDNSAsync(port int, scheme string) <-chan *MDNSServer {
	ch := make(chan *MDNSServer, 1)
	go func() {
		ty  := zeroconf.NewType(mdnsService)
		svc := zeroconf.NewService(ty, mdnsInstance, uint16(port))
		svc.Text = []string{"version=1", fmt.Sprintf("scheme=%s", scheme)}

		client, err := zeroconf.New().Publish(svc).Open()
		if err != nil {
			ch <- nil
			return
		}
		ch <- &MDNSServer{client: client}
	}()
	return ch
}

// Shutdown deregisters this instance from mDNS.
func (m *MDNSServer) Shutdown() {
	if m != nil && m.client != nil {
		m.client.Close()
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
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func discoverPeers(ctx context.Context, scanDuration time.Duration) ([]PeerInfo, error) {
	scanCtx, cancel := context.WithTimeout(ctx, scanDuration)
	defer cancel()

	var peers []PeerInfo
	ty := zeroconf.NewType(mdnsService)

	client, err := zeroconf.New().Browse(func(e zeroconf.Event) {
		if e.Op == zeroconf.OpRemoved {
			return
		}
		if e.Name == mdnsInstance {
			return
		}
		scheme := "http"
		for _, txt := range e.Text {
			if txt == "scheme=https" {
				scheme = "https"
			}
		}
		// Prefer IPv4, fall back to IPv6, then hostname.
		addr := ""
		for _, a := range e.Addrs {
			ip := net.IP(a.AsSlice())
			if ip.To4() != nil {
				addr = fmt.Sprintf("%s:%d", ip.String(), e.Port)
				break
			}
		}
		if addr == "" && len(e.Addrs) > 0 {
			a := e.Addrs[0]
			if a.Is6() {
				addr = fmt.Sprintf("[%s]:%d", a.String(), e.Port)
			} else {
				addr = fmt.Sprintf("%s:%d", a.String(), e.Port)
			}
		}
		if addr == "" && e.Hostname != "" {
			addr = fmt.Sprintf("%s:%d", e.Hostname, e.Port)
		}
		if addr != "" {
			peers = append(peers, PeerInfo{Addr: addr, Scheme: scheme})
		}
	}, ty).Open()

	if err != nil {
		return nil, fmt.Errorf("local: mDNS browse: %w", err)
	}
	defer client.Close()

	<-scanCtx.Done()
	return peers, nil
}

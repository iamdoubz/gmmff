//go:build !js

package localmode

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/betamos/zeroconf"
)

const (
	mdnsService  = "_gmmff._tcp"
	mdnsInstance = "gmmff"
)

// ipSupport describes which IP families are available on this host.
type ipSupport struct {
	v4 bool
	v6 bool
}

// String returns a human-readable description for logging.
func (s ipSupport) String() string {
	switch {
	case s.v4 && s.v6:
		return "dual-stack (IPv4 + IPv6)"
	case s.v4:
		return "IPv4 only"
	case s.v6:
		return "IPv6 only"
	default:
		return "none"
	}
}

// network returns the betamos/zeroconf network string for this support level.
func (s ipSupport) network() string {
	switch {
	case s.v4 && s.v6:
		return "udp"
	case s.v4:
		return "udp4"
	case s.v6:
		return "udp6"
	default:
		return "udp"
	}
}

// detectIPSupport probes which IP families are usable for multicast on this
// host by attempting to open a UDP socket of each type. This is cheaper and
// more accurate than parsing /proc or checking kernel flags — if you can open
// the socket, the family works; if not (EAFNOSUPPORT, ENETUNREACH, etc.) it
// doesn't.
func detectIPSupport() ipSupport {
	var s ipSupport

	// Try to bind a UDP4 socket on the mDNS multicast address.
	if c, err := net.ListenPacket("udp4", "0.0.0.0:0"); err == nil {
		c.Close()
		s.v4 = true
	}

	// Try to bind a UDP6 socket. Some kernels disable IPv6 entirely
	// (net.ipv6.conf.all.disable_ipv6=1), which causes EAFNOSUPPORT here.
	if c, err := net.ListenPacket("udp6", "[::]:0"); err == nil {
		c.Close()
		s.v6 = true
	}

	return s
}

// MDNSServer holds a running mDNS service registration.
type MDNSServer struct {
	client *zeroconf.Client
}

// RegisterMDNS advertises this gmmff instance on the local network via mDNS.
// Runs with a 3-second timeout — if mDNS setup fails or hangs it returns an
// error without blocking startup.
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
		support := detectIPSupport()
		fmt.Fprintf(os.Stderr, "local: mDNS network support detected: %s\n", support)

		if !support.v4 && !support.v6 {
			fmt.Fprintf(os.Stderr, "local: mDNS register error: no usable IP family found\n")
			ch <- nil
			return
		}

		ty  := zeroconf.NewType(mdnsService)
		svc := zeroconf.NewService(ty, mdnsInstance, uint16(port))
		svc.Text = []string{"version=1", fmt.Sprintf("scheme=%s", scheme)}

		client, err := zeroconf.New().Network(support.network()).Publish(svc).Open()
		if err != nil {
			fmt.Fprintf(os.Stderr, "local: mDNS register error: %v\n", err)
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

	support := detectIPSupport()
	if !support.v4 && !support.v6 {
		return nil, fmt.Errorf("local: mDNS browse: no usable IP family found")
	}

	var peers []PeerInfo
	ty := zeroconf.NewType(mdnsService)
	cb := makeBrowseCallback(&peers)

	client, err := zeroconf.New().Network(support.network()).Browse(cb, ty).Open()
	if err != nil {
		return nil, fmt.Errorf("local: mDNS browse: %w", err)
	}
	defer client.Close()

	<-scanCtx.Done()
	return peers, nil
}

// makeBrowseCallback returns a zeroconf event handler that appends discovered
// peers to the provided slice, preferring IPv4 addresses.
func makeBrowseCallback(peers *[]PeerInfo) func(zeroconf.Event) {
	return func(e zeroconf.Event) {
		if e.Op == zeroconf.OpRemoved || e.Name == mdnsInstance {
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
			*peers = append(*peers, PeerInfo{Addr: addr, Scheme: scheme})
		}
	}
}

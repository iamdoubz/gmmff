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

const mdnsService = "_gmmff._tcp"

// ipSupport describes which IP families are available on this host.
type ipSupport struct {
	v4 bool
	v6 bool
}

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

// detectIPSupport probes which IP families are usable on this host by
// attempting to open a UDP socket of each type.
func detectIPSupport() ipSupport {
	var s ipSupport
	if c, err := net.ListenPacket("udp4", "0.0.0.0:0"); err == nil {
		c.Close()
		s.v4 = true
	}
	if c, err := net.ListenPacket("udp6", "[::]:0"); err == nil {
		c.Close()
		s.v6 = true
	}
	return s
}

// instanceName returns a unique mDNS instance name for this process.
// Uses the system hostname so two machines on the same LAN are always
// distinguishable, with a short random suffix to handle the case where
// two instances run on the same host.
func instanceName() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "gmmff"
	}
	// Append a 4-byte random hex suffix so two instances on the same host
	// (e.g. during testing) don't collide.
	b := make([]byte, 4)
	// crypto/rand not imported here — use time-seeded simple token.
	// For an mDNS instance name uniqueness is all that's needed; this is
	// not a security-sensitive value.
	t := uint32(time.Now().UnixNano() & 0xFFFFFFFF)
	b[0] = byte(t >> 24)
	b[1] = byte(t >> 16)
	b[2] = byte(t >> 8)
	b[3] = byte(t)
	return fmt.Sprintf("%s-%x", host, b)
}

// MDNSServer holds a running mDNS service registration.
type MDNSServer struct {
	client   *zeroconf.Client
	instance string // the unique instance name we registered under
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

		name := instanceName()
		ty := zeroconf.NewType(mdnsService)
		svc := zeroconf.NewService(ty, name, uint16(port))
		svc.Text = []string{"version=1", fmt.Sprintf("scheme=%s", scheme)}

		client, err := zeroconf.New().Network(support.network()).Publish(svc).Open()
		if err != nil {
			fmt.Fprintf(os.Stderr, "local: mDNS register error: %v\n", err)
			ch <- nil
			return
		}
		fmt.Fprintf(os.Stderr, "local: mDNS registered as %q\n", name)
		ch <- &MDNSServer{client: client, instance: name}
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

func discoverPeers(ctx context.Context, scanDuration time.Duration, selfName string) ([]PeerInfo, error) {
	scanCtx, cancel := context.WithTimeout(ctx, scanDuration)
	defer cancel()

	support := detectIPSupport()
	if !support.v4 && !support.v6 {
		return nil, fmt.Errorf("local: mDNS browse: no usable IP family found")
	}

	var peers []PeerInfo
	ty := zeroconf.NewType(mdnsService)
	cb := makeBrowseCallback(&peers, selfName)

	client, err := zeroconf.New().Network(support.network()).Browse(cb, ty).Open()
	if err != nil {
		return nil, fmt.Errorf("local: mDNS browse: %w", err)
	}
	defer client.Close()

	<-scanCtx.Done()
	return peers, nil
}

// makeBrowseCallback returns a zeroconf event handler that appends discovered
// peers, excluding the instance registered by this process (selfName).
func makeBrowseCallback(peers *[]PeerInfo, selfName string) func(zeroconf.Event) {
	return func(e zeroconf.Event) {
		// Ignore removals and our own announcement.
		if e.Op == zeroconf.OpRemoved {
			return
		}
		if selfName != "" && e.Name == selfName {
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

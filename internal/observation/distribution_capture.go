package observation

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"
	"github.com/mdlayher/packet"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

type ingressCapture struct {
	connection  *packet.Conn
	ready       CaptureReady
	mu          sync.Mutex
	frames      []TCPIngress
	err         error
	done        chan struct{}
	stopContext func() bool
	notify      chan<- struct{}
	closing     atomic.Bool
	finishOnce  sync.Once
	finishError error
}

func (executor *Executor) openIngressCapture(ctx context.Context, provider ProviderIngress, limit int, notify chan<- struct{}) (*ingressCapture, error) {
	bridge, err := netlink.LinkByName(provider.Bridge)
	if err != nil {
		return nil, errors.New("provider capture bridge is unavailable")
	}
	if bridge.Type() != "bridge" {
		return nil, errors.New("provider capture interface is not a bridge")
	}
	port, err := netlink.LinkByName(provider.PortInterface)
	if err != nil || port.Attrs().MasterIndex != bridge.Attrs().Index {
		return nil, errors.New("provider capture port is not attached to the configured bridge")
	}
	mac, err := net.ParseMAC(provider.DestinationMAC)
	if err != nil {
		return nil, errors.New("provider capture destination MAC is invalid")
	}
	entries, err := netlink.NeighList(port.Attrs().Index, unix.AF_BRIDGE)
	if err != nil {
		return nil, errors.New("provider capture forwarding database is unavailable")
	}
	matched := false
	for _, entry := range entries {
		if entry.LinkIndex == port.Attrs().Index && bytes.Equal(entry.HardwareAddr, mac) && entry.State&unix.NUD_FAILED == 0 {
			matched = true
		}
	}
	if !matched {
		return nil, errors.New("provider capture MAC does not match the current bridge forwarding database")
	}
	device, err := net.InterfaceByIndex(bridge.Attrs().Index)
	if err != nil {
		return nil, errors.New("provider capture kernel interface is unavailable")
	}
	connection, err := packet.Listen(device, packet.Raw, unix.ETH_P_ALL, nil)
	if err != nil {
		return nil, errors.New("provider capture socket cannot be opened")
	}
	if err := connection.SetPromiscuous(true); err != nil {
		_ = connection.Close()
		return nil, errors.New("provider capture cannot observe forwarded bridge traffic")
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		_ = connection.Close()
		return nil, errors.New("provider capture deadline is unavailable")
	}
	if err := connection.SetReadDeadline(deadline); err != nil {
		_ = connection.Close()
		return nil, errors.New("provider capture deadline cannot be set")
	}
	capture := &ingressCapture{
		connection: connection,
		ready:      CaptureReady{ConnectionID: provider.ConnectionID, Interface: device.Name, PortInterface: port.Attrs().Name, DestinationMAC: mac.String(), At: executor.config.Clock.Now().UTC()},
		frames:     nil, err: nil, done: make(chan struct{}), stopContext: nil, mu: sync.Mutex{}, notify: notify,
		closing: atomic.Bool{}, finishOnce: sync.Once{}, finishError: nil,
	}
	capture.stopContext = context.AfterFunc(ctx, func() { _ = connection.Close() })
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				capture.mu.Lock()
				capture.err = fmt.Errorf("provider ingress capture panicked: %v", recovered)
				capture.mu.Unlock()
			}
			close(capture.done)
		}()
		capture.read(ctx, provider, limit, executor.config.Clock.Now)
	}()
	return capture, nil
}

func (capture *ingressCapture) read(ctx context.Context, provider ProviderIngress, limit int, now func() time.Time) {
	buffer := make([]byte, 65536)
	for {
		count, _, err := capture.connection.ReadFrom(buffer)
		if err != nil {
			if !capture.closing.Load() && ctx.Err() == nil && !errors.Is(err, net.ErrClosed) {
				capture.mu.Lock()
				capture.err = errors.New("provider ingress capture failed before completion")
				capture.mu.Unlock()
			}
			return
		}
		frame, matched := decodeIngress(buffer[:count], provider, now().UTC())
		if !matched {
			continue
		}
		capture.mu.Lock()
		if len(capture.frames) >= limit {
			capture.err = errors.New("provider ingress capture exceeded its frame bound")
			capture.mu.Unlock()
			return
		}
		capture.frames = append(capture.frames, frame)
		capture.mu.Unlock()
		select {
		case capture.notify <- struct{}{}:
		default:
		}
	}
}

func decodeIngress(data []byte, provider ProviderIngress, at time.Time) (TCPIngress, bool) {
	var absent TCPIngress
	decoded := gopacket.NewPacket(data, layers.LayerTypeEthernet, gopacket.Default)
	ethernet, ok := decoded.Layer(layers.LayerTypeEthernet).(*layers.Ethernet)
	if !ok || ethernet.DstMAC.String() != provider.DestinationMAC {
		return absent, false
	}
	tcp, ok := decoded.Layer(layers.LayerTypeTCP).(*layers.TCP)
	if !ok || !tcp.SYN || tcp.ACK {
		return absent, false
	}
	var source, destination netip.Addr
	switch provider.Family {
	case FamilyIPv4:
		header, found := decoded.Layer(layers.LayerTypeIPv4).(*layers.IPv4)
		if !found {
			return absent, false
		}
		source, _ = netip.AddrFromSlice(header.SrcIP)
		destination, _ = netip.AddrFromSlice(header.DstIP)
	case FamilyIPv6:
		header, found := decoded.Layer(layers.LayerTypeIPv6).(*layers.IPv6)
		if !found {
			return absent, false
		}
		source, _ = netip.AddrFromSlice(header.SrcIP)
		destination, _ = netip.AddrFromSlice(header.DstIP)
	default:
		return absent, false
	}
	return TCPIngress{At: at, Source: source.Unmap(), Destination: destination.Unmap(), SourcePort: uint16(tcp.SrcPort), Sequence: tcp.Seq, DestinationPort: uint16(tcp.DstPort), DestinationMAC: ethernet.DstMAC.String(), ConnectionID: provider.ConnectionID}, true
}

func (capture *ingressCapture) finish() error {
	capture.finishOnce.Do(func() {
		capture.stopContext()
		stats, statsErr := capture.connection.Stats()
		capture.closing.Store(true)
		_ = capture.connection.Close()
		<-capture.done
		if statsErr != nil || stats.Drops != 0 {
			capture.finishError = errors.New("provider ingress capture statistics are unavailable or report dropped packets")
		}
	})
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if capture.err != nil {
		return capture.err
	}
	return capture.finishError
}

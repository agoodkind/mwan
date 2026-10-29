//go:build linux && netns

package netif_test

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"runtime"
	"testing"

	"github.com/mdlayher/packet"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/netif"
)

type forwardPathNamespaceResult struct {
	namespace netns.NsHandle
	err       error
}

type forwardPathPacketEndpoints struct {
	receiver        netns.NsHandle
	senderNamespace netns.NsHandle
	sender          *packet.Conn
	client          *net.Interface
}

func isolateForwardPathEndpoints(t *testing.T, fixture forwardPathFixture) forwardPathPacketEndpoints {
	t.Helper()
	receiver := newForwardPathPacketNamespace(t)
	for _, peer := range fixture.peers {
		if err := netlink.LinkSetNsFd(peer, int(receiver)); err != nil {
			t.Fatalf("move receiver %s into its namespace: %v", peer.Attrs().Name, err)
		}
		enableForwardPathEndpoint(t, receiver, peer.Attrs().Name)
	}
	client, err := net.InterfaceByName(fixture.input.Attrs().Name + "p")
	if err != nil {
		t.Fatalf("inspect sender before namespace isolation: %v", err)
	}
	link, err := netlink.LinkByName(client.Name)
	if err != nil {
		t.Fatalf("inspect sender link before namespace isolation: %v", err)
	}
	senderNamespace := newForwardPathPacketNamespace(t)
	if err := netlink.LinkSetNsFd(link, int(senderNamespace)); err != nil {
		t.Fatalf("move sender %s into its namespace: %v", client.Name, err)
	}
	enableForwardPathEndpoint(t, senderNamespace, client.Name)
	sender := openForwardPathReceiverSocket(t, senderNamespace, client.Name)
	t.Cleanup(func() {
		if err := sender.Close(); err != nil {
			t.Errorf("close isolated packet sender: %v", err)
		}
	})
	router, err := netns.Get()
	if err != nil {
		t.Fatalf("inspect router namespace identity: %v", err)
	}
	defer func() {
		if err := router.Close(); err != nil {
			t.Errorf("close router namespace observation: %v", err)
		}
	}()
	if router.Equal(senderNamespace) || router.Equal(receiver) || senderNamespace.Equal(receiver) {
		t.Fatal("packet sender, router and receivers require distinct network namespaces")
	}
	t.Logf("forwarding namespaces: sender=%s router=%s receivers=%s", senderNamespace, router, receiver)
	return forwardPathPacketEndpoints{receiver: receiver, senderNamespace: senderNamespace, sender: sender, client: client}
}

func enableForwardPathEndpoint(t *testing.T, namespace netns.NsHandle, name string) {
	t.Helper()
	handle, err := netlink.NewHandleAt(namespace, unix.NETLINK_ROUTE)
	if err != nil {
		t.Fatalf("open endpoint namespace for %s: %v", name, err)
	}
	defer func() {
		if err := handle.Close(); err != nil {
			t.Errorf("close endpoint namespace socket for %s: %v", name, err)
		}
	}()
	link, err := handle.LinkByName(name)
	if err != nil {
		t.Fatalf("inspect endpoint %s in its namespace: %v", name, err)
	}
	if err := handle.LinkSetUp(link); err != nil {
		t.Fatalf("enable endpoint %s in its namespace: %v", name, err)
	}
}

func newForwardPathPacketNamespace(t *testing.T) netns.NsHandle {
	t.Helper()
	results := make(chan forwardPathNamespaceResult, 1)
	go func() {
		// The runtime destroys this thread when the goroutine exits.
		runtime.LockOSThread()
		namespace, err := netns.New()
		if err == nil {
			settings := netif.NewProcSysctlRunner(slog.Default(), false)
			for _, family := range []string{"ipv4", "ipv6"} {
				if err = settings.Set(t.Context(), "net."+family+".conf.all.forwarding", "0"); err != nil {
					err = errors.Join(err, namespace.Close())
					break
				}
			}
		}
		results <- forwardPathNamespaceResult{namespace: namespace, err: err}
	}()
	var result forwardPathNamespaceResult
	select {
	case result = <-results:
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	if result.err != nil {
		t.Fatalf("create non-forwarding receiver namespace: %v", result.err)
	}
	t.Cleanup(func() {
		if err := result.namespace.Close(); err != nil {
			t.Errorf("close receiver namespace: %v", err)
		}
	})
	return result.namespace
}

type forwardPathReceiverSocketResult struct {
	connection *packet.Conn
	err        error
}

func openForwardPathReceiverSocket(t *testing.T, namespace netns.NsHandle, name string) *packet.Conn {
	t.Helper()
	results := make(chan forwardPathReceiverSocketResult, 1)
	go func() {
		runtime.LockOSThread()
		connection, err := forwardPathReceiverSocket(t, namespace, name)
		results <- forwardPathReceiverSocketResult{connection: connection, err: err}
	}()
	select {
	case result := <-results:
		if result.err != nil {
			t.Fatalf("open receiver %s in its namespace: %v", name, result.err)
		}
		return result.connection
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
		return nil
	}
}

func forwardPathReceiverSocket(t *testing.T, namespace netns.NsHandle, name string) (*packet.Conn, error) {
	t.Helper()
	if err := netns.Set(namespace); err != nil {
		return nil, fmt.Errorf("select receiver namespace: %w", err)
	}
	link, err := netlink.LinkByName(name)
	if err != nil {
		return nil, fmt.Errorf("inspect receiver: %w", err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return nil, fmt.Errorf("enable receiver: %w", err)
	}
	current, err := net.InterfaceByName(name)
	if err != nil {
		return nil, fmt.Errorf("inspect enabled receiver: %w", err)
	}
	connection, err := packet.Listen(current, packet.Raw, unix.ETH_P_ALL, nil)
	if err != nil {
		return nil, fmt.Errorf("open receiver capture: %w", err)
	}
	if deadline, bounded := t.Deadline(); bounded {
		if err := connection.SetDeadline(deadline); err != nil {
			return nil, errors.Join(fmt.Errorf("set receiver deadline: %w", err), connection.Close())
		}
	}
	return connection, nil
}

//go:build linux && firewallnetns

package main

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/vishvananda/netns"
)

func tunnelRuntimePayload(size int) []byte {
	payload := make([]byte, size)
	for index := range payload {
		payload[index] = byte(index % 251)
	}
	return payload
}

func tunnelRuntimeTransfer(t *testing.T, topology tunnelRuntimeTopology, destinationNamespace, sourceNamespace netns.NsHandle, network, destination, source string, size int) (netip.Addr, error) {
	t.Helper()
	defer setRuntimeNamespace(t, topology.gateway)
	setRuntimeNamespace(t, destinationNamespace)
	listener, err := net.Listen(network, destination)
	if err != nil {
		t.Fatalf("listen on %s: %v", destination, err)
	}
	defer func() { _ = listener.Close() }()
	type served struct {
		peer netip.Addr
		err  error
	}
	results := make(chan served, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			results <- served{err: acceptErr}
			return
		}
		defer func() { _ = connection.Close() }()
		_ = connection.SetDeadline(time.Now().Add(15 * time.Second))
		received := make([]byte, size)
		if _, readErr := io.ReadFull(connection, received); readErr != nil {
			results <- served{err: fmt.Errorf("listener read: %w", readErr)}
			return
		}
		if _, writeErr := connection.Write(received); writeErr != nil {
			results <- served{err: fmt.Errorf("listener write: %w", writeErr)}
			return
		}
		peer, _ := netip.ParseAddrPort(connection.RemoteAddr().String())
		results <- served{peer: peer.Addr()}
	}()

	setRuntimeNamespace(t, sourceNamespace)
	local, err := net.ResolveTCPAddr(network, source)
	if err != nil {
		t.Fatal(err)
	}
	dialer := net.Dialer{LocalAddr: local, Timeout: time.Second}
	connection, err := dialer.Dial(network, destination)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("dial %s from %s: %w", destination, source, err)
	}
	defer func() { _ = connection.Close() }()
	_ = connection.SetDeadline(time.Now().Add(15 * time.Second))
	payload := tunnelRuntimePayload(size)
	writeErrors := make(chan error, 1)
	go func() {
		_, writeErr := connection.Write(payload)
		writeErrors <- writeErr
	}()
	returned := make([]byte, size)
	if _, err := io.ReadFull(connection, returned); err != nil {
		return netip.Addr{}, fmt.Errorf("client read from %s: %w", destination, err)
	}
	if err := <-writeErrors; err != nil {
		return netip.Addr{}, fmt.Errorf("client write to %s: %w", destination, err)
	}
	if !bytes.Equal(returned, payload) {
		return netip.Addr{}, fmt.Errorf("transfer with %s returned different bytes", destination)
	}
	result := <-results
	return result.peer, result.err
}

func waitTunnelRuntimeTransfer(t *testing.T, daemon *runtimeDaemon, topology tunnelRuntimeTopology, destinationNamespace, sourceNamespace netns.NsHandle, network, destination, source string, window time.Duration) netip.Addr {
	t.Helper()
	deadline := time.Now().Add(window)
	var lastErr error
	for time.Now().Before(deadline) {
		peer, err := tunnelRuntimeTransfer(t, topology, destinationNamespace, sourceNamespace, network, destination, source, tunnelRuntimeTransferSize)
		if err == nil {
			return peer
		}
		lastErr = err
		t.Logf("%s transfer to %s from %s failed and repeats: %v", network, destination, source, err)
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("no %s transfer to %s from %s within %s: %v; daemon=%s", network, destination, source, window, lastErr, tunnelRuntimeLog(t, daemon))
	return netip.Addr{}
}

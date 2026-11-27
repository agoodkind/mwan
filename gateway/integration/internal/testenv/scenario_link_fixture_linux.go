//go:build linux && (firewall || protocol || systemd)

package testenv

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"goodkind.io/mwan/gateway/internal/forwardingready"
	"goodkind.io/mwan/gateway/internal/netif"
)

const (
	OwnedRuntimeParentMAC = "02:00:5e:39:70:01"
	OwnedRuntimeLateMAC   = "02:00:5e:39:70:02"
)

func KillOwnedRuntimeDaemon(t *testing.T, daemon *RuntimeDaemon) {
	t.Helper()
	if daemon.Stopped {
		return
	}
	daemon.Stopped = true
	if err := daemon.Command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Errorf("kill WAN daemon: %v", err)
	}
	select {
	case err := <-daemon.Done:
		if err == nil {
			t.Fatal("killed daemon exited successfully")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("wait for WAN daemon after SIGKILL: deadline exceeded")
	}
}

func WaitOwnedRoleForwarding(
	t *testing.T,
	daemon *RuntimeDaemon,
	socket string,
	expected forwardingready.State,
) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var state forwardingready.State
	var err error
	var stableAt time.Time
	for time.Now().Before(deadline) {
		state, err = forwardingready.Read(context.Background(), socket, 500*time.Millisecond)
		if err == nil && state == expected {
			if stableAt.IsZero() {
				stableAt = time.Now()
			}
			if time.Since(stableAt) >= time.Second {
				return
			}
		} else {
			stableAt = time.Time{}
		}
		AssertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf(
		"forwarding readiness=%+v expected=%+v err=%v; %s",
		state,
		expected,
		err,
		RuntimeLogTail(t, daemon, 50),
	)
}

func WriteOwnedRuntimeNetwork(t *testing.T, directory string, includeSecond, includeOwned bool) {
	t.Helper()
	fixture, err := os.ReadFile(filepath.Join("..", "..", "yang", "instances", "network-min.json"))
	if err != nil {
		t.Fatal(err)
	}
	fixture = bytes.ReplaceAll(
		fixture,
		[]byte(`"goodkind-mwan-steering:link-files": "rendered"`),
		[]byte(`"goodkind-mwan-steering:link-files": "hand-authored"`),
	)
	legacyWAN := []byte(
		`"name": "enatt0",
        "type": "iana-if-type:other",
        "goodkind-mwan-steering:link-files": "hand-authored"`,
	)
	typedWAN := []byte(
		`"name": "enatt0",
        "type": "iana-if-type:other",
        "goodkind-mwan-steering:owner": "external",
        "goodkind-mwan-steering:link": {
          "match": { "hardware-address": "02:00:5e:00:53:aa" }
        }`,
	)
	if !bytes.Contains(fixture, legacyWAN) {
		t.Fatal("test fixture has no hand-authored enatt0 entry")
	}
	fixture = bytes.Replace(fixture, legacyWAN, typedWAN, 1)
	var document map[string]json.RawMessage
	if err := json.Unmarshal(fixture, &document); err != nil {
		t.Fatal(err)
	}
	var interfaces map[string]json.RawMessage
	if err := json.Unmarshal(document["ietf-interfaces:interfaces"], &interfaces); err != nil {
		t.Fatal(err)
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(interfaces["interface"], &entries); err != nil {
		t.Fatal(err)
	}
	add := func(value string) {
		entries = append(entries, json.RawMessage(value))
	}
	add(
		`{"name":"ownphys0","type":"iana-if-type:other","goodkind-mwan-steering:connection-id":"ownphys0","goodkind-mwan-steering:owner":"external","goodkind-mwan-steering:link":{"match":{"hardware-address":"` + OwnedRuntimeParentMAC + `"}}}`,
	)
	if includeOwned {
		add(
			`{"name":"owned-br","type":"iana-if-type:bridge","goodkind-mwan-steering:connection-id":"owned-br","goodkind-mwan-steering:owner":"mwan","goodkind-mwan-steering:link":{}}`,
		)
		add(
			`{"name":"owned397","type":"iana-if-type:other","goodkind-mwan-steering:connection-id":"owned397","goodkind-mwan-steering:owner":"mwan","goodkind-mwan-steering:link":{"vlan":{"parent":"ownphys0","id":397}}}`,
		)
		if includeSecond {
			add(
				`{"name":"owned398","type":"iana-if-type:other","goodkind-mwan-steering:connection-id":"owned398","goodkind-mwan-steering:owner":"mwan","goodkind-mwan-steering:link":{"vlan":{"parent":"ownphys0","id":398}}}`,
			)
		}
	}
	add(
		`{"name":"latephys0","type":"iana-if-type:other","goodkind-mwan-steering:connection-id":"latephys0","goodkind-mwan-steering:owner":"external","goodkind-mwan-steering:link":{"match":{"hardware-address":"` + OwnedRuntimeLateMAC + `"}}}`,
	)
	if includeOwned {
		add(
			`{"name":"late397","type":"iana-if-type:other","goodkind-mwan-steering:connection-id":"late397","goodkind-mwan-steering:owner":"mwan","goodkind-mwan-steering:link":{"vlan":{"parent":"latephys0","id":397}}}`,
		)
	}
	interfaces["interface"], err = json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	document["ietf-interfaces:interfaces"], err = json.Marshal(interfaces)
	if err != nil {
		t.Fatal(err)
	}
	output, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "network.json"), output, 0o600); err != nil {
		t.Fatal(err)
	}
}

func WaitOwnedRuntimeLink(
	t *testing.T,
	daemon *RuntimeDaemon,
	name string,
	timeout time.Duration,
) netlink.Link {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		link, err := netlink.LinkByName(name)
		if err == nil && link.Attrs().Flags&net.FlagUp != 0 {
			return link
		}
		AssertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("link %s was not enabled: %s", name, RuntimeLogTail(t, daemon, 40))
	return nil
}

func WaitOwnedRuntimeAbsent(
	t *testing.T,
	daemon *RuntimeDaemon,
	name string,
	timeout time.Duration,
) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := netlink.LinkByName(name); netif.IsLinkNotFound(err) {
			return
		} else if err != nil {
			t.Fatalf("inspect link %s: %v", name, err)
		}
		AssertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("link %s was not removed: %s", name, RuntimeLogTail(t, daemon, 40))
}

func AssertOwnedRuntimeVLANPacket(
	t *testing.T,
	gateway netns.NsHandle,
	peer netns.NsHandle,
	gatewayVLAN netlink.Link,
) {
	t.Helper()
	address, err := netlink.ParseAddr("10.39.7.1/24")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrAdd(gatewayVLAN, address); err != nil {
		t.Fatal(err)
	}
	SetRuntimeNamespace(t, peer)
	parent, err := netlink.LinkByName("owned-peer")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkAdd(&netlink.Vlan{
		LinkAttrs: netlink.LinkAttrs{Name: "peer397", ParentIndex: parent.Attrs().Index},
		VlanId:    397,
	}); err != nil {
		t.Fatal(err)
	}
	ConfigureRuntimeLink(t, "peer397", []string{"10.39.7.2/24"})
	listener, err := net.ListenPacket("udp4", "10.39.7.2:39700")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	SetRuntimeNamespace(t, gateway)
	connection, err := net.DialTimeout("udp4", "10.39.7.2:39700", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.Write([]byte("owned-vlan")); err != nil {
		t.Fatal(err)
	}
	SetRuntimeNamespace(t, peer)
	if err := listener.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 32)
	count, _, err := listener.ReadFrom(buffer)
	SetRuntimeNamespace(t, gateway)
	if err != nil || string(buffer[:count]) != "owned-vlan" {
		t.Fatalf("VLAN packet: count=%d data=%q error=%v", count, buffer[:count], err)
	}
}

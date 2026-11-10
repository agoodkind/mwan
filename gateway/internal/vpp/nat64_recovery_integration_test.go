//go:build linux && vpp_integration

package vpp_test

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"go.fd.io/govpp"
	"go.fd.io/govpp/api"
	"go.fd.io/govpp/binapi/feature"
	"go.fd.io/govpp/binapi/ip_types"
	"go.fd.io/govpp/binapi/nat64"
	"go.fd.io/govpp/binapi/nat_types"
	"go.fd.io/govpp/core"

	vppclient "goodkind.io/mwan/internal/vpp"
)

func verifyNAT64Recovery(t *testing.T, logger *slog.Logger, settings vppclient.Settings, policy vppclient.Policy) {
	t.Helper()
	settings.TAPs = slices.Clone(settings.TAPs)
	connection, err := govpp.Connect(settings.Endpoints.APISocket)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	if err := connection.CheckCompatibility(&feature.FeatureIsEnabled{}, &feature.FeatureIsEnabledReply{}); err != nil {
		t.Fatal(err)
	}
	settings.StateFile = filepath.Join(filepath.Dir(settings.StateFile), "completed-enable.json")
	controller := recoveryController(t, settings)
	if err := controller.Reconcile(t.Context(), logger, policy); err != nil {
		t.Fatalf("complete an owned NAT64 enable: %v", err)
	}
	verifyEnableJournal(t, settings.StateFile, true, true, false)
	removeRecoveryPolicy(t, logger, settings)
	verifyNAT64InterfaceCount(t, connection, 0)
	disable := *settings.NAT64
	disable.Enable = false
	if _, err := nat64.NewServiceClient(connection).Nat64PluginEnableDisable(t.Context(), &disable); err != nil {
		t.Fatalf("reset the empty private NAT64 instance after its owned enable: %v", err)
	}

	settings.StateFile = filepath.Join(filepath.Dir(settings.StateFile), "pending-enable.json")
	empty := settings
	empty.TAPs = nil
	if err := recoveryController(t, empty).Reconcile(t.Context(), logger, vppclient.Policy{}); err != nil {
		t.Fatal(err)
	}
	record := readRecoveryJournal(t, settings.StateFile)
	record["NAT64EnablePending"] = json.RawMessage("true")
	writeRecoveryJournal(t, settings.StateFile, record)
	enable := *settings.NAT64
	enable.Enable = true
	if _, err := nat64.NewServiceClient(connection).Nat64PluginEnableDisable(t.Context(), &enable); err != nil {
		t.Fatalf("apply the journaled enable before its interrupted completion: %v", err)
	}
	if _, err := nat64.NewServiceClient(connection).Nat64PluginEnableDisable(t.Context(), &enable); !errors.Is(err, api.VPPApiError(1)) {
		t.Fatalf("VPP 26.06 must identify the already enabled zero-interface plugin with typed result 1: %v", err)
	}
	verifyEnableJournal(t, settings.StateFile, false, false, true)
	verifyNAT64InterfaceCount(t, connection, 0)
	controller = recoveryController(t, settings)
	if err := controller.Reconcile(t.Context(), logger, policy); err != nil {
		t.Fatalf("resume an interrupted NAT64 enable with zero interfaces: %v", err)
	}
	verifyEnableJournal(t, settings.StateFile, true, false, false)
	verifyPendingTAPReplacement(t, logger, connection, settings, policy)
	removeRecoveryPolicy(t, logger, settings)
	verifyNAT64InterfaceCount(t, connection, 0)

	foreignPool, err := ip_types.ParseIP4Address("203.0.113.77")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nat64.NewServiceClient(connection).Nat64AddDelPoolAddrRange(t.Context(), &nat64.Nat64AddDelPoolAddrRange{StartAddr: foreignPool, EndAddr: foreignPool, VrfID: ^uint32(0), IsAdd: true}); err != nil {
		t.Fatal(err)
	}
	foreignPrefix, err := ip_types.ParseIP6Prefix("2001:db8:77:64::/96")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nat64.NewServiceClient(connection).Nat64AddDelPrefix(t.Context(), &nat64.Nat64AddDelPrefix{Prefix: foreignPrefix, VrfID: 77, IsAdd: true}); err != nil {
		t.Fatal(err)
	}
	verifyForeignNAT64Resources(t, connection)
	verifyNAT64InterfaceCount(t, connection, 0)
}

func verifyPendingTAPReplacement(t *testing.T, logger *slog.Logger, connection *core.Connection, settings vppclient.Settings, policy vppclient.Policy) {
	t.Helper()
	record := readRecoveryJournal(t, settings.StateFile)
	var objects []json.RawMessage
	if err := json.Unmarshal(record["Objects"], &objects); err != nil {
		t.Fatal(err)
	}
	found := false
	for index, object := range objects {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(object, &fields); err != nil {
			t.Fatal(err)
		}
		var name string
		if err := json.Unmarshal(fields["Interface"], &name); err != nil {
			t.Fatal(err)
		}
		var membership nat64.Nat64AddDelInterface
		if err := json.Unmarshal(fields["NAT64"], &membership); err != nil {
			t.Fatal(err)
		}
		if name != policy.NAT64.Inside || membership.Flags&nat_types.NAT_IS_INSIDE == 0 {
			continue
		}
		pending, err := json.Marshal([]json.RawMessage{object})
		if err != nil {
			t.Fatal(err)
		}
		record["Pending"] = pending
		objects = slices.Delete(objects, index, index+1)
		found = true
		break
	}
	if !found {
		t.Fatal("the completed policy did not record its owned inside NAT64 membership")
	}
	completed, err := json.Marshal(objects)
	if err != nil {
		t.Fatal(err)
	}
	record["Objects"] = completed
	writeRecoveryJournal(t, settings.StateFile, record)
	settings.TAPs = slices.Clone(settings.TAPs)
	for index := range settings.TAPs {
		if settings.TAPs[index].ConnectionID == "vpp-test-inside" {
			settings.TAPs[index].Create.RxRingSz = 512
		}
	}
	if err := recoveryController(t, settings).Reconcile(t.Context(), logger, policy); err != nil {
		t.Fatalf("resolve pending membership before replacing its TAP: %v", err)
	}
	client, err := vppclient.Open(t.Context(), settings.Endpoints)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	interfaces, err := client.Interfaces(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	inside, exists := interfaces[policy.NAT64.Inside]
	if !exists {
		t.Fatal("the replacement inside TAP does not exist")
	}
	reply, err := feature.NewServiceClient(connection).FeatureIsEnabled(t.Context(), &feature.FeatureIsEnabled{ArcName: "ip6-unicast", FeatureName: "nat64-in2out", SwIfIndex: inside.SwIfIndex})
	if err != nil || !reply.IsEnabled {
		t.Fatalf("the replacement TAP must enable NAT64 packet processing after resolving pending membership: %v, %+v", err, reply)
	}
	record = readRecoveryJournal(t, settings.StateFile)
	var pending []json.RawMessage
	if err := json.Unmarshal(record["Pending"], &pending); err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("replacement left %d unresolved ownership operations", len(pending))
	}
}

func removeRecoveryPolicy(t *testing.T, logger *slog.Logger, settings vppclient.Settings) {
	t.Helper()
	settings.TAPs = nil
	if err := recoveryController(t, settings).Reconcile(t.Context(), logger, vppclient.Policy{}); err != nil {
		t.Fatalf("remove the private recovery policy: %v", err)
	}
}

func recoveryController(t *testing.T, settings vppclient.Settings) *vppclient.Controller {
	t.Helper()
	controller, err := vppclient.NewController(settings)
	if err != nil {
		t.Fatal(err)
	}
	return controller
}

func readRecoveryJournal(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]json.RawMessage
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	return record
}

func writeRecoveryJournal(t *testing.T, path string, record map[string]json.RawMessage) {
	t.Helper()
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func verifyEnableJournal(t *testing.T, path string, enabled, owned, pending bool) {
	t.Helper()
	record := readRecoveryJournal(t, path)
	for field, expected := range map[string]bool{"NAT64Enabled": enabled, "NAT64Owned": owned, "NAT64EnablePending": pending} {
		var actual bool
		if err := json.Unmarshal(record[field], &actual); err != nil {
			t.Fatal(err)
		}
		if actual != expected {
			t.Fatalf("journal %s = %t; expected %t", field, actual, expected)
		}
	}
}

func verifyNAT64InterfaceCount(t *testing.T, connection *core.Connection, expected int) {
	t.Helper()
	stream, err := nat64.NewServiceClient(connection).Nat64InterfaceDump(t.Context(), &nat64.Nat64InterfaceDump{})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	count := 0
	for {
		_, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		count++
	}
	if count != expected {
		t.Fatalf("private VPP has %d NAT64 interfaces; expected %d", count, expected)
	}
}

func verifyForeignNAT64Resources(t *testing.T, connection *core.Connection) {
	t.Helper()
	pool, err := nat64.NewServiceClient(connection).Nat64PoolAddrDump(t.Context(), &nat64.Nat64PoolAddrDump{})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	poolPreserved := false
	for {
		entry, err := pool.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if entry.Address.String() == "203.0.113.77" && entry.VrfID == ^uint32(0) {
			poolPreserved = true
		}
	}
	prefixes, err := nat64.NewServiceClient(connection).Nat64PrefixDump(t.Context(), &nat64.Nat64PrefixDump{})
	if err != nil {
		t.Fatal(err)
	}
	defer prefixes.Close()
	prefixPreserved := false
	for {
		entry, err := prefixes.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if entry.Prefix.String() == "2001:db8:77:64::/96" && entry.VrfID == 77 {
			prefixPreserved = true
		}
	}
	if !poolPreserved || !prefixPreserved {
		t.Fatalf("NAT64 recovery changed foreign resources: pool %t, prefix %t", poolPreserved, prefixPreserved)
	}
}

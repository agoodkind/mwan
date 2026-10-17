//go:build linux && netns

package steering

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/vishvananda/netns"

	"goodkind.io/mwan/internal/ifmgr"
)

const (
	// Linux uses net.core.rmem_default for sockets without an explicit buffer size.
	// The testbed gateway used this 212992-byte default when the overflow occurred.
	defaultReceiveBufferBytes = 212992
	defaultReceiveBufferPath  = "/proc/sys/net/core/rmem_default"
	// A standalone reproduction overflowed a 212992-byte buffer on every run with
	// at least 60 map rules. The test rejects smaller buildRules results before
	// Apply to require a batch that consistently reproduced the overflow.
	minimumMapRules  = 60
	batchMemberCount = 40
)

func TestApplyCommitsBatchLargerThanDefaultReceiveBuffer(t *testing.T) {
	useDefaultReceiveBuffer(t)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	original, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	namespace, err := netns.New()
	if err != nil {
		t.Fatal(err)
	}
	defer namespace.Close()
	defer func() {
		if err := netns.Set(original); err != nil {
			t.Error(err)
		}
	}()

	members := make([]Member, 0, batchMemberCount)
	for index := range batchMemberCount {
		name := fmt.Sprintf("wan%d", index)
		members = append(members, Member{
			WANRef: ifmgr.WANRef{Name: name, Iface: name},
			Mark:   uint32(index + 1),
		})
	}
	slots := balancer{Modulus: 2, Slots: []uint32{1, 2}}
	rules := buildRules(ruleInput{
		InternalIface: "lan", InternalNetV4: netip.MustParsePrefix("10.0.0.0/24"),
		InternalPrefix: netip.MustParsePrefix("fd00::/64"),
		OpnsenseEdgeV6: netip.MustParseAddr("fd00::1"), Mode: hashModeRandom,
		AssignV4: slots, AssignV6: slots, Members: members,
	})
	mapRules := 0
	for _, rule := range rules {
		if len(rule.Assign.Slots) > 0 {
			mapRules++
		}
	}
	if mapRules < minimumMapRules {
		t.Fatalf("map rules = %d, want at least %d", mapRules, minimumMapRules)
	}

	if err := newNFTApplier().Apply(context.Background(), slog.Default(), rules); err != nil {
		t.Fatalf("apply %d rules with %d map rules: %v", len(rules), mapRules, err)
	}
	if listed := listedSteeringRules(t); listed != len(rules) {
		t.Fatalf("nft lists %d rules in table inet %s, want %d", listed, steerTableName, len(rules))
	}
}

// A larger receive buffer could let the test pass without the production fix.
// Linux 7.0 rejects writes to net.core.rmem_default in a new network namespace.
// This helper sets the default in the namespace where the test starts.
func useDefaultReceiveBuffer(t *testing.T) {
	t.Helper()
	raw, err := os.ReadFile(defaultReceiveBufferPath)
	if err != nil {
		t.Fatal(err)
	}
	original := strings.TrimSpace(string(raw))
	want := strconv.Itoa(defaultReceiveBufferBytes)
	if original == want {
		return
	}
	if err := os.WriteFile(defaultReceiveBufferPath, []byte(want), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.WriteFile(defaultReceiveBufferPath, []byte(original), 0o600); err != nil {
			t.Errorf("restore net.core.rmem_default to %s: %v", original, err)
		}
	})
}

// nft -j list table inet mwan_steer reads the kernel ruleset independently of
// google/nftables. Count its JSON rule objects to verify the committed rules.
func listedSteeringRules(t *testing.T) int {
	t.Helper()
	output, err := exec.Command("nft", "-j", "list", "table", "inet", steerTableName).CombinedOutput()
	if err != nil {
		t.Fatalf("nft list table inet %s: %v\n%s", steerTableName, err, output)
	}
	var listing struct {
		Nftables []map[string]json.RawMessage `json:"nftables"`
	}
	if err := json.Unmarshal(output, &listing); err != nil {
		t.Fatalf("decode nft listing: %v\n%s", err, output)
	}
	count := 0
	for _, object := range listing.Nftables {
		if _, ok := object["rule"]; ok {
			count++
		}
	}
	return count
}

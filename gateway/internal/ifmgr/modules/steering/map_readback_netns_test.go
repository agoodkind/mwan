//go:build linux && netns

package steering

import (
	"bytes"
	"context"
	"log/slog"
	"slices"
	"testing"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"

	"goodkind.io/mwan/internal/firewall"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/wanstate"
)

func markProofReadback(t *testing.T, ctx context.Context, writer ifmgr.Module, policy Config, snapshot wanstate.Snapshot) {
	t.Helper()
	desired, err := CompilePolicy(policy, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := firewall.Inspect(ctx, desired); err != nil {
		t.Fatalf("correct host-order steering map failed certification: %v", err)
	}
	for _, reverseKey := range []bool{true, false} {
		reverseSteeringMapBytes(t, reverseKey)
		if _, err := firewall.Inspect(ctx, desired); err == nil {
			t.Fatalf("steering certification accepted key=%v byte reversal", reverseKey)
		} else {
			t.Logf("rejected key=%v byte reversal: %v", reverseKey, err)
		}
		if err := writer.Reconcile(ctx, slog.Default()); err != nil {
			t.Fatalf("restore production steering map after key=%v byte reversal: %v", reverseKey, err)
		}
		if _, err := firewall.Inspect(ctx, desired); err != nil {
			t.Fatalf("restored host-order steering map failed certification: %v", err)
		}
	}
}

func reverseSteeringMapBytes(t *testing.T, reverseKey bool) {
	t.Helper()
	connection, err := nftables.New()
	if err != nil {
		t.Fatal(err)
	}
	table := &nftables.Table{Name: steerTableName, Family: nftables.TableFamilyINet}
	chain, err := connection.ListChain(table, steerChainName)
	if err != nil {
		t.Fatal(err)
	}
	rules, err := connection.GetRules(table, chain)
	if err != nil || len(rules) == 0 {
		t.Fatalf("read production steering rules: count=%d error=%v", len(rules), err)
	}
	for index, expression := range rules[0].Exprs {
		lookup, present := expression.(*expr.Lookup)
		if !present {
			continue
		}
		set, err := connection.GetSetByName(table, lookup.SetName)
		if err != nil {
			t.Fatal(err)
		}
		elements, err := connection.GetSetElements(set)
		if err != nil || len(elements) != 2 {
			t.Fatalf("read production steering map: count=%d error=%v", len(elements), err)
		}
		reverseSteeringElements(t, elements, reverseKey)
		set.ID, set.Name = 0, ""
		if err := connection.AddSet(set, elements); err != nil {
			t.Fatal(err)
		}
		replacement := *lookup
		replacement.SetID, replacement.SetName = set.ID, set.Name
		rules[0].Exprs[index] = &replacement
		connection.ReplaceRule(rules[0])
		if err := connection.Flush(); err != nil {
			t.Fatalf("replace production steering map with key=%v byte reversal: %v", reverseKey, err)
		}
		return
	}
	t.Fatal("production steering rule did not contain an anonymous map")
}

func reverseSteeringElements(t *testing.T, elements []nftables.SetElement, reverseKey bool) {
	t.Helper()
	for index := range elements {
		element := &elements[index]
		if len(element.Key) != 4 || len(element.Val) != 4 {
			t.Fatalf("production steering map used an unexpected word width: key=%x value=%x", element.Key, element.Val)
		}
		slot := binaryutil.NativeEndian.Uint32(element.Key)
		mark := binaryutil.NativeEndian.Uint32(element.Val)
		if slot >= 2 || mark != [2]uint32{markProofFirstMark, markProofSecondMark}[slot] {
			t.Fatalf("production steering map changed native slot/mark bytes: slot=%d mark=%d key=%x value=%x", slot, mark, element.Key, element.Val)
		}
		if reverseKey {
			element.Key = bytes.Clone(element.Key)
			slices.Reverse(element.Key)
		} else {
			element.Val = bytes.Clone(element.Val)
			slices.Reverse(element.Val)
		}
	}
}

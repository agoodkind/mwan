package wanconfig

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"slices"
	"testing"
)

// recordingPublisher captures the one ReplaceConfig call Publish makes.
type recordingPublisher struct {
	ownedPaths []string
	items      []Item
	calls      int
	err        error
}

func (r *recordingPublisher) ReplaceConfig(_ context.Context, ownedPaths []string, items []Item) error {
	r.calls++
	r.ownedPaths = ownedPaths
	r.items = items
	return r.err
}

// TestPublish_ReplacesOwnedSubtreesWithTheProjection pins the write contract:
// one replace of exactly the owned subtrees carrying the projected items.
func TestPublish_ReplacesOwnedSubtreesWithTheProjection(t *testing.T) {
	t.Parallel()
	member := testMember("att", "enatt0")
	member.ProbePolicy = "att"
	member.NPTInternal = netip.MustParsePrefix("3d06:bad:b01:210::/60")
	member.NPTExternal = netip.MustParsePrefix("2001:db8:a::/60")
	gateway := Gateway{
		InternalIface: "eninternal0",
		Members:       []Member{member},
	}
	rec := &recordingPublisher{}

	if err := Publish(context.Background(), slog.Default(), rec, gateway); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if rec.calls != 1 {
		t.Fatalf("ReplaceConfig calls = %d, want 1", rec.calls)
	}
	wantOwned := []string{
		"/ietf-interfaces:interfaces",
		"/ietf-nat:nat",
		"/goodkind-mwan-steering:daemon",
	}
	if !slices.Equal(rec.ownedPaths, wantOwned) {
		t.Fatalf("ownedPaths = %v, want %v", rec.ownedPaths, wantOwned)
	}
	wantItems, err := ConfigItems(gateway)
	if err != nil {
		t.Fatalf("ConfigItems: %v", err)
	}
	if len(rec.items) != len(wantItems) {
		t.Fatalf("items = %d, want %d", len(rec.items), len(wantItems))
	}
}

// TestPublish_NeverWritesAnInvalidGateway pins that a gateway the projection
// rejects reaches the datastore zero times, so a bad config cannot half-write
// the tree.
func TestPublish_NeverWritesAnInvalidGateway(t *testing.T) {
	t.Parallel()
	rec := &recordingPublisher{}
	err := Publish(context.Background(), slog.Default(), rec, Gateway{InternalIface: "", Members: nil})
	if !errors.Is(err, ErrInvalidGateway) {
		t.Fatalf("err = %v, want ErrInvalidGateway", err)
	}
	if rec.calls != 0 {
		t.Fatalf("ReplaceConfig calls = %d, want 0", rec.calls)
	}
}

// TestPublish_SurfacesTheDatastoreFailure pins that a datastore rejection
// comes back to the caller, who logs it and keeps the daemon running. The
// gateway is valid, so the error can only come from the datastore call.
func TestPublish_SurfacesTheDatastoreFailure(t *testing.T) {
	t.Parallel()
	rejection := errors.New("session start failed")
	rec := &recordingPublisher{err: rejection}
	err := Publish(context.Background(), slog.Default(), rec, Gateway{
		InternalIface: "eninternal0",
		Members:       []Member{testMember("att", "enatt0")},
	})
	if rec.calls != 1 {
		t.Fatalf("ReplaceConfig calls = %d, want 1: the gateway never reached the datastore", rec.calls)
	}
	if !errors.Is(err, rejection) {
		t.Fatalf("err = %v, want the datastore rejection", err)
	}
}

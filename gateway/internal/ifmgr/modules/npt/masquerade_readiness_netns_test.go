//go:build linux && netns

package npt

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/ifmgr/modules/addresses"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/netif"
)

func TestMasqueradeAddressReadinessWithoutDHCPv6Lease(t *testing.T) {
	inIPv4Namespace(t)
	link := addDummyLink(t, "wan0")
	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatal(err)
	}
	enabled := true
	connection := interfaceintent.Connection{
		ID: "wan0", Name: "wan0", Owner: interfaceintent.OwnerMWAN,
		IPv6: &interfaceintent.IPv6{Family: interfaceintent.Family{DHCP: &enabled}, AutoConf: &enabled},
	}
	env := &ifmgr.Env{Log: slog.Default(), Connections: []interfaceintent.Connection{connection}, OwnedLinks: &ifmgr.OwnedLinkResults{}}
	env.OwnedLinks.Replace([]netif.OwnedLinkResult{{ConnectionID: "wan0", Name: "wan0", ActualName: "wan0", IfIndex: link.Attrs().Index, Status: netif.OwnedLinkReady}})
	authority, err := addresses.New(addresses.Config{Connections: env.Connections, Providers: map[string]addresses.Provider{"wan0": {IPv6: &config.IPv6Translation{Mode: config.TranslationMasquerade}}}, StateFile: filepath.Join(t.TempDir(), "state.json")})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := authority.Init(ctx, env); err != nil {
		t.Fatal(err)
	}
	if err := authority.Reconcile(ctx, slog.Default()); err != nil {
		t.Fatal(err)
	}
	if env.OwnedAddresses.FamilyReady("wan0", "ipv6") {
		t.Fatal("IPv6 is ready without a usable address")
	}
	address, err := netlink.ParseAddr("2001:db8:abcd::2/64")
	if err != nil {
		t.Fatal(err)
	}
	address.Flags = unix.IFA_F_NODAD
	if err := netlink.AddrAdd(link, address); err != nil {
		t.Fatal(err)
	}
	if err := authority.Reconcile(ctx, slog.Default()); err != nil {
		t.Fatal(err)
	}
	if !env.OwnedAddresses.FamilyReady("wan0", "ipv6") {
		t.Fatal("IPv6 is not ready with a usable address")
	}
	if err := netlink.AddrDel(link, address); err != nil {
		t.Fatal(err)
	}
	if err := authority.Reconcile(ctx, slog.Default()); err != nil {
		t.Fatal(err)
	}
	if env.OwnedAddresses.FamilyReady("wan0", "ipv6") {
		t.Fatal("IPv6 remained ready after address removal")
	}
}

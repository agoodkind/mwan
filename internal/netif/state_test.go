package netif

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"syscall"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

func TestNormalizeCIDR(t *testing.T) {
	if got := normalizeCIDR("3D06:BAD:B01:FF::1/128"); got != "3d06:bad:b01:ff::1/128" {
		t.Fatalf("normalizeCIDR got %q", got)
	}
}

func TestAddrSpecFamilyInferred(t *testing.T) {
	if (AddrSpec{CIDR: "10.0.0.1/24"}).family() != "inet" {
		t.Fatal("v4 should be inet")
	}
	if (AddrSpec{CIDR: "::1/128"}).family() != "inet6" {
		t.Fatal("v6 should be inet6")
	}
	if (AddrSpec{CIDR: "::1", Family: "inet"}).family() != "inet" {
		t.Fatal("explicit override should win")
	}
}

func TestFamilyToNetlink(t *testing.T) {
	if got := familyToNetlink("inet"); got == 0 {
		t.Fatal("inet should map to AF_INET, not 0")
	}
	if got := familyToNetlink("inet6"); got == 0 {
		t.Fatal("inet6 should map to AF_INET6, not 0")
	}
	if familyToNetlink("inet") == familyToNetlink("inet6") {
		t.Fatal("inet and inet6 must map to distinct constants")
	}
}

func TestBuildTableRoute(t *testing.T) {
	attrs := netlink.NewLinkAttrs()
	attrs.Name = "uplink0"
	attrs.Index = 17
	link := &netlink.Dummy{LinkAttrs: attrs}

	cases := []struct {
		name      string
		want      RouteSpec
		wantDest  string
		wantGw    string
		wantScope netlink.Scope
		wantProto int
	}{
		{
			name: "on-link ipv4 /29",
			want: RouteSpec{
				Family:   "inet",
				Dest:     "192.0.2.0/29",
				Dev:      "uplink0",
				TableID:  500,
				Protocol: unix.RTPROT_BGP,
			},
			wantDest:  "192.0.2.0/29",
			wantScope: netlink.SCOPE_LINK,
			wantProto: unix.RTPROT_BGP,
		},
		{
			name: "on-link ipv6 /128",
			want: RouteSpec{
				Family:  "inet6",
				Dest:    "2001:db8:1::1/128",
				Dev:     "uplink0",
				TableID: 501,
			},
			wantDest:  "2001:db8:1::1/128",
			wantScope: netlink.SCOPE_LINK,
		},
		{
			name: "via-gateway ipv6 /60",
			want: RouteSpec{
				Family:   "inet6",
				Dest:     "2001:db8:3::/60",
				Via:      "2001:db8:2::1",
				Dev:      "uplink0",
				TableID:  502,
				Metric:   20,
				Protocol: unix.RTPROT_BGP,
			},
			wantDest:  "2001:db8:3::/60",
			wantGw:    "2001:db8:2::1",
			wantProto: unix.RTPROT_BGP,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			route, err := buildTableRoute(slog.Default(), tc.want, link)
			if err != nil {
				t.Fatalf("buildTableRoute: %v", err)
			}
			if route.LinkIndex != attrs.Index {
				t.Fatalf("link index got %d, want %d", route.LinkIndex, attrs.Index)
			}
			if route.Table != tc.want.TableID {
				t.Fatalf("table got %d, want %d", route.Table, tc.want.TableID)
			}
			if route.Family != familyToNetlink(tc.want.Family) {
				t.Fatalf("family got %d, want %d", route.Family, familyToNetlink(tc.want.Family))
			}
			if route.Priority != tc.want.Metric {
				t.Fatalf("metric got %d, want %d", route.Priority, tc.want.Metric)
			}
			if int(route.Protocol) != tc.wantProto {
				t.Fatalf("protocol got %d, want %d", route.Protocol, tc.wantProto)
			}
			if route.Dst == nil || route.Dst.String() != tc.wantDest {
				t.Fatalf("dest got %v, want %s", route.Dst, tc.wantDest)
			}
			if tc.wantGw == "" {
				if route.Gw != nil {
					t.Fatalf("gateway got %s, want nil", route.Gw)
				}
				if route.Scope != tc.wantScope {
					t.Fatalf("scope got %d, want %d", route.Scope, tc.wantScope)
				}
				return
			}
			if !route.Gw.Equal(net.ParseIP(tc.wantGw)) {
				t.Fatalf("gateway got %s, want %s", route.Gw, tc.wantGw)
			}
		})
	}
}

// TestIsLinkNotFoundMatchesAMissingLink reads the gateway of a link that does
// not exist through the exported reader, so the error it classifies is the one
// the kernel and netlink produce, wrapped the way callers receive it.
func TestIsLinkNotFoundMatchesAMissingLink(t *testing.T) {
	t.Parallel()

	_, err := IfaceDefaultGateway("inet", "mwannolink0")
	if err == nil {
		t.Fatal("IfaceDefaultGateway on a missing link returned nil error")
	}
	if !IsLinkNotFound(err) {
		t.Fatalf("IsLinkNotFound(%v) = false, want true", err)
	}
	if IsLinkNotFound(fmt.Errorf("list rules: %w", syscall.EBUSY)) {
		t.Fatal("IsLinkNotFound matched a busy netlink read")
	}
	if IsLinkNotFound(errors.New("Link not found")) {
		t.Fatal("IsLinkNotFound matched on error text alone")
	}
	if IsLinkNotFound(nil) {
		t.Fatal("IsLinkNotFound matched a nil error")
	}
}

func TestRouteToCurrentPreservesDestination(t *testing.T) {
	t.Parallel()

	_, destination, err := net.ParseCIDR("2001:db8:1234::/48")
	if err != nil {
		t.Fatalf("ParseCIDR returned error: %v", err)
	}
	current, err := routeToCurrent(slog.Default(), netlink.Route{Dst: destination})
	if err != nil {
		t.Fatalf("routeToCurrent returned error: %v", err)
	}
	if current.Dest != "2001:db8:1234::/48" {
		t.Fatalf("routeToCurrent destination = %q, want %q", current.Dest, "2001:db8:1234::/48")
	}
}

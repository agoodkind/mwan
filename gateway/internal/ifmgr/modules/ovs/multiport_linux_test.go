package ovs

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netlink/nl"
	"goodkind.io/mwan/gateway/internal/ovsconfig"
)

func TestMultiportReadsRuntimeForConfiguredDevices(t *testing.T) {
	root := t.TempDir()
	physical := filepath.Join(root, "devices", "0000:03:00.0", "mlx5_core.sf.1")
	if err := os.MkdirAll(physical, 0o700); err != nil {
		t.Fatal(err)
	}

	iface := filepath.Join(root, "class", "net", "p0")
	if err := os.MkdirAll(iface, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(physical, filepath.Join(iface, "device")); err != nil {
		t.Fatal(err)
	}

	ports := []ovsconfig.Port{
		{Name: "host", Type: "internal"},
		{Name: "p0", Type: "doca"},
		{
			Name:    "rep0",
			Type:    "dpdk",
			Options: []ovsconfig.Option{{Name: "dpdk-devargs", Value: "03:00.0,representor=[0]"}},
		},
		{
			Name: "p1",
			Type: "dpdk",
			Options: []ovsconfig.Option{
				{Name: "dpdk-devargs", Value: "bus=pci,addr=0000:03:00.1/class=eth"},
			},
		},
	}

	for _, enabled := range []bool{false, true} {
		var readDevices []string
		err := checkMultiport(
			ports,
			filepath.Dir(iface),
			func(device string) (*netlink.DevlinkParam, error) {
				readDevices = append(readDevices, device)

				return &netlink.DevlinkParam{
					Name: multiportParameter,
					Type: nl.DEVLINK_PARAM_TYPE_BOOL,
					Values: []netlink.DevlinkParamValue{
						{CMODE: nl.DEVLINK_PARAM_CMODE_PERMANENT, Data: true},
						{
							CMODE: nl.DEVLINK_PARAM_CMODE_RUNTIME,
							Data:  device == "0000:03:00.0" || enabled,
						},
					},
				}, nil
			},
		)
		if enabled && err != nil {
			t.Fatalf("both physical ports have multiport enabled: %v", err)
		}

		if !enabled && (err == nil || !strings.Contains(err.Error(), "0000:03:00.1")) {
			t.Fatalf("expected runtime readiness error for p1: %v", err)
		}

		if !slices.Equal(readDevices, []string{"0000:03:00.0", "0000:03:00.1"}) {
			t.Fatalf("read devlink parameters on %v", readDevices)
		}
	}
}

func TestMultiportRequiresAnIdentifiablePhysicalDevice(t *testing.T) {
	for _, port := range []ovsconfig.Port{
		{Name: "geneve0", Type: "geneve"},
		{Name: "missing", Type: "doca"},
		{
			Name:    "net-ring",
			Type:    "dpdk",
			Options: []ovsconfig.Option{{Name: "dpdk-devargs", Value: "net_ring0"}},
		},
	} {
		t.Run(
			port.Name,
			func(t *testing.T) {
				err := checkMultiport(
					[]ovsconfig.Port{port},
					t.TempDir(),
					func(device string) (*netlink.DevlinkParam, error) {
						t.Fatalf("queried unrelated device %q", device)

						return nil, nil
					},
				)
				if err == nil {
					t.Fatal("accepted DOCA offload without a physical PCI device")
				}
			},
		)
	}
}

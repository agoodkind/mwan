package ovs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netlink/nl"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/gateway/internal/clock"
	"goodkind.io/mwan/gateway/internal/ovsconfig"
)

const multiportParameter = "esw_multiport"

type physicalPortType string

const (
	docaPort physicalPortType = "doca"
	dpdkPort physicalPortType = "dpdk"
)

var (
	pciAddress = regexp.MustCompile(
		`(?i)^[[:xdigit:]]{4}:[[:xdigit:]]{2}:[01][[:xdigit:]]\.[0-7]$`,
	)
	errMultiportRuntime     = errors.New("esw_multiport must be true in runtime mode")
	errPhysicalPortRequired = errors.New(
		"configure a physical doca or dpdk port to check esw_multiport",
	)
	errPortInterfaceName = errors.New("invalid Linux interface name")
	errPortPCIDevice     = errors.New("cannot identify a PCI device for this interface")
	errDPDKDevice        = errors.New("dpdk-devargs must identify a PCI device")
)

func requireMultiport(ctx context.Context, ports []ovsconfig.Port) error {
	handle, err := netlink.NewHandle(unix.NETLINK_GENERIC)
	if err != nil {
		slog.ErrorContext(ctx, "Cannot open devlink socket", "err", err)

		return fmt.Errorf("open devlink socket: %w", err)
	}
	defer handle.Close()

	return checkMultiport(
		ports,
		"/sys/class/net",
		func(device string) (*netlink.DevlinkParam, error) {
			if err := ctx.Err(); err != nil {
				slog.WarnContext(
					ctx,
					"Multiport device check canceled",
					"device",
					device,
					"err",
					err,
				)

				return nil, fmt.Errorf("check multiport device: %w", err)
			}

			if deadline, ok := ctx.Deadline(); ok {
				if err := handle.SetSocketTimeout(
					max(deadline.Sub(clock.Real{}.Now()), time.Millisecond),
				); err != nil {
					slog.ErrorContext(
						ctx,
						"Cannot set devlink timeout",
						"device",
						device,
						"err",
						err,
					)

					return nil, fmt.Errorf("set devlink timeout: %w", err)
				}
			}

			return handle.DevlinkGetDeviceParamByName("pci", device, multiportParameter)
		},
	)
}

func checkMultiport(
	ports []ovsconfig.Port,
	sysClassNet string,
	read func(string) (*netlink.DevlinkParam, error),
) error {
	checked := make(map[string]bool)

	for _, port := range ports {
		var device string
		var err error
		switch physicalPortType(port.Type) {
		case docaPort:
			device, err = interfacePCIAddress(sysClassNet, port.Name)
		case dpdkPort:
			device, err = dpdkPCIAddress(optionMap(port.Options)["dpdk-devargs"])
		default:
			continue
		}

		if err != nil {
			slog.Error("Cannot resolve OVS port PCI device", "port", port.Name, "err", err)

			return fmt.Errorf("OVS port %q: %w", port.Name, err)
		}

		if checked[device] {
			continue
		}

		param, err := read(device)
		if err != nil {
			slog.Error(
				"Cannot read devlink parameter",
				"device",
				device,
				"parameter",
				multiportParameter,
				"err",
				err,
			)

			return fmt.Errorf(
				"read devlink pci/%s %s for port %q: %w",
				device,
				multiportParameter,
				port.Name,
				err,
			)
		}

		if !multiportEnabled(param) {
			slog.Error(
				"OVS port failed multiport check",
				"device",
				device,
				"port",
				port.Name,
				"err",
				errMultiportRuntime,
			)

			return fmt.Errorf(
				"OVS port %q, devlink pci/%s: %w",
				port.Name,
				device,
				errMultiportRuntime,
			)
		}

		checked[device] = true
	}

	if len(checked) == 0 {
		return errPhysicalPortRequired
	}

	return nil
}

func multiportEnabled(param *netlink.DevlinkParam) bool {
	if param == nil || param.Type != nl.DEVLINK_PARAM_TYPE_BOOL {
		return false
	}

	for _, value := range param.Values {
		if value.CMODE == nl.DEVLINK_PARAM_CMODE_RUNTIME {
			return value.Data == true
		}
	}

	return false
}

func interfacePCIAddress(sysClassNet, name string) (string, error) {
	if !filepath.IsLocal(name) || filepath.Base(name) != name || name == "." {
		slog.Error("Cannot resolve OVS interface", "name", name, "err", errPortInterfaceName)

		return "", fmt.Errorf("%w: %q", errPortInterfaceName, name)
	}

	device, err := filepath.EvalSymlinks(filepath.Join(sysClassNet, name, "device"))
	if err != nil {
		slog.Error("Cannot resolve interface device symlink", "interface", name, "err", err)

		return "", fmt.Errorf("resolve interface PCI device: %w", err)
	}

	for path := device; path != filepath.Dir(path); path = filepath.Dir(path) {
		if address := filepath.Base(path); pciAddress.MatchString(address) {
			return strings.ToLower(address), nil
		}
	}

	slog.Error("Interface lacks a PCI device", "interface", name, "err", errPortPCIDevice)

	return "", fmt.Errorf("%w: %q", errPortPCIDevice, name)
}

func dpdkPCIAddress(arguments string) (string, error) {
	bus, _, _ := strings.Cut(arguments, "/")
	address, options, _ := strings.Cut(bus, ",")
	if address == "bus=pci" {
		address = ""

		for option := range strings.SplitSeq(options, ",") {
			if candidate, found := strings.CutPrefix(option, "addr="); found {
				address = candidate

				break
			}
		}
	}

	address = strings.TrimPrefix(address, "pci:")
	if strings.Count(address, ":") == 1 {
		address = "0000:" + address
	}

	if !pciAddress.MatchString(address) {
		return "", errDPDKDevice
	}

	return strings.ToLower(address), nil
}

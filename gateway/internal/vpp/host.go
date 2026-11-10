package vpp

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"

	interfaces "go.fd.io/govpp/binapi/interface"
	"go.fd.io/govpp/binapi/interface_types"
	"go.fd.io/govpp/binapi/tapv2"
)

func (client *Client) preflightHosts(ctx context.Context, wanted map[string]TAP, observed map[string]*interfaces.SwInterfaceDetails) (failure error) {
	defer func() {
		if failure != nil {
			slog.ErrorContext(ctx, "inspect VPP TAP host ownership failed", "error", failure)
		}
	}()
	links, err := net.Interfaces()
	if err != nil {
		return fmt.Errorf("inspect Linux interfaces before VPP TAP creation: %w", err)
	}
	hosts := make(map[string]bool, len(links))
	for _, link := range links {
		hosts[link.Name] = true
	}
	stream, err := tapv2.NewServiceClient(client.connection).SwInterfaceTapV2Dump(ctx, &tapv2.SwInterfaceTapV2Dump{SwIfIndex: ^interface_types.InterfaceIndex(0)})
	taps, err := collect(stream, err)
	if err != nil {
		return fmt.Errorf("inspect VPP TAP host ownership: %w", err)
	}
	for name, resource := range wanted {
		entry, found := observed[name]
		if found && !strings.HasPrefix(entry.Tag, ownershipTagPrefix(resource.ConnectionID)) {
			return fmt.Errorf("VPP interface %s has a foreign ownership tag %q", name, entry.Tag)
		}
		if !hosts[resource.Create.HostIfName] {
			continue
		}
		owned := false
		for _, tap := range taps {
			if found && tap.SwIfIndex == uint32(entry.SwIfIndex) && tap.ID == resource.Create.ID && tap.HostIfName == resource.Create.HostIfName {
				owned = true
			}
		}
		if !owned {
			return fmt.Errorf("host interface %s already exists without the matching owned VPP TAP", resource.Create.HostIfName)
		}
	}
	return nil
}

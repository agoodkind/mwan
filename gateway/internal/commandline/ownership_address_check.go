package commandline

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/netip"
	"slices"

	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/networkjson"
	"goodkind.io/mwan/internal/networkload"
)

const (
	exitCheckOK           = 0
	exitCheckFailed       = 1
	exitCheckUnobservable = 2
	exitCheckUsage        = 64
	gatewayHostPrefixBits = 32
)

type gatewayCheckDeps struct {
	out             io.Writer
	loadNetwork     func() (*networkjson.Config, error)
	loadNetworkFrom func(string, string) (*networkjson.Config, error)
	listAddrs       func(context.Context, *slog.Logger, string) ([]netif.CurrentAddr, error)
}

func runCheckOwnedAddresses(ctx context.Context, output io.Writer) int {
	return checkOwnedAddresses(ctx, newGatewayCheckDeps(output))
}

func runCheckNetwork(path, schemaDir string, output io.Writer) int {
	return checkNetwork(newGatewayCheckDeps(output), path, schemaDir)
}

func newGatewayCheckDeps(output io.Writer) gatewayCheckDeps {
	return gatewayCheckDeps{
		out: output,
		loadNetwork: func() (*networkjson.Config, error) {
			return networkload.Load(networkjson.DefaultPath, networkjson.DefaultSchemaDir)
		},
		loadNetworkFrom: networkload.Load,
		listAddrs:       netif.ListAddrs,
	}
}

func checkOwnedAddresses(ctx context.Context, deps gatewayCheckDeps) int {
	log := slog.With("component", "mwan", "op", "checkOwnedAddresses")
	network, err := deps.loadNetwork()
	if err != nil {
		fmt.Fprintf(deps.out, "network configuration unreadable: %v\n", err)

		return exitCheckFailed
	}

	present := 0
	missing := 0

	for _, name := range slices.Sorted(maps.Keys(network.WAN)) {
		entry := network.WAN[name]
		policy := entry.TranslationV4
		if policy == nil || len(policy.StaticMappings) == 0 {
			continue
		}

		held, err := deps.listAddrs(ctx, log, entry.Iface)
		if err != nil {
			fmt.Fprintf(deps.out, "addresses on %s unreadable: %v\n", entry.Iface, err)

			return exitCheckFailed
		}

		externals := make([]netip.Addr, 0, len(policy.StaticMappings))
		for _, mapping := range policy.StaticMappings {
			externals = append(externals, mapping.External)
		}

		owned, err := netif.OnLinkMappedAddresses(held, externals)
		if err != nil {
			fmt.Fprintf(deps.out, "addresses on %s unreadable: %v\n", entry.Iface, err)

			return exitCheckFailed
		}

		for _, address := range owned {
			state := "missing"
			if holdsHostAddress(held, address) {
				state = "present"
				present++
			} else {
				missing++
			}

			fmt.Fprintf(deps.out, "owned address %s on %s: %s\n", address, entry.Iface, state)
		}
	}

	fmt.Fprintf(deps.out, "owned addresses: %d present, %d missing\n", present, missing)
	if missing > 0 {
		return exitCheckFailed
	}

	return exitCheckOK
}

func checkNetwork(deps gatewayCheckDeps, path string, schemaDir string) int {
	loaded, err := deps.loadNetworkFrom(path, schemaDir)
	if err != nil {
		fmt.Fprintf(deps.out, "network configuration rejected: %v\n", err)

		return exitCheckFailed
	}

	for _, rejected := range loaded.Rejected {
		fmt.Fprintf(
			deps.out,
			"provider entry rejected: interface %s: %v\n",
			rejected.Interface,
			rejected.Err,
		)
	}

	fmt.Fprintf(
		deps.out,
		"network configuration: %d providers, %d rejected\n",
		len(loaded.WAN),
		len(loaded.Rejected),
	)
	if len(loaded.Rejected) > 0 {
		return exitCheckFailed
	}

	return exitCheckOK
}

func holdsHostAddress(held []netif.CurrentAddr, address netip.Addr) bool {
	want := netip.PrefixFrom(address, gatewayHostPrefixBits).String()

	for _, current := range held {
		if current.CIDR == want {
			return true
		}
	}

	return false
}

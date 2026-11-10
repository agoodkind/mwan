package vpp

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"os"
	"strings"

	"go.fd.io/govpp/adapter"
	"go.fd.io/govpp/adapter/statsclient"
	"go.fd.io/govpp/api"
	interfaces "go.fd.io/govpp/binapi/interface"
	"go.fd.io/govpp/core"
)

type statisticName string

const (
	rxStatistic    statisticName = core.InterfaceStats_Rx
	txStatistic    statisticName = core.InterfaceStats_Tx
	dropsStatistic statisticName = core.InterfaceStats_Drops
)

// InterfaceCounters separates missing shared memory vectors from measured zeroes.
type InterfaceCounters struct {
	Generation     string
	Counters       api.InterfaceCounters
	RXAvailable    bool
	TXAvailable    bool
	DropsAvailable bool
}

// ReadInterfaceCounters rejects inconsistent API and shared memory identities.
func ReadInterfaceCounters(ctx context.Context, settings Endpoints) (result map[string]InterfaceCounters, failure error) {
	defer func() {
		if failure != nil {
			slog.ErrorContext(ctx, "read VPP interface counters failed", "error", failure)
		}
	}()
	if err := settings.Validate(); err != nil {
		return nil, err
	}
	generation, err := socketGeneration(ctx, settings.StatsSocket)
	if err != nil {
		return nil, err
	}
	apiClient, err := Open(ctx, settings, &interfaces.SwInterfaceDump{}, &interfaces.SwInterfaceDetails{})
	if err != nil {
		return nil, err
	}
	defer apiClient.Close()
	requestContext, cancel := apiClient.requestContext(ctx)
	defer cancel()
	identities, err := apiClient.Interfaces(requestContext)
	if err != nil {
		return nil, err
	}
	client := statsclient.NewStatsClient(settings.StatsSocket, statsclient.SetSocketRetryTimeout(settings.Timeout))
	if err := client.Connect(); err != nil {
		return nil, fmt.Errorf("connect VPP statistics %s: %w", settings.StatsSocket, err)
	}
	defer func() { _ = client.Disconnect() }()
	entries, err := client.DumpStats("^/if/names$", "^/if/rx$", "^/if/tx$", "^/if/drops$")
	if err != nil {
		return nil, fmt.Errorf("read VPP interface statistics %s: %w", settings.StatsSocket, err)
	}
	confirmed, err := apiClient.Interfaces(requestContext)
	if err != nil {
		return nil, err
	}
	return decodeInterfaceCounters(settings.StatsSocket, generation, entries, identities, confirmed)
}

func decodeInterfaceCounters(socket, generation string, entries []adapter.StatEntry, identities, confirmed map[string]*interfaces.SwInterfaceDetails) (map[string]InterfaceCounters, error) {
	names, err := interfaceNames(entries)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("VPP statistics %s does not provide interface names", socket)
	}
	result := make(map[string]InterfaceCounters, len(names))
	for index, rawName := range names {
		if uint64(index) > math.MaxUint32 {
			return nil, fmt.Errorf("VPP statistics index exceeds the binary API index width")
		}
		name := strings.TrimRight(string(rawName), "\x00")
		if name == "" {
			continue
		}
		identity, present := identities[name]
		after, stillPresent := confirmed[name]
		if !present || !stillPresent || identity.SwIfIndex != after.SwIfIndex || identity.Tag != after.Tag || uint32(identity.SwIfIndex) != uint32(index) {
			continue
		}
		result[name] = interfaceCounter(entries, index, identity, generation+"/"+identity.Tag)
	}
	return result, nil
}

func interfaceNames(entries []adapter.StatEntry) (adapter.NameStat, error) {
	for _, entry := range entries {
		if string(entry.Name) != core.InterfaceStats_Names {
			continue
		}
		names, ok := entry.Data.(adapter.NameStat)
		if !ok {
			return nil, fmt.Errorf("VPP interface name statistics have type %s", entry.Type)
		}
		return names, nil
	}
	return nil, nil
}

func interfaceCounter(entries []adapter.StatEntry, index int, identity *interfaces.SwInterfaceDetails, generation string) InterfaceCounters {
	value := InterfaceCounters{Generation: generation, Counters: api.InterfaceCounters{InterfaceIndex: uint32(identity.SwIfIndex), InterfaceName: identity.InterfaceName}, RXAvailable: false, TXAvailable: false, DropsAvailable: false}
	for _, entry := range entries {
		switch statisticName(entry.Name) {
		case rxStatistic:
			value.Counters.Rx, value.RXAvailable = combinedCounter(entry, index)
		case txStatistic:
			value.Counters.Tx, value.TXAvailable = combinedCounter(entry, index)
		case dropsStatistic:
			value.Counters.Drops, value.DropsAvailable = simpleCounter(entry, index)
		}
	}
	return value
}

func socketGeneration(ctx context.Context, path string) (result string, failure error) {
	defer func() {
		if failure != nil {
			slog.ErrorContext(ctx, "inspect VPP socket generation failed", "socket", path, "error", failure)
		}
	}()
	status, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("inspect VPP socket %s: %w", path, err)
	}
	return fmt.Sprintf("%s/%d", path, status.ModTime().UnixNano()), nil
}

func combinedCounter(entry adapter.StatEntry, index int) (api.InterfaceCounterCombined, bool) {
	var result api.InterfaceCounterCombined
	workers, ok := entry.Data.(adapter.CombinedCounterStat)
	if !ok || len(workers) == 0 {
		return result, false
	}
	for _, counters := range workers {
		if index >= len(counters) {
			return api.InterfaceCounterCombined{}, false
		}
		result.Packets += counters[index].Packets()
		result.Bytes += counters[index].Bytes()
	}
	return result, true
}

func simpleCounter(entry adapter.StatEntry, index int) (uint64, bool) {
	workers, ok := entry.Data.(adapter.SimpleCounterStat)
	if !ok || len(workers) == 0 {
		return 0, false
	}
	var result uint64
	for _, counters := range workers {
		if index >= len(counters) {
			return 0, false
		}
		result += uint64(counters[index])
	}
	return result, true
}

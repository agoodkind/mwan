package networkjson

import (
	"fmt"
	"net/netip"
	"strings"

	"goodkind.io/mwan/internal/connectionid"
)

func normalizeConnectionIDs(entries []ifaceEntry) (map[string]connectionid.ID, map[string]connectionid.ID, error) {
	ids := make(map[string]connectionid.ID, len(entries))
	explicit := make(map[string]connectionid.ID)
	seen := make(map[connectionid.ID]string, len(entries))
	for _, entry := range entries {
		provider := ""
		if entry.WAN != nil {
			provider = entry.WAN.Name
		}
		id := connectionid.Resolve(entry.ConnectionID, provider, entry.Name)
		if id == "" || strings.ContainsAny(id.String(), "'\"[]/") {
			return nil, nil, fmt.Errorf("interface %q has invalid connection-id %q", entry.Name, id)
		}
		if previous, exists := seen[id]; exists {
			return nil, nil, fmt.Errorf("connection-id %q is shared by interfaces %q and %q", id, previous, entry.Name)
		}
		seen[id] = entry.Name
		ids[entry.Name] = id
		if entry.ConnectionID != "" {
			explicit[entry.Name] = id
		}
	}
	return ids, explicit, nil
}

func checkMappedExternals(loaded *Config, names []string) error {
	mappedBy := make(map[netip.Addr]string, len(names))
	for _, name := range names {
		policy := loaded.WAN[name].TranslationV4
		if policy == nil {
			continue
		}
		for _, mapping := range policy.StaticMappings {
			if taken, seen := mappedBy[mapping.External]; seen {
				return fmt.Errorf("wan %s ipv4: static-mapping external %s is already mapped by wan %s",
					name, mapping.External, taken)
			}
			mappedBy[mapping.External] = name
		}
	}
	return nil
}

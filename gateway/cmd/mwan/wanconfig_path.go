package main

import (
	"time"

	"goodkind.io/mwan/internal/wanstate"
	"goodkind.io/mwan/internal/yangpub"
)

func pathVerdictItems(base string, verdict wanstate.PathVerdict) []yangpub.Item {
	var items []yangpub.Item
	if verdict.Dependency != "" {
		items = append(items, yangpub.Item{Path: base + "/dependency", Value: verdict.Dependency})
	}
	if verdict.Reason != "" {
		items = append(items, yangpub.Item{Path: base + "/reason", Value: verdict.Reason})
	}
	return items
}

func pathReadinessItems(familyBase string, path wanstate.PathReadiness) []yangpub.Item {
	if path.State == "" {
		return nil
	}
	base := familyBase + "/path-readiness"
	items := []yangpub.Item{
		{Path: base + "/state", Value: string(path.State)},
		{Path: base + "/since", Value: path.Since.UTC().Format(time.RFC3339Nano)},
	}
	items = append(items, pathVerdictItems(base, path.PathVerdict)...)
	for _, transition := range path.History {
		entry := base + "/transition[id='" + transition.ID + "']"
		items = append(items,
			yangpub.Item{Path: entry + "/id", Value: transition.ID},
			yangpub.Item{Path: entry + "/at", Value: transition.At.UTC().Format(time.RFC3339Nano)},
			yangpub.Item{Path: entry + "/current", Value: string(transition.Current.State)},
		)
		if transition.Previous != "" {
			items = append(items, yangpub.Item{Path: entry + "/previous", Value: string(transition.Previous)})
		}
		items = append(items, pathVerdictItems(entry, transition.Current)...)
	}
	return items
}

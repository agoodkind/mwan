package networkd

import (
	"goodkind.io/mwan/internal/connectionid"
	"goodkind.io/mwan/internal/interfaceintent"
)

// ObsoleteLinkFiles uses the complete render and pruning rules without changing the directory.
func ObsoleteLinkFiles(directory string, connections []interfaceintent.Connection, tables map[connectionid.ID]int) ([]string, error) {
	rendered, err := renderAll(connections, tables)
	if err != nil {
		return nil, err
	}
	obsolete, err := obsoleteFiles(directory, rendered)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, file := range obsolete {
		if file.Kind == FileLink {
			names = append(names, file.File)
		}
	}
	return names, nil
}

package commandline

import (
	"slices"

	"github.com/spf13/cobra"
)

func CommandPathsForTest() [][]string {
	started := false
	root := newRootCommand(&started, func(string) {})
	var paths [][]string
	var collect func(*cobra.Command, []string)
	collect = func(command *cobra.Command, path []string) {
		paths = append(paths, path)

		for _, child := range command.Commands() {
			collect(child, append(slices.Clone(path), child.Name()))
		}
	}
	collect(root, nil)

	return paths
}

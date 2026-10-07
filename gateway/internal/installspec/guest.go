package installspec

import (
	"path/filepath"
	"strings"

	"goodkind.io/mwan/internal/config"
)

const (
	// namespacedSysctlPrefix marks the keys a container can write. The keys
	// under net. belong to the container's network namespace.
	namespacedSysctlPrefix = "net."
	commentPrefix          = "#"
	blockSeparator         = "\n\n"
)

// IsSysctl reports true when the file's destination directory is SysctlDir.
func (file File) IsSysctl() bool {
	return filepath.Dir(file.Dest) == SysctlDir
}

// Content reads the embedded file. For an lxc guest, Content returns only the
// net. settings of a sysctl file. Each setting includes the comment lines
// above it. Content returns every other file and every qemu file unchanged.
// Content returns false as the second result when an lxc sysctl file has no
// net. setting. The caller then skips the file.
func (file File) Content(guest config.GuestType) ([]byte, bool, error) {
	content, err := Read(file.Embedded)
	if err != nil {
		return nil, false, err
	}
	if guest != config.GuestTypeLXC || !file.IsSysctl() {
		return content, true, nil
	}
	kept := namespacedSysctlSettings(content)
	return kept, kept != nil, nil
}

// namespacedSysctlSettings keeps the net. settings of a sysctl file, each with
// the comments above it. It returns nil when no setting remains.
func namespacedSysctlSettings(content []byte) []byte {
	var keptBlocks []string
	for block := range strings.SplitSeq(strings.TrimSpace(string(content)), blockSeparator) {
		var comments []string
		var settings []string
		for line := range strings.SplitSeq(block, "\n") {
			trimmed := strings.TrimSpace(line)
			switch {
			case strings.HasPrefix(trimmed, commentPrefix):
				comments = append(comments, trimmed)
			case strings.HasPrefix(trimmed, namespacedSysctlPrefix):
				settings = append(settings, trimmed)
			}
		}
		if len(settings) == 0 {
			continue
		}
		keptBlocks = append(keptBlocks, strings.Join(append(comments, settings...), "\n"))
	}
	if len(keptBlocks) == 0 {
		return nil
	}
	return []byte(strings.Join(keptBlocks, blockSeparator) + "\n")
}

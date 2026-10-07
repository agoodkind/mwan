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

func (file File) IsSysctl() bool {
	return filepath.Dir(file.Dest) == SysctlDir
}

// Content reads the embedded file. For an lxc guest, Content returns only the
// net. settings of a sysctl file, each with the comment lines directly above it.
// Content returns every other file and every qemu file unchanged.
// Content returns false as the second result when an lxc sysctl file has no
// net. setting. The caller removes or skips the file.
func (file File) Content(guest config.GuestType) ([]byte, bool, error) {
	content, err := Read(file.Embedded)
	if err != nil {
		return nil, false, err
	}
	if guest != config.GuestTypeLXC || !file.IsSysctl() {
		return content, true, nil
	}
	kept := NamespacedSysctlSettings(content)
	return kept, kept != nil, nil
}

// NamespacedSysctlSettings keeps each net. setting of sysctl file content with
// the comment lines directly above it. NamespacedSysctlSettings drops comments
// above dropped settings and comments at the end of a block.
// NamespacedSysctlSettings keeps blank-line separation between blocks that keep
// a setting. NamespacedSysctlSettings returns nil when no net. setting remains.
func NamespacedSysctlSettings(content []byte) []byte {
	var keptBlocks []string
	for block := range strings.SplitSeq(strings.TrimSpace(string(content)), blockSeparator) {
		var keptLines []string
		var pendingComments []string
		for line := range strings.SplitSeq(block, "\n") {
			trimmed := strings.TrimSpace(line)
			switch {
			case trimmed == "":
			case strings.HasPrefix(trimmed, commentPrefix):
				pendingComments = append(pendingComments, trimmed)
			case strings.HasPrefix(trimmed, namespacedSysctlPrefix):
				keptLines = append(keptLines, pendingComments...)
				keptLines = append(keptLines, trimmed)
				pendingComments = nil
			default:
				pendingComments = nil
			}
		}
		if len(keptLines) == 0 {
			continue
		}
		keptBlocks = append(keptBlocks, strings.Join(keptLines, "\n"))
	}
	if len(keptBlocks) == 0 {
		return nil
	}
	return []byte(strings.Join(keptBlocks, blockSeparator) + "\n")
}

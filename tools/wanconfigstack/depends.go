package main

import (
	"context"
	"debug/elf"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// libraryDirs are where an installed shared library can sit on the
// container: the distribution's library tree, and the stack's own.
var libraryDirs = []string{"/usr/lib", "/lib", stackPrefix}

var (
	errNoSharedLibrary = errors.New("no shared library in stage")
	errNoProvider      = errors.New("no installed library provides the soname")
	errNoOwner         = errors.New("no installed package owns the library")
)

// elfNeeded returns the sonames every ELF file under dir needs, with the
// sonames the tree provides itself removed. Non-ELF files (headers,
// pkg-config files) are skipped.
func (b *builder) elfNeeded(ctx context.Context, dir string) ([]string, error) {
	needed := map[string]bool{}
	provided := map[string]bool{}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return b.fail(ctx, "walk stage", walkErr, slog.String("path", path))
		}
		if entry.IsDir() || entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		file, err := elf.Open(path)
		if isNotELF(err) {
			return nil
		}
		if err != nil {
			return b.fail(ctx, "open ELF file", err, slog.String("path", path))
		}
		defer func() { _ = file.Close() }()
		libs, err := file.ImportedLibraries()
		if err != nil {
			return b.fail(ctx, "read imported libraries", err, slog.String("path", path))
		}
		for _, lib := range libs {
			needed[lib] = true
		}
		if file.Type == elf.ET_DYN {
			provided[entry.Name()] = true
			if sonames, err := file.DynString(elf.DT_SONAME); err == nil {
				for _, soname := range sonames {
					provided[soname] = true
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, b.fail(ctx, "walk stage", err, slog.String("dir", dir))
	}
	result := []string{}
	for soname := range needed {
		if !provided[soname] {
			result = append(result, soname)
		}
	}
	slices.Sort(result)
	b.log.InfoContext(ctx, "wanconfigstack: shared libraries needed", "dir", dir, "sonames", strings.Join(result, " "))
	return result, nil
}

// isNotELF reports whether [elf.Open] refused the file for its content: a
// format error for a file with a foreign header, or an EOF for a file too
// short to hold one. Either is a text or data file in the staged tree.
func isNotELF(err error) bool {
	var formatErr *elf.FormatError
	if errors.As(err, &formatErr) {
		return true
	}
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

// findLibrary locates an installed shared library by soname.
func (b *builder) findLibrary(ctx context.Context, soname string) (string, error) {
	for _, root := range libraryDirs {
		found := ""
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return b.fail(ctx, "walk library dir", walkErr, slog.String("path", path))
			}
			if found == "" && entry.Name() == soname {
				found = path
			}
			return nil
		})
		if err != nil {
			return "", b.fail(ctx, "walk library dir", err, slog.String("dir", root))
		}
		if found != "" {
			return found, nil
		}
	}
	return "", b.fail(ctx, "resolve shared library", errNoProvider, slog.String("soname", soname))
}

// shlibDepends returns the Depends entries for a staged tree: the package
// that owns each shared library its ELF files need. Packages from this
// build are pinned to their exact version; distribution packages are named
// without one, because the gateway and the container run the same release.
func (b *builder) shlibDepends(ctx context.Context, stage string, owners map[string]string) ([]string, error) {
	sonames, err := b.elfNeeded(ctx, stage)
	if err != nil {
		return nil, err
	}
	depends := []string{}
	for _, soname := range sonames {
		path, err := b.findLibrary(ctx, soname)
		if err != nil {
			return nil, err
		}
		pkg, ok := ownerOf(path, owners)
		if !ok {
			return nil, b.fail(ctx, "resolve library owner", errNoOwner, slog.String("soname", soname), slog.String("path", path))
		}
		if version, ok := b.versions[pkg]; ok {
			pkg = pkg + " (= " + version + ")"
		}
		if !slices.Contains(depends, pkg) {
			depends = append(depends, pkg)
		}
	}
	slices.Sort(depends)
	return depends, nil
}

// ownerOf names the package owning a library path, following the symlink
// chain a soname usually is (libfoo.so.1 -> libfoo.so.1.2.3) and the
// merged-/usr alias the path may carry.
func ownerOf(path string, owners map[string]string) (string, bool) {
	candidates := []string{path}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		candidates = append(candidates, resolved)
	}
	for _, candidate := range candidates {
		for _, alias := range []string{candidate, "/usr" + candidate, strings.TrimPrefix(candidate, "/usr")} {
			if pkg, ok := owners[alias]; ok {
				return pkg, true
			}
		}
	}
	return "", false
}

// stagedLibraryDir returns the directory under the stack prefix that holds
// the stage's shared libraries, as the gateway will see it.
func (b *builder) stagedLibraryDir(ctx context.Context, stage string) (string, error) {
	found := ""
	err := filepath.WalkDir(filepath.Join(stage, stackPrefix), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return b.fail(ctx, "walk stage", walkErr, slog.String("path", path))
		}
		if found == "" && !entry.IsDir() && strings.Contains(entry.Name(), ".so") {
			found = filepath.Dir(path)
		}
		return nil
	})
	if err != nil {
		return "", b.fail(ctx, "walk stage", err, slog.String("stage", stage))
	}
	if found == "" {
		return "", b.fail(ctx, "find staged library dir", errNoSharedLibrary, slog.String("stage", stage))
	}
	return strings.TrimPrefix(found, stage), nil
}

// writeLoaderConf writes the ld.so.conf.d entry into a stage, naming the
// staged library directory under the stack prefix.
func (b *builder) writeLoaderConf(ctx context.Context, stage string) error {
	libDir, err := b.stagedLibraryDir(ctx, stage)
	if err != nil {
		return err
	}
	path := filepath.Join(stage, loaderConfPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return b.fail(ctx, "create loader conf dir", err, slog.String("path", path))
	}
	if err := os.WriteFile(path, []byte(libDir+"\n"), 0o600); err != nil {
		return b.fail(ctx, "write loader conf", err, slog.String("path", path))
	}
	b.log.InfoContext(ctx, "wanconfigstack: loader conf staged", "path", loaderConfPath, "libdir", libDir)
	return nil
}

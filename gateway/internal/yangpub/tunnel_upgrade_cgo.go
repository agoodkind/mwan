package yangpub

/*
#cgo pkg-config: sysrepo libyang
#include "tunnel_upgrade.h"
*/
import "C"

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"

	"goodkind.io/mwan/gateway/internal/installfile"
)

const (
	tunnelOptionsRevision = "2026-11-27"
	legacyTunnelPath      = "/ietf-interfaces:interfaces/interface/goodkind-mwan-steering:link/tunnel/vxlan"
	migrationFileMode     = 0o600
)

var errTunnelLeaf = errors.New("unexpected sysrepo tunnel option path")

func lockSchemaInstall(repo string) (*os.File, error) {
	dir, err := os.Open(repo)
	if err != nil {
		return nil, fmt.Errorf("open sysrepo repository: %w", err)
	}
	if err := unix.Flock(int(dir.Fd()), unix.LOCK_EX); err != nil {
		_ = dir.Close()
		slog.Error("Cannot lock the schema upgrade", "repository", repo, "err", err)

		return nil, fmt.Errorf("lock sysrepo repository: %w", err)
	}

	return dir, nil
}

func tunnelMigrationPath(ds Datastore) string {
	return filepath.Join(RepositoryPath(), "mwan-vxlan-options-"+string(ds)+".json")
}

func (p *publisher) updateInstalledModel(
	ctx context.Context,
	conn *C.sr_conn_ctx_t,
	model Model,
	search *C.char,
	name string,
	installed string,
	revision string,
) error {
	migrate := name == "goodkind-mwan-steering" && revision >= tunnelOptionsRevision
	if installed != revision {
		var err error
		if migrate && installed < tunnelOptionsRevision && hasLegacyTunnel(conn) {
			err = p.updateTunnelSchema(ctx, conn, model, search)
		} else {
			err = updateModule(conn, model, search)
		}
		if err != nil {
			return err
		}
	}

	if migrate {
		return p.restoreTunnelOptions(ctx)
	}

	return nil
}

func hasLegacyTunnel(conn *C.sr_conn_ctx_t) bool {
	path := C.CString(legacyTunnelPath)
	var present C.int
	C.has_legacy_tunnel(conn, path, &present)
	C.free(unsafe.Pointer(path))

	return present != 0
}

// Sysrepo releases datastore locks when it replaces the schema.
func (p *publisher) updateTunnelSchema(
	ctx context.Context,
	conn *C.sr_conn_ctx_t,
	model Model,
	search *C.char,
) error {
	for _, datastore := range []Datastore{DatastoreRunning, DatastoreStartup} {
		session, err := p.lockTunnelStore(ctx, datastore)
		if err != nil {
			return err
		}
		defer stopTunnelSession(session)

		items, err := p.legacyTunnelItems(ctx, session, datastore)
		if err != nil {
			return err
		}

		path := tunnelMigrationPath(datastore)
		if len(items) == 0 {
			if err := removeTunnelJournal(path); err != nil {
				return err
			}

			continue
		}

		data, err := json.Marshal(items)
		if err != nil {
			return fmt.Errorf("encode tunnel options for %s: %w", datastore, err)
		}
		if _, err := installfile.Write(path, data, migrationFileMode); err != nil {
			p.log.ErrorContext(
				ctx,
				"Cannot save tunnel options before schema update",
				"datastore",
				datastore,
				"err",
				err,
			)

			return fmt.Errorf("save tunnel options for %s: %w", datastore, err)
		}
	}
	if err := syncTunnelJournalDir(); err != nil {
		return err
	}

	return updateModule(conn, model, search)
}

func (p *publisher) lockTunnelStore(
	ctx context.Context,
	datastore Datastore,
) (*C.sr_session_ctx_t, error) {
	store, err := datastoreOf(datastore)
	if err != nil {
		return nil, err
	}
	session, err := p.startSession(ctx, store)
	if err != nil {
		return nil, err
	}

	var result C.int
	C.lock_tunnel_interfaces(session, C.uint32_t(applyTimeoutMS), &result)
	if srFailed(result) {
		stopTunnelSession(session)
		err := srError("lock interfaces in "+string(datastore), result)
		p.log.ErrorContext(
			ctx,
			"Cannot lock interfaces for tunnel migration",
			"datastore",
			datastore,
			"err",
			err,
		)

		return nil, err
	}

	return session, nil
}

func (p *publisher) legacyTunnelItems(
	ctx context.Context,
	session *C.sr_session_ctx_t,
	datastore Datastore,
) ([]Item, error) {
	query := C.CString(legacyTunnelPath + "/*")
	options := (*C.struct_tunnel_options)(C.malloc(C.sizeof_struct_tunnel_options))
	C.read_tunnel_options(session, query, C.uint32_t(getTimeoutMS), options)
	C.free(unsafe.Pointer(query))
	defer func() { C.free_tunnel_options(options) }()
	if srFailed(options.result) {
		err := srError("read existing VXLAN settings", options.result)
		p.log.ErrorContext(
			ctx,
			"Cannot read existing VXLAN settings",
			"datastore",
			datastore,
			"err",
			err,
		)

		return nil, err
	}
	items := make([]Item, 0, options.count)
	results := unsafe.Slice(options.values, options.count)
	for index := range results {
		value := &results[index]
		path := C.GoString(value.xpath)
		parent, leaf, found := strings.CutLast(path, "/vxlan/")
		if !found {
			return nil, fmt.Errorf("%w: %s", errTunnelLeaf, path)
		}
		C.format_tunnel_value(options, C.size_t(index))
		if options.text == nil {
			return nil, fmt.Errorf("%w: cannot read %s", errTunnelLeaf, path)
		}
		optionPath := parent + "/options[name='" + leaf + "']/value"
		items = append(items, Item{Path: optionPath, Value: C.GoString(options.text)})
	}

	return items, nil
}

func stopTunnelSession(session *C.sr_session_ctx_t) {
	C.finish_tunnel_session(session)
}

// Restarting an interrupted install must restore pending values even when
// sysrepo already reports the new revision.
func (p *publisher) restoreTunnelOptions(ctx context.Context) error {
	for _, datastore := range []Datastore{DatastoreRunning, DatastoreStartup} {
		path := tunnelMigrationPath(datastore)
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("read pending tunnel options for %s: %w", datastore, err)
		}

		var items []Item
		if err := json.Unmarshal(data, &items); err != nil {
			p.log.ErrorContext(
				ctx,
				"Cannot decode saved VXLAN options",
				"datastore",
				datastore,
				"err",
				err,
			)

			return fmt.Errorf("decode pending tunnel options for %s: %w", datastore, err)
		}
		if err := p.restoreMissingTunnelItems(ctx, datastore, items); err != nil {
			return err
		}
		if err := removeTunnelJournal(path); err != nil {
			return err
		}
		if err := syncTunnelJournalDir(); err != nil {
			return err
		}
	}

	return nil
}

func (p *publisher) restoreMissingTunnelItems(
	ctx context.Context,
	datastore Datastore,
	items []Item,
) error {
	session, err := p.lockTunnelStore(ctx, datastore)
	if err != nil {
		return err
	}
	defer stopTunnelSession(session)

	var missing []Item
	for _, item := range items {
		parent, _, valid := strings.CutLast(item.Path, "/options[")
		if !valid {
			err := fmt.Errorf("%w: %s", errTunnelLeaf, item.Path)
			p.log.ErrorContext(ctx, "Invalid tunnel option in saved migration", "err", err)

			return err
		}
		protocol, _, err := p.GetItem(ctx, datastore, parent+"/protocol")
		if err != nil {
			return err
		}
		if protocol != "vxlan" {
			continue
		}
		_, exists, err := p.GetItem(ctx, datastore, item.Path)
		if err != nil {
			return err
		}
		if !exists {
			missing = append(missing, item)
		}
	}

	if len(missing) == 0 {
		return nil
	}

	return applyEdits(ctx, p.log, session, nil, missing)
}

func removeTunnelJournal(path string) error {
	err := os.Remove(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Error("Cannot remove saved tunnel options", "path", path, "err", err)

		return fmt.Errorf("remove tunnel migration journal: %w", err)
	}

	return nil
}

func syncTunnelJournalDir() error {
	dir, err := os.Open(RepositoryPath())
	if err != nil {
		return fmt.Errorf("open sysrepo repository: %w", err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		slog.Error("Cannot sync tunnel migration journals", "err", err)

		return fmt.Errorf("sync sysrepo repository: %w", err)
	}

	return nil
}

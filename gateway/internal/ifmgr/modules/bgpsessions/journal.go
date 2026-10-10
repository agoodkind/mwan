package bgpsessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"

	"github.com/google/renameio/v2"
)

const journalFileMode = 0o600

type routeOwner struct {
	Interface string `json:"interface"`
	Metric    int    `json:"metric"`
}

type journalDocument struct {
	Owners []routeOwner `json:"owners"`
}

func sessionOwner(cfg Session) routeOwner {
	return routeOwner{Interface: cfg.Iface, Metric: int(cfg.Config.RouteMetric)}
}

func loadJournal(path string) ([]routeOwner, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		slog.Error("bgp_sessions: journal read failed", "path", path, "err", err)
		return nil, fmt.Errorf("read the session route journal %s: %w", path, err)
	}
	var document journalDocument
	if err := json.Unmarshal(data, &document); err != nil {
		slog.Error("bgp_sessions: journal decode failed", "path", path, "err", err)
		return nil, fmt.Errorf("decode the session route journal %s: %w", path, err)
	}
	return document.Owners, nil
}

func (m *Module) persistOwners(ctx context.Context, log *slog.Logger, owners []routeOwner) error {
	data, err := json.Marshal(journalDocument{Owners: owners})
	if err != nil {
		log.ErrorContext(ctx, "bgp_sessions: journal encode failed", "err", err)
		return fmt.Errorf("encode the session route journal: %w", err)
	}
	if err := renameio.WriteFile(m.cfg.StateFile, data, journalFileMode); err != nil {
		log.ErrorContext(ctx, "bgp_sessions: journal write failed", "path", m.cfg.StateFile, "err", err)
		return fmt.Errorf("write the session route journal %s: %w", m.cfg.StateFile, err)
	}
	m.owners = owners
	return nil
}

func (m *Module) recordOwner(ctx context.Context, log *slog.Logger, owner routeOwner) error {
	if slices.Contains(m.owners, owner) {
		return nil
	}
	return m.persistOwners(ctx, log, append(slices.Clone(m.owners), owner))
}

func (m *Module) releaseRemovedOwners(ctx context.Context, log *slog.Logger) error {
	configured := make([]routeOwner, 0, len(m.sessions))
	for _, entry := range m.sessions {
		configured = append(configured, sessionOwner(entry.cfg))
	}
	retained := slices.DeleteFunc(slices.Clone(m.owners), func(owner routeOwner) bool {
		return !slices.Contains(configured, owner)
	})
	if len(retained) == len(m.owners) {
		return nil
	}
	return m.persistOwners(ctx, log, retained)
}

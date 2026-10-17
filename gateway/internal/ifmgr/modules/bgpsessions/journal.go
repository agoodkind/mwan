package bgpsessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"time"

	"github.com/google/renameio/v2"
)

const journalFileMode = 0o600

type routeOwner struct {
	Interface string `json:"interface"`
	Metric    int    `json:"metric"`
}

// retention records a session that StopRetaining stopped. The kernel routes of
// the owner stay until the deadline, which is the end of the restart time that
// the peer applies.
type retention struct {
	routeOwner

	Deadline time.Time `json:"deadline"`
}

type journalDocument struct {
	Owners    []routeOwner `json:"owners"`
	Retaining []retention  `json:"retaining,omitempty"`
}

func sessionOwner(cfg Session) routeOwner {
	return routeOwner{Interface: cfg.Iface, Metric: int(cfg.Config.RouteMetric)}
}

func loadJournal(path string) (journalDocument, error) {
	var document journalDocument
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return document, nil
	}
	if err != nil {
		slog.Error("bgp_sessions: journal read failed", "path", path, "err", err)
		return document, fmt.Errorf("read the session route journal %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &document); err != nil {
		slog.Error("bgp_sessions: journal decode failed", "path", path, "err", err)
		return journalDocument{Owners: nil, Retaining: nil}, fmt.Errorf("decode the session route journal %s: %w", path, err)
	}
	return document, nil
}

func (m *Module) persistJournal(ctx context.Context, log *slog.Logger, document journalDocument) error {
	data, err := json.Marshal(document)
	if err != nil {
		log.ErrorContext(ctx, "bgp_sessions: journal encode failed", "err", err)
		return fmt.Errorf("encode the session route journal: %w", err)
	}
	if err := renameio.WriteFile(m.cfg.StateFile, data, journalFileMode); err != nil {
		log.ErrorContext(ctx, "bgp_sessions: journal write failed", "path", m.cfg.StateFile, "err", err)
		return fmt.Errorf("write the session route journal %s: %w", m.cfg.StateFile, err)
	}
	m.owners = document.Owners
	m.retaining = document.Retaining
	return nil
}

func (m *Module) persistOwners(ctx context.Context, log *slog.Logger, owners []routeOwner) error {
	return m.persistJournal(ctx, log, journalDocument{Owners: owners, Retaining: m.retaining})
}

func (m *Module) persistRetaining(ctx context.Context, log *slog.Logger, retaining []retention) error {
	return m.persistJournal(ctx, log, journalDocument{Owners: m.owners, Retaining: retaining})
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

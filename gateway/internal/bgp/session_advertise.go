package bgp

import (
	"errors"
	"fmt"

	"github.com/osrg/gobgp/v4/pkg/apiutil"
)

// SetAdvertisement originates or withdraws the export prefixes.
// SetAdvertisement originates ExportAlways prefixes while eligible is true.
// SetAdvertisement originates ExportBackup prefixes while eligible and
// backupActive are both true. SetAdvertisement makes no GoBGP request for a
// prefix already in the wanted state.
func (s *Session) SetAdvertisement(eligible, backupActive bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started {
		return fmt.Errorf("bgp session %q is not started", s.cfg.Name)
	}

	var reconcileErr error
	for _, rule := range s.cfg.Export {
		wanted := exportAdvertised(rule.Mode, eligible, backupActive)
		_, advertised := s.advertised[rule.Prefix]
		if wanted == advertised {
			continue
		}
		if err := s.reconcileExportLocked(rule, wanted); err != nil {
			reconcileErr = errors.Join(reconcileErr, err)
		}
	}
	return reconcileErr
}

func (s *Session) reconcileExportLocked(rule ExportRule, wanted bool) error {
	if !wanted {
		path, err := withdrawalPath(s.log, rule.Prefix)
		if err != nil {
			return err
		}
		request := apiutil.DeletePathRequest{Paths: []*apiutil.Path{path}}
		if err := s.server.DeletePath(request); err != nil {
			s.log.Error("withdraw bgp session prefix failed", "prefix", rule.Prefix, "error", err)
			return fmt.Errorf("withdraw %s: %w", rule.Prefix, err)
		}
		delete(s.advertised, rule.Prefix)
		return nil
	}

	path, err := exportPath(s.log, s.cfg, rule)
	if err != nil {
		return err
	}
	request := apiutil.AddPathRequest{Paths: []*apiutil.Path{path}}
	if _, err := s.server.AddPath(request); err != nil {
		s.log.Error("advertise bgp session prefix failed", "prefix", rule.Prefix, "error", err)
		return fmt.Errorf("advertise %s: %w", rule.Prefix, err)
	}
	s.advertised[rule.Prefix] = struct{}{}
	return nil
}

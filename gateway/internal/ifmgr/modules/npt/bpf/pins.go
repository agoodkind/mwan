//go:build linux

package bpf

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/cilium/ebpf"
)

const (
	defaultPinDirectory = "/sys/fs/bpf/mwan/npt"
	pinDirectoryMode    = 0o700
	pinIngressName      = "npt_ingress"
	pinEgressName       = "npt_egress"
	pinPoliciesName     = "policies"
)

type pinTarget struct {
	path string
	pin  func(string) error
}

func openPinnedObjects(directory string) (objects *nptObjects, found bool, resultErr error) {
	defer func() {
		if resultErr != nil {
			slog.Warn("NPTv6 pinned objects could not be opened", "directory", directory, "err", resultErr)
		}
	}()
	var opened nptObjects
	var err error
	opened.NptIngress, err = ebpf.LoadPinnedProgram(filepath.Join(directory, pinIngressName), nil)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("open pinned %s: %w", pinIngressName, err)
	}
	opened.NptEgress, err = ebpf.LoadPinnedProgram(filepath.Join(directory, pinEgressName), nil)
	if err != nil {
		closeErr := opened.Close()
		return nil, false, errors.Join(fmt.Errorf("open pinned %s: %w", pinEgressName, err), closeErr)
	}
	opened.Policies, err = ebpf.LoadPinnedMap(filepath.Join(directory, pinPoliciesName), nil)
	if err != nil {
		closeErr := opened.Close()
		return nil, false, errors.Join(fmt.Errorf("open pinned %s: %w", pinPoliciesName, err), closeErr)
	}
	return &opened, true, nil
}

func validatePinnedObjects(previous *nptObjects, expected *ebpf.MapInfo) (resultErr error) {
	defer func() {
		if resultErr != nil {
			slog.Warn("NPTv6 pinned objects failed validation", "err", resultErr)
		}
	}()
	programs := map[string]*ebpf.Program{pinIngressName: previous.NptIngress, pinEgressName: previous.NptEgress}
	for name, program := range programs {
		info, err := program.Info()
		if err != nil {
			return fmt.Errorf("inspect pinned %s: %w", name, err)
		}
		if info.Type != ebpf.SchedCLS || info.Name != name {
			return fmt.Errorf("pinned %s is a %s program named %q", name, info.Type, info.Name)
		}
	}
	info, err := previous.Policies.Info()
	if err != nil {
		return fmt.Errorf("inspect pinned %s: %w", pinPoliciesName, err)
	}
	if info.Name != expected.Name || info.Type != expected.Type || info.KeySize != expected.KeySize || info.ValueSize != expected.ValueSize {
		return fmt.Errorf("pinned %s has a different schema", pinPoliciesName)
	}
	return nil
}

// adoptPinnedObjects opens existing pins as the previous generation.
// A pin inspection failure logs a warning without stopping translator creation
// or the wan role.
func (translator *Translator) adoptPinnedObjects() {
	previous, found, err := openPinnedObjects(translator.pinDirectory)
	if err != nil || !found {
		return
	}
	expected, err := translator.objects.Policies.Info()
	if err == nil {
		err = validatePinnedObjects(previous, expected)
	}
	if err != nil {
		slog.Warn("NPTv6 pinned objects from an earlier start were discarded", "directory", translator.pinDirectory)
		if closeErr := previous.Close(); closeErr != nil {
			slog.Warn("NPTv6 discarded pinned objects could not be closed", "err", closeErr)
		}
		return
	}
	translator.previous = previous
}

// Previous pins must remain available for edge verification until the first
// fully successful Reconcile. Each translator attempts replacement once.
// A pin replacement failure logs a warning without stopping the wan role.
func (translator *Translator) replacePins() {
	if translator.pinsSettled {
		return
	}
	translator.pinsSettled = true
	if err := translator.pinObjects(); err != nil {
		return
	}
	if err := translator.closePrevious(); err != nil {
		slog.Warn("NPTv6 previous pinned objects could not be closed", "err", err)
	}
}

func (translator *Translator) pinObjects() (resultErr error) {
	defer func() {
		if resultErr != nil {
			slog.Warn("NPTv6 pin replacement failed", "directory", translator.pinDirectory, "err", resultErr)
		}
	}()
	if err := os.MkdirAll(translator.pinDirectory, pinDirectoryMode); err != nil {
		return fmt.Errorf("create pin directory: %w", err)
	}
	targets := []pinTarget{
		{filepath.Join(translator.pinDirectory, pinIngressName), translator.objects.NptIngress.Pin},
		{filepath.Join(translator.pinDirectory, pinEgressName), translator.objects.NptEgress.Pin},
		{filepath.Join(translator.pinDirectory, pinPoliciesName), translator.objects.Policies.Pin},
	}
	for _, target := range targets {
		if err := os.Remove(target.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove earlier pin: %w", err)
		}
	}
	for _, target := range targets {
		if err := target.pin(target.path); err != nil {
			return errors.Join(fmt.Errorf("pin %s: %w", target.path, err), removePins(targets))
		}
	}
	return nil
}

func removePins(targets []pinTarget) (resultErr error) {
	defer func() {
		if resultErr != nil {
			slog.Warn("NPTv6 partial pins could not be removed", "err", resultErr)
		}
	}()
	var failures []error
	for _, target := range targets {
		if err := os.Remove(target.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (translator *Translator) closePrevious() error {
	if translator.previous == nil {
		return nil
	}
	previous := translator.previous
	translator.previous = nil
	return previous.Close()
}

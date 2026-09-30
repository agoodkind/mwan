package resolved

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"

	"github.com/vishvananda/netlink"
	"goodkind.io/mwan/internal/installfile"
	"goodkind.io/mwan/internal/netif"
)

// Intent supplies one configured connection and its current ready link.
type Intent struct {
	ConnectionID string
	Name         string
	Index        int
	Ready        bool
	DNS          []DNS
	Domains      []Domain
}

type resolverValue interface{ DNS | Domain }

type field[T resolverValue] struct {
	Original []T  `json:"original"`
	Pending  *[]T `json:"pending,omitempty"`
	Verified *[]T `json:"verified,omitempty"`
}

type record struct {
	Name     string         `json:"name"`
	Index    int            `json:"index"`
	Identity string         `json:"identity"`
	DNS      *field[DNS]    `json:"dns,omitempty"`
	Domains  *field[Domain] `json:"domains,omitempty"`
}

type journal struct {
	Version int                `json:"version"`
	BootID  string             `json:"boot_id"`
	Links   map[string]*record `json:"links"`
}

// Ownership journals each resolver field before changing resolved.
type Ownership struct {
	mu    sync.Mutex
	path  string
	state journal
}

// NewOwnership loads an absolute journal path without changing resolver settings.
func NewOwnership(path string) (*Ownership, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("resolved journal path must be absolute: %q", path)
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return nil, failure("read boot identity", err)
	}
	owner := &Ownership{mu: sync.Mutex{}, path: path, state: journal{Version: 1, BootID: strings.TrimSpace(string(boot)), Links: map[string]*record{}}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return owner, nil
	}
	if err != nil {
		return nil, failure("read resolver journal", err)
	}
	var saved journal
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&saved); err != nil {
		return nil, failure("decode resolver journal", err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("resolver journal contains multiple JSON values")
		}
		return nil, failure("decode resolver journal remainder", err)
	}
	if saved.Version != 1 || saved.BootID == "" || saved.Links == nil {
		return nil, fmt.Errorf("invalid resolver journal metadata")
	}
	for id, value := range saved.Links {
		if id == "" || value == nil || value.Index <= 0 || value.Name == "" || value.Identity == "" {
			return nil, fmt.Errorf("invalid resolver journal link %q", id)
		}
	}
	if saved.BootID == owner.state.BootID {
		owner.state = saved
	}
	return owner, nil
}

func (o *Ownership) save() error {
	data, err := json.Marshal(o.state)
	if err != nil {
		return failure("encode resolver journal", err)
	}
	if _, err := installfile.Write(o.path, append(data, '\n'), 0o600); err != nil {
		return failure("write resolver journal", err)
	}
	directory, err := os.Open(filepath.Dir(o.path))
	if err != nil {
		return failure("open resolver journal directory", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return failure("sync resolver journal directory", err)
	}
	return nil
}

func (o *Ownership) verify(value *record) error {
	link, err := netlink.LinkByIndex(value.Index)
	if err != nil {
		return failure("observe resolver link", err)
	}
	if link.Attrs().Name != value.Name || !netif.LinkMatchesIdentity(link, value.Index, value.Identity) {
		return fmt.Errorf("resolver link %s identity changed", value.Name)
	}
	return nil
}

// Reconcile applies static intent and releases fields absent from the current configuration.
func (o *Ownership) Reconcile(ctx context.Context, intents []Intent) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	wanted := make(map[string]Intent, len(intents))
	for _, intent := range intents {
		wanted[intent.ConnectionID] = intent
	}
	if err := o.retireMissingLinks(); err != nil {
		return err
	}
	if len(o.state.Links) == 0 && !hasSettings(intents) {
		return nil
	}
	c, err := openClient()
	if err != nil {
		return err
	}
	defer c.connection.Close()
	var failures []error
	for id, value := range o.state.Links {
		intent, configured := wanted[id]
		if configured && !intent.Ready {
			continue
		}
		if configured && (value.Index != intent.Index || value.Name != intent.Name) {
			intent.DNS = nil
			intent.Domains = nil
		}
		if err := o.applyRecord(ctx, c, value, intent); err != nil {
			failures = append(failures, fmt.Errorf("connection %s: %w", id, err))
		}
		if value.DNS == nil && value.Domains == nil {
			delete(o.state.Links, id)
		}
	}
	for id, intent := range wanted {
		if !intent.Ready || len(intent.DNS)+len(intent.Domains) == 0 || o.state.Links[id] != nil {
			continue
		}
		link, err := netlink.LinkByIndex(intent.Index)
		if err != nil {
			failures = append(failures, failure("observe configured resolver link", err))
			continue
		}
		if link.Attrs().Name != intent.Name {
			failures = append(failures, fmt.Errorf("connection %s resolver link changed", id))
			continue
		}
		value := &record{Name: intent.Name, Index: intent.Index, Identity: netif.LinkOwnershipIdentity(link), DNS: nil, Domains: nil}
		o.state.Links[id] = value
		if err := o.applyRecord(ctx, c, value, intent); err != nil {
			failures = append(failures, fmt.Errorf("connection %s: %w", id, err))
		}
	}
	if err := o.save(); err != nil {
		failures = append(failures, err)
	}
	if err := errors.Join(failures...); err != nil {
		return failure("reconcile resolver fields", err)
	}
	return nil
}

func (o *Ownership) retireMissingLinks() error {
	modified := false
	for id, value := range o.state.Links {
		link, err := netlink.LinkByIndex(value.Index)
		var absent netlink.LinkNotFoundError
		if errors.As(err, &absent) {
			delete(o.state.Links, id)
			modified = true
			continue
		}
		if err != nil {
			return failure("observe journaled resolver link", err)
		}
		if link.Attrs().Name != value.Name || !netif.LinkMatchesIdentity(link, value.Index, value.Identity) {
			delete(o.state.Links, id)
			modified = true
		}
	}
	if modified {
		return o.save()
	}
	return nil
}

func (o *Ownership) applyRecord(ctx context.Context, c *client, value *record, intent Intent) error {
	dnsErr := reconcileField(o, value, &value.DNS, intent.DNS, func() ([]DNS, error) { return c.dns(ctx, value.Index) }, func(values []DNS) error { return c.setDNS(ctx, value.Index, values) })
	domainsErr := reconcileField(o, value, &value.Domains, intent.Domains, func() ([]Domain, error) { return c.domains(ctx, value.Index) }, func(values []Domain) error { return c.setDomains(ctx, value.Index, values) })
	if err := errors.Join(dnsErr, domainsErr); err != nil {
		slog.WarnContext(ctx, "resolved: link apply failed", "interface", value.Name, "err", err)
		return fmt.Errorf("resolver fields: %w", err)
	}
	return nil
}

func hasSettings(intents []Intent) bool {
	for _, intent := range intents {
		if intent.Ready && len(intent.DNS)+len(intent.Domains) > 0 {
			return true
		}
	}
	return false
}

func equalValues[T resolverValue](left, right []T) bool {
	return len(left) == len(right) && (len(left) == 0 || reflect.DeepEqual(left, right))
}

func reconcileField[T resolverValue](owner *Ownership, link *record, saved **field[T], wanted []T, read func() ([]T, error), write func([]T) error) error {
	if len(wanted) == 0 && *saved == nil {
		return nil
	}
	releasing := len(wanted) == 0
	current, err := read()
	if err != nil {
		return err
	}
	if len(wanted) == 0 {
		state := *saved
		owned := state.Verified != nil && equalValues(current, *state.Verified) || state.Pending != nil && equalValues(current, *state.Pending)
		if !owned {
			*saved = nil
			if err := owner.save(); err != nil {
				return err
			}
			return fmt.Errorf("external resolver change preserved on %s", link.Name)
		}
		wanted = state.Original
	} else if *saved == nil {
		*saved = &field[T]{Original: current, Pending: nil, Verified: nil}
	}
	state := *saved
	if !equalValues(current, wanted) {
		if err := writeField(owner, link, state, wanted, read, write); err != nil {
			return err
		}
	}
	if releasing {
		*saved = nil
	} else {
		state.Verified = &wanted
		state.Pending = nil
	}
	return owner.save()
}

func writeField[T resolverValue](owner *Ownership, link *record, state *field[T], wanted []T, read func() ([]T, error), write func([]T) error) error {
	state.Pending = &wanted
	if err := owner.save(); err != nil {
		return err
	}
	if err := owner.verify(link); err != nil {
		return err
	}
	if err := write(wanted); err != nil {
		return err
	}
	observed, err := read()
	if err != nil {
		return err
	}
	if !equalValues(observed, wanted) {
		return fmt.Errorf("resolved readback differs on %s", link.Name)
	}
	return nil
}

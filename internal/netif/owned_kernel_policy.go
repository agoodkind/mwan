package netif

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"

	"github.com/vishvananda/netlink"
	"goodkind.io/mwan/internal/installfile"
	"goodkind.io/mwan/internal/interfaceintent"
)

type ownedKernelField struct {
	ConnectionID string  `json:"connection_id"`
	LinkIndex    int     `json:"link_index"`
	LinkIdentity string  `json:"link_identity"`
	Family       string  `json:"family"`
	Leaf         string  `json:"leaf"`
	Original     string  `json:"original"`
	Pending      *string `json:"pending,omitempty"`
	Verified     *string `json:"verified,omitempty"`
}

type ownedKernelJournal struct {
	BootID string             `json:"boot_id"`
	Fields []ownedKernelField `json:"fields"`
}

type kernelPolicySetting struct {
	family string
	leaf   string
	value  string
}

// OwnedKernelPolicyReconciler records prior values before changing per-link policy.
type OwnedKernelPolicyReconciler struct {
	mu      sync.Mutex
	path    string
	sysctl  SysctlRunner
	journal ownedKernelJournal
}

// NewOwnedKernelPolicyReconciler discards records from previous boots without changing kernel policy.
func NewOwnedKernelPolicyReconciler(path string, sysctl SysctlRunner) (*OwnedKernelPolicyReconciler, error) {
	if sysctl == nil {
		return nil, errors.New("kernel policy requires sysctl access")
	}
	journal, err := loadOwnedKernelPolicyJournal(path)
	if err != nil {
		return nil, err
	}
	return &OwnedKernelPolicyReconciler{mu: sync.Mutex{}, path: path, sysctl: sysctl, journal: journal}, nil
}

// ValidateOwnedKernelPolicyJournal checks persistent state before startup writes network configuration.
func ValidateOwnedKernelPolicyJournal(path string) error {
	_, err := loadOwnedKernelPolicyJournal(path)
	return err
}

func loadOwnedKernelPolicyJournal(path string) (ownedKernelJournal, error) {
	if !filepath.IsAbs(path) {
		return ownedKernelJournal{}, errors.New("kernel policy requires an absolute journal path")
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		slog.Warn("kernel policy boot ID read failed", "err", err)
		return ownedKernelJournal{}, fmt.Errorf("read kernel policy boot ID: %w", err)
	}
	bootID := string(boot)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ownedKernelJournal{BootID: bootID, Fields: nil}, nil
	}
	if err != nil {
		slog.Warn("kernel policy journal read failed", "path", path, "err", err)
		return ownedKernelJournal{}, fmt.Errorf("read kernel policy journal: %w", err)
	}
	journal := ownedKernelJournal{BootID: bootID, Fields: nil}
	if err := json.Unmarshal(data, &journal); err != nil {
		slog.Warn("kernel policy journal decode failed", "path", path, "err", err)
		return ownedKernelJournal{}, fmt.Errorf("decode kernel policy journal: %w", err)
	}
	return kernelPolicyJournalForBoot(journal, bootID), nil
}

func kernelPolicyJournalForBoot(journal ownedKernelJournal, bootID string) ownedKernelJournal {
	if journal.BootID != bootID {
		return ownedKernelJournal{BootID: bootID, Fields: nil}
	}
	return journal
}

func (r *OwnedKernelPolicyReconciler) save() error {
	data, err := json.Marshal(r.journal)
	if err != nil {
		slog.Warn("kernel policy journal encode failed", "err", err)
		return fmt.Errorf("encode kernel policy journal: %w", err)
	}
	if _, err := installfile.Write(r.path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("persist kernel policy journal: %w", err)
	}
	directory, err := os.Open(filepath.Dir(r.path))
	if err != nil {
		slog.Warn("kernel policy directory open failed", "err", err)
		return fmt.Errorf("open kernel policy directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		slog.Warn("kernel policy directory sync failed", "err", err)
		return fmt.Errorf("sync kernel policy directory: %w", err)
	}
	return nil
}

// Reconcile retains policy across shutdown and restores fields omitted by current intent.
func (r *OwnedKernelPolicyReconciler) Reconcile(ctx context.Context, connections []interfaceintent.Connection, results []OwnedLinkResult) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("kernel policy reconciliation canceled: %w", err)
	}
	if r.sysctl.DryRun() {
		return nil
	}
	ready := make(map[string]OwnedLinkResult, len(results))
	for _, result := range results {
		ready[result.ConnectionID] = result
	}
	keep := make(map[string]bool)
	for _, connection := range connections {
		if connection.Owner != interfaceintent.OwnerMWAN {
			continue
		}
		for _, setting := range kernelPolicySettings(connection) {
			keep[kernelFieldKey(connection.ID.String(), setting.family, setting.leaf)] = true
		}
	}
	var failures []error
	if err := r.restoreOmitted(ctx, keep); err != nil {
		failures = append(failures, err)
	}
	for _, connection := range connections {
		if connection.Owner != interfaceintent.OwnerMWAN {
			continue
		}
		result, found := ready[connection.ID.String()]
		if !found || result.Status != OwnedLinkReady {
			continue
		}
		if err := r.applyConnection(ctx, connection, result); err != nil {
			failures = append(failures, err)
		}
	}
	err := errors.Join(failures...)
	if err != nil {
		slog.WarnContext(ctx, "kernel policy reconciliation failed", "err", err)
	}
	return err
}

func kernelFieldKey(id, family, leaf string) string { return id + "/" + family + "/" + leaf }

func kernelPolicySettings(connection interfaceintent.Connection) []kernelPolicySetting {
	var settings []kernelPolicySetting
	if connection.IPv4 != nil && connection.IPv4.Forwarding != nil {
		settings = append(settings, kernelPolicySetting{"ipv4", "forwarding", kernelPolicyBool(*connection.IPv4.Forwarding)})
	}
	if connection.IPv6 == nil {
		return settings
	}
	ipv6 := connection.IPv6
	if ipv6.AcceptRA != nil && !*ipv6.AcceptRA {
		settings = append(settings, kernelPolicySetting{"ipv6", "accept_ra", "0"})
	}
	if ipv6.AutoConf != nil || ipv6.AcceptRA != nil {
		value := ipv6.AcceptRA != nil && *ipv6.AcceptRA
		if ipv6.AutoConf != nil {
			value = *ipv6.AutoConf
		}
		settings = append(settings, kernelPolicySetting{"ipv6", "autoconf", kernelPolicyBool(value)})
	}
	if ipv6.AcceptRADefaultRoute != nil || ipv6.AcceptRA != nil {
		value := ipv6.AcceptRA != nil && *ipv6.AcceptRA
		if ipv6.AcceptRADefaultRoute != nil {
			value = *ipv6.AcceptRADefaultRoute
		}
		settings = append(settings, kernelPolicySetting{"ipv6", "accept_ra_defrtr", kernelPolicyBool(value)})
	}
	if ipv6.RouteMetric != nil && !ipv6.Gateway.IsValid() {
		settings = append(settings, kernelPolicySetting{"ipv6", "ra_defrtr_metric", strconv.FormatUint(uint64(*ipv6.RouteMetric), 10)})
	}
	if ipv6.Forwarding != nil {
		settings = append(settings, kernelPolicySetting{"ipv6", "forwarding", kernelPolicyBool(*ipv6.Forwarding)})
	}
	if ipv6.AcceptRA != nil && *ipv6.AcceptRA {
		settings = append(settings, kernelPolicySetting{"ipv6", "accept_ra", "2"})
	}
	return settings
}

func kernelPolicyBool(enabled bool) string {
	if enabled {
		return "1"
	}
	return "0"
}

func (r *OwnedKernelPolicyReconciler) applyConnection(ctx context.Context, connection interfaceintent.Connection, ready OwnedLinkResult) error {
	if ready.IfIndex == 0 || ready.Name != connection.Name || ready.ActualName == "" {
		return fmt.Errorf("connection %s kernel policy link is not ready", connection.ID)
	}
	link, err := netlink.LinkByIndex(ready.IfIndex)
	if err != nil {
		slog.Warn("kernel policy ready link lookup failed", "connection_id", connection.ID, "err", err)
		return fmt.Errorf("find kernel policy ready link: %w", err)
	}
	if link.Attrs().Name != ready.ActualName || !linkMatchesConnection(link, connection) {
		return fmt.Errorf("connection %s kernel policy link identity changed", connection.ID)
	}
	for _, setting := range kernelPolicySettings(connection) {
		if err := r.applyField(ctx, connection.ID.String(), link, setting); err != nil {
			return err
		}
	}
	return nil
}

func (r *OwnedKernelPolicyReconciler) applyField(ctx context.Context, id string, link netlink.Link, setting kernelPolicySetting) error {
	index := -1
	for i, field := range r.journal.Fields {
		if kernelFieldKey(field.ConnectionID, field.Family, field.Leaf) == kernelFieldKey(id, setting.family, setting.leaf) {
			if !LinkMatchesIdentity(link, field.LinkIndex, field.LinkIdentity) {
				if err := r.restoreField(ctx, i); err != nil {
					return err
				}
				break
			}
			index = i
			break
		}
	}
	if index < 0 {
		key := "net." + setting.family + ".conf." + link.Attrs().Name + "." + setting.leaf
		original, err := r.readValue(ctx, key)
		if err != nil {
			return err
		}
		r.journal.Fields = append(r.journal.Fields, ownedKernelField{
			ConnectionID: id, LinkIndex: link.Attrs().Index, LinkIdentity: LinkOwnershipIdentity(link),
			Family: setting.family, Leaf: setting.leaf, Original: original,
			Pending: nil, Verified: nil,
		})
		index = len(r.journal.Fields) - 1
	}
	field := &r.journal.Fields[index]
	unchanged, err := r.fieldUnchanged(ctx, *field, setting.value)
	if err != nil {
		return err
	}
	if unchanged {
		return nil
	}
	field.Pending = &setting.value
	if err := r.save(); err != nil {
		return err
	}
	if err := r.writeVerified(ctx, *field, setting.value, false); err != nil {
		return err
	}
	field.Verified, field.Pending = &setting.value, nil
	return r.save()
}

func (r *OwnedKernelPolicyReconciler) fieldUnchanged(ctx context.Context, field ownedKernelField, value string) (bool, error) {
	if field.Pending != nil || field.Verified == nil || *field.Verified != value {
		return false, nil
	}
	link, matches, err := r.observedField(field)
	if err != nil || !matches {
		return false, err
	}
	key := "net." + field.Family + ".conf." + link.Attrs().Name + "." + field.Leaf
	current, err := r.readValue(ctx, key)
	return current == value, err
}

func (r *OwnedKernelPolicyReconciler) readValue(ctx context.Context, key string) (string, error) {
	value, err := r.sysctl.Get(ctx, key)
	if err != nil {
		slog.WarnContext(ctx, "kernel policy read failed", "key", key, "err", err)
		return "", fmt.Errorf("read kernel policy %s: %w", key, err)
	}
	return value, nil
}

func (r *OwnedKernelPolicyReconciler) observedField(field ownedKernelField) (netlink.Link, bool, error) {
	link, err := netlink.LinkByIndex(field.LinkIndex)
	if IsLinkNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		slog.Warn("kernel policy recorded link lookup failed", "connection_id", field.ConnectionID, "err", err)
		return nil, false, fmt.Errorf("find kernel policy recorded link: %w", err)
	}
	return link, LinkMatchesIdentity(link, field.LinkIndex, field.LinkIdentity), nil
}

func (r *OwnedKernelPolicyReconciler) writeVerified(ctx context.Context, field ownedKernelField, value string, restoring bool) error {
	link, matches, err := r.observedField(field)
	if err != nil {
		return err
	}
	if !matches {
		return fmt.Errorf("connection %s kernel policy link identity changed before write", field.ConnectionID)
	}
	key := "net." + field.Family + ".conf." + link.Attrs().Name + "." + field.Leaf
	current, err := r.readValue(ctx, key)
	if err != nil {
		return err
	}
	if restoring && current != field.Original && !kernelOwnedValue(field, current) {
		return fmt.Errorf("kernel policy %s changed outside MWAN before restoration", key)
	}
	if current != value {
		if err := r.sysctl.Set(ctx, key, value); err != nil {
			slog.WarnContext(ctx, "kernel policy write failed", "key", key, "err", err)
			return fmt.Errorf("write kernel policy %s: %w", key, err)
		}
	}
	linkAfter, matches, err := r.observedField(field)
	if err != nil {
		return err
	}
	if !matches || linkAfter.Attrs().Name != link.Attrs().Name {
		return fmt.Errorf("connection %s kernel policy link changed during write", field.ConnectionID)
	}
	actual, err := r.readValue(ctx, key)
	if err != nil {
		return err
	}
	if actual != value {
		return fmt.Errorf("kernel policy %s is %s after writing %s", key, actual, value)
	}
	return nil
}

func (r *OwnedKernelPolicyReconciler) restoreOmitted(ctx context.Context, keep map[string]bool) error {
	sort.SliceStable(r.journal.Fields, func(i, j int) bool {
		return kernelRestoreOrder(r.journal.Fields[i]) < kernelRestoreOrder(r.journal.Fields[j])
	})
	var failures []error
	for i := 0; i < len(r.journal.Fields); {
		field := r.journal.Fields[i]
		if keep[kernelFieldKey(field.ConnectionID, field.Family, field.Leaf)] {
			i++
			continue
		}
		if err := r.restoreField(ctx, i); err != nil {
			failures = append(failures, err)
			i++
		}
	}
	err := errors.Join(failures...)
	if err != nil {
		slog.WarnContext(ctx, "kernel policy restoration failed", "err", err)
	}
	return err
}

func kernelRestoreOrder(field ownedKernelField) int {
	if field.Family == "ipv6" && field.Leaf == "accept_ra" {
		if field.Original == "0" {
			return 0
		}
		return 2
	}
	return 1
}

func (r *OwnedKernelPolicyReconciler) restoreField(ctx context.Context, index int) error {
	field := r.journal.Fields[index]
	link, matches, err := r.observedField(field)
	if err != nil {
		return err
	}
	if matches {
		if err := r.restoreOwnedField(ctx, index, link); err != nil {
			return err
		}
	}
	r.journal.Fields = append(r.journal.Fields[:index], r.journal.Fields[index+1:]...)
	return r.save()
}

func (r *OwnedKernelPolicyReconciler) restoreOwnedField(ctx context.Context, index int, link netlink.Link) error {
	field := r.journal.Fields[index]
	key := "net." + field.Family + ".conf." + link.Attrs().Name + "." + field.Leaf
	current, err := r.readValue(ctx, key)
	if err != nil {
		return err
	}
	if current == field.Original {
		return nil
	}
	if !kernelOwnedValue(field, current) {
		return fmt.Errorf("kernel policy %s changed outside MWAN; original value was not restored", key)
	}
	r.journal.Fields[index].Verified = &current
	r.journal.Fields[index].Pending = &field.Original
	if err := r.save(); err != nil {
		return err
	}
	return r.writeVerified(ctx, field, field.Original, true)
}

func kernelOwnedValue(field ownedKernelField, value string) bool {
	return field.Verified != nil && value == *field.Verified || field.Pending != nil && value == *field.Pending
}

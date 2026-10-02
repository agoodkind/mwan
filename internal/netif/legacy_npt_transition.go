package netif

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/installfile"
	"goodkind.io/mwan/internal/interfaceintent"
)

// LegacyNPTEdge limits a producer transition to verified intent and kernel rules.
type LegacyNPTEdge struct {
	Record       NPTEdgeRecord `json:"record"`
	IntentSHA256 string        `json:"intent_sha256"`
	RulesSHA256  string        `json:"rules_sha256"`
}

// LegacyMappedAddress binds a configured secondary mapping to its kernel address.
type LegacyMappedAddress struct {
	Record       ownedStaticObject `json:"record"`
	IntentSHA256 string            `json:"intent_sha256"`
	KernelSHA256 string            `json:"kernel_sha256"`
}

// ReadLegacyMappedAddresses omits absent mappings and never changes kernel state.
func ReadLegacyMappedAddresses(connection interfaceintent.Connection, prefixes []netip.Prefix, intentSHA256 string) ([]LegacyMappedAddress, error) {
	if len(intentSHA256) != sha256.Size*2 {
		return nil, fmt.Errorf("legacy mapping requires an exact intent digest")
	}
	link, err := ObserveLegacyLink(connection)
	if err != nil {
		return nil, NewLegacyNPTError("read legacy mapping link", err)
	}
	addresses, err := netlink.AddrList(link, unix.AF_INET)
	if err != nil {
		return nil, NewLegacyNPTError("read legacy mapping addresses", err)
	}
	var captured []LegacyMappedAddress
	for _, prefix := range prefixes {
		if !prefix.Addr().Is4() || prefix.Bits() != 32 {
			return nil, fmt.Errorf("legacy mapping requires an IPv4 secondary /32")
		}
		for _, address := range addresses {
			if !addressMatches(address, prefix) {
				continue
			}
			if address.Flags&(unix.IFA_F_TENTATIVE|unix.IFA_F_DADFAILED) != 0 {
				return nil, fmt.Errorf("legacy mapped address %s is not ready", prefix)
			}
			data, err := json.Marshal(address)
			if err != nil {
				return nil, NewLegacyNPTError("marshal legacy mapped address", err)
			}
			digest := sha256.Sum256(data)
			captured = append(captured, LegacyMappedAddress{Record: ownedStaticObject{Scope: "", ConnectionID: connection.ID.String(), Family: "ipv4", LinkName: connection.Name, LinkIndex: link.Attrs().Index, LinkIdentity: LinkOwnershipIdentity(link), Prefix: prefix.String(), Destination: "", Gateway: "", Metric: 0}, IntentSHA256: intentSHA256, KernelSHA256: hex.EncodeToString(digest[:])})
		}
	}
	return captured, nil
}

type legacyNPTProducer struct {
	PID              int    `json:"pid"`
	StartTime        string `json:"start_time"`
	ExecutableSHA256 string `json:"executable_sha256"`
}

type legacyNPTTransition struct {
	Version          int                   `json:"version"`
	BootID           string                `json:"boot_id"`
	NetworkNamespace string                `json:"network_namespace"`
	Producer         legacyNPTProducer     `json:"producer"`
	JournalPath      string                `json:"journal_path"`
	Edges            []LegacyNPTEdge       `json:"edges"`
	MappedAddresses  []LegacyMappedAddress `json:"mapped_addresses,omitempty"`
}

// ReadTransitionKernelIdentity binds receipts to the boot and network namespace.
func ReadTransitionKernelIdentity() (string, string, error) {
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", "", NewLegacyNPTError("read transition boot identity", err)
	}
	namespace, err := os.Readlink("/proc/self/ns/net")
	if err != nil {
		return "", "", NewLegacyNPTError("read transition namespace", err)
	}
	return string(boot), namespace, nil
}

// ReadLegacyNPTProducer verifies the running WAN command and original executable.
func ReadLegacyNPTProducer(pid int, expectedSHA256 string) (legacyNPTProducer, error) {
	if pid <= 1 || pid == os.Getpid() || len(expectedSHA256) != sha256.Size*2 {
		return legacyNPTProducer{}, fmt.Errorf("invalid legacy producer identity")
	}
	root := filepath.Join("/proc", strconv.Itoa(pid))
	command, err := os.ReadFile(filepath.Join(root, "cmdline"))
	if err != nil {
		return legacyNPTProducer{}, NewLegacyNPTError("read original command", err)
	}
	arguments := strings.Split(string(command), "\x00")
	if len(arguments) < 4 || arguments[1] != "ifmgr" {
		return legacyNPTProducer{}, fmt.Errorf("PID %d is not an ifmgr producer", pid)
	}
	wan := false
	for i, argument := range arguments {
		if argument == "--role=wan" || argument == "--role" && i+1 < len(arguments) && arguments[i+1] == "wan" {
			wan = true
		}
	}
	if !wan {
		return legacyNPTProducer{}, fmt.Errorf("PID %d is not the WAN producer", pid)
	}
	namespace, err := os.Readlink(filepath.Join(root, "ns/net"))
	if err != nil {
		return legacyNPTProducer{}, NewLegacyNPTError("read original namespace", err)
	}
	_, currentNamespace, err := ReadTransitionKernelIdentity()
	if err != nil {
		return legacyNPTProducer{}, err
	}
	if namespace != currentNamespace {
		return legacyNPTProducer{}, fmt.Errorf("legacy producer network namespace differs")
	}
	stat, err := os.ReadFile(filepath.Join(root, "stat"))
	if err != nil {
		return legacyNPTProducer{}, NewLegacyNPTError("read original process start time", err)
	}
	end := strings.LastIndex(string(stat), ") ")
	if end < 0 {
		return legacyNPTProducer{}, fmt.Errorf("invalid producer process stat")
	}
	fields := strings.Fields(string(stat)[end+2:])
	if len(fields) < 20 {
		return legacyNPTProducer{}, fmt.Errorf("incomplete producer process stat")
	}
	executable, err := os.Open(filepath.Join(root, "exe"))
	if err != nil {
		return legacyNPTProducer{}, NewLegacyNPTError("open original executable", err)
	}
	defer executable.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, executable); err != nil {
		return legacyNPTProducer{}, NewLegacyNPTError("hash original executable", err)
	}
	actual := hex.EncodeToString(digest.Sum(nil))
	if actual != expectedSHA256 {
		return legacyNPTProducer{}, fmt.Errorf("legacy producer executable differs from verified release")
	}
	return legacyNPTProducer{PID: pid, StartTime: fields[19], ExecutableSHA256: actual}, nil
}

// WriteLegacyNPTTransition records the running producer without changing kernel state.
func WriteLegacyNPTTransition(path string, pid int, producerSHA256, journalPath string, edges []LegacyNPTEdge, mappings []LegacyMappedAddress) error {
	if os.Geteuid() != 0 || !filepath.IsAbs(path) || !filepath.IsAbs(journalPath) || len(edges) == 0 {
		return fmt.Errorf("legacy NPT capture requires root, absolute paths and verified edges")
	}
	producer, err := ReadLegacyNPTProducer(pid, producerSHA256)
	if err != nil {
		return err
	}
	boot, namespace, err := ReadTransitionKernelIdentity()
	if err != nil {
		return err
	}
	manifest := legacyNPTTransition{Version: 1, BootID: boot, NetworkNamespace: namespace, Producer: producer, JournalPath: journalPath, Edges: edges, MappedAddresses: mappings}
	data, err := json.Marshal(manifest)
	if err != nil {
		return NewLegacyNPTError("marshal legacy NPT manifest", err)
	}
	confirmed, err := ReadLegacyNPTProducer(pid, producerSHA256)
	if err != nil {
		return err
	}
	if confirmed != producer {
		return fmt.Errorf("legacy producer changed during capture")
	}
	if info, err := os.Lstat(path); err == nil {
		owner, ok := info.Sys().(*syscall.Stat_t)
		if !ok || owner.Uid != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return fmt.Errorf("existing legacy NPT manifest must be a root-owned regular file with mode 0600")
		}
	} else if !os.IsNotExist(err) {
		return NewLegacyNPTError("inspect legacy NPT manifest path", err)
	}
	_, err = installfile.Write(path, append(data, '\n'), 0o600)
	if err != nil {
		return NewLegacyNPTError("write legacy NPT manifest", err)
	}
	return nil
}

// ApplyLegacyNPTTransition adopts exact old-producer edges only after producer termination.
func ApplyLegacyNPTTransition(path, journalPath string, current []LegacyNPTEdge, mappings []LegacyMappedAddress) error {
	if os.Geteuid() != 0 || !filepath.IsAbs(path) || !filepath.IsAbs(journalPath) {
		return fmt.Errorf("legacy NPT adoption requires root and absolute paths")
	}
	manifest, err := ReadLegacyNPTManifest(path, journalPath)
	if err != nil {
		return err
	}
	r, err := NewOwnedStaticReconciler(journalPath)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := make(map[NPTEdgeRecord]bool)
	for _, edge := range manifest.Edges {
		if seen[edge.Record] {
			return fmt.Errorf("duplicate legacy NPT edge")
		}
		seen[edge.Record] = true
		if !slices.Contains(current, edge) {
			return fmt.Errorf("legacy NPT intent, rules or link changed for %s", edge.Record.ConnectionID)
		}
		if err := r.ReadLegacyNPTEdge(edge.Record); err != nil {
			return err
		}
	}
	if err := r.readLegacyMappedReceipts(manifest.MappedAddresses, mappings); err != nil {
		return err
	}
	for _, edge := range manifest.Edges {
		value := nptEdgeObject(edge.Record)
		if !r.recorded(value) {
			if err := r.reserve(value); err != nil {
				return err
			}
		}
	}
	for _, mapping := range manifest.MappedAddresses {
		if !r.recorded(mapping.Record) {
			if err := r.reserve(mapping.Record); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *OwnedStaticReconciler) readLegacyMappedReceipts(captured, current []LegacyMappedAddress) error {
	seen := make(map[ownedStaticObject]bool)
	for _, mapping := range captured {
		if seen[mapping.Record] || !slices.Contains(current, mapping) {
			return fmt.Errorf("legacy mapped address intent, link or kernel address changed for %s", mapping.Record.ConnectionID)
		}
		seen[mapping.Record] = true
		for _, old := range r.journal.Objects {
			if old.Prefix == mapping.Record.Prefix && old.LinkName == mapping.Record.LinkName && old != mapping.Record {
				return fmt.Errorf("legacy mapped address conflicts with an existing receipt")
			}
		}
	}
	return nil
}

// ReadLegacyNPTManifest rejects stale evidence and a producer that has not stopped.
func ReadLegacyNPTManifest(path, journalPath string) (legacyNPTTransition, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return legacyNPTTransition{}, NewLegacyNPTError("inspect legacy NPT manifest", err)
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return legacyNPTTransition{}, fmt.Errorf("legacy NPT manifest must be a root-owned regular file with mode 0600")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return legacyNPTTransition{}, NewLegacyNPTError("read legacy NPT manifest", err)
	}
	var manifest legacyNPTTransition
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return legacyNPTTransition{}, NewLegacyNPTError("decode legacy NPT manifest", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return legacyNPTTransition{}, fmt.Errorf("legacy NPT manifest contains trailing data")
	}
	boot, namespace, err := ReadTransitionKernelIdentity()
	if err != nil {
		return legacyNPTTransition{}, err
	}
	if manifest.Version != 1 || manifest.BootID != boot || manifest.NetworkNamespace != namespace || manifest.JournalPath != journalPath || manifest.Producer.PID <= 1 || manifest.Producer.StartTime == "" || len(manifest.Producer.ExecutableSHA256) != sha256.Size*2 || len(manifest.Edges) == 0 {
		return legacyNPTTransition{}, fmt.Errorf("legacy NPT manifest identity is stale or invalid")
	}
	if _, err := os.Stat(filepath.Join("/proc", strconv.Itoa(manifest.Producer.PID))); !os.IsNotExist(err) {
		return legacyNPTTransition{}, fmt.Errorf("legacy NPT producer PID %d is still present or cannot be verified absent", manifest.Producer.PID)
	}
	return manifest, nil
}

// ReadLegacyNPTEdge rejects changed kernel identity and conflicting scoped receipts.
func (r *OwnedStaticReconciler) ReadLegacyNPTEdge(record NPTEdgeRecord) error {
	link, err := netlink.LinkByIndex(record.InterfaceIndex)
	if err != nil || link.Attrs().Name != record.Interface || !LinkMatchesIdentity(link, record.InterfaceIndex, record.LinkIdentity) {
		return fmt.Errorf("legacy NPT link changed for %s", record.ConnectionID)
	}
	addresses, err := netlink.AddrList(link, unix.AF_INET6)
	if err != nil {
		return NewLegacyNPTError("read legacy NPT edge addresses", err)
	}
	ready := false
	for _, address := range addresses {
		if addressMatches(address, record.Prefix) && address.Flags&(unix.IFA_F_TENTATIVE|unix.IFA_F_DADFAILED) == 0 {
			ready = true
		}
	}
	if !ready {
		return fmt.Errorf("legacy NPT edge is not ready for %s", record.ConnectionID)
	}
	value := nptEdgeObject(record)
	for _, old := range r.journal.Objects {
		if old.Prefix == value.Prefix && old.LinkName == value.LinkName && old != value {
			return fmt.Errorf("legacy NPT edge conflicts with an existing receipt")
		}
	}
	return nil
}

// NewLegacyNPTError preserves the operation and cause for the command error boundary.
func NewLegacyNPTError(operation string, cause error) *LegacyNPTOperationError {
	return &LegacyNPTOperationError{operation: operation, cause: cause}
}

// LegacyNPTOperationError retains the original cause without logging it.
type LegacyNPTOperationError struct {
	operation string
	cause     error
}

// Error includes the operation in the command's rejection message.
func (failure *LegacyNPTOperationError) Error() string {
	return failure.operation + ": " + failure.cause.Error()
}

// Unwrap permits [errors.Is] and [errors.As] to inspect the original cause.
func (failure *LegacyNPTOperationError) Unwrap() error {
	return failure.cause
}

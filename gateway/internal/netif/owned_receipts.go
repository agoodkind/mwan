package netif

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"goodkind.io/mwan/internal/connectionid"
)

func readOwnedJournal[T ownedLinkState | ownedStaticJournal | ownedKernelJournal](path string, journal *T) (present bool, failure error) {
	defer func() {
		if failure != nil {
			slog.Warn("ownership journal read failed", "path", path, "error", failure)
		}
	}()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read ownership journal %s: %w", path, err)
	}
	if err := json.Unmarshal(data, journal); err != nil {
		return true, fmt.Errorf("decode ownership journal %s: %w", path, err)
	}
	return true, nil
}

// OwnedReleaseReceipts separates ordinary ownership from required NPT edge addresses.
type OwnedReleaseReceipts struct {
	LinksPresent        bool `json:"links_present"`
	AddressesPresent    bool `json:"addresses_present"`
	KernelPolicyPresent bool `json:"kernel_policy_present"`
	VirtualLinks        int  `json:"virtual_links"`
	Memberships         int  `json:"memberships"`
	OrdinaryObjects     int  `json:"ordinary_objects"`
	Promotions          int  `json:"promotions"`
	KernelFields        int  `json:"kernel_fields"`
	NPTEdges            int  `json:"npt_edges"`
	PreviousBoot        bool `json:"previous_boot"`
}

// Released permits current-boot NPT edge receipts and rejects ordinary receipts.
func (receipts OwnedReleaseReceipts) Released() bool {
	return !receipts.PreviousBoot && receipts.VirtualLinks == 0 && receipts.Memberships == 0 && receipts.OrdinaryObjects == 0 && receipts.Promotions == 0 && receipts.KernelFields == 0
}

// InspectOwnedRelease reads persisted receipts without constructor boot normalization.
func InspectOwnedRelease(id connectionid.ID, linksPath, addressesPath, kernelPath string) (result OwnedReleaseReceipts, failure error) {
	defer func() {
		if failure != nil {
			slog.Warn("connection release inspection failed", "connection", id, "error", failure)
		}
	}()
	for _, path := range []string{linksPath, addressesPath, kernelPath} {
		if !filepath.IsAbs(path) {
			return result, fmt.Errorf("release verification requires absolute links, addresses, and kernel policy journal paths")
		}
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return result, fmt.Errorf("read release boot ID: %w", err)
	}
	bootID := strings.TrimSpace(string(boot))
	var links ownedLinkState
	result.LinksPresent, err = readOwnedJournal(linksPath, &links)
	if err != nil {
		return result, err
	}
	if record, found := links.Virtuals[id.String()]; found {
		result.VirtualLinks++
		result.PreviousBoot = strings.TrimSpace(record.BootID) != bootID
	}
	if record, found := links.Memberships[id.String()]; found {
		result.Memberships++
		result.PreviousBoot = result.PreviousBoot || strings.TrimSpace(record.MasterBoot) != bootID
	}
	var addresses ownedStaticJournal
	result.AddressesPresent, err = readOwnedJournal(addressesPath, &addresses)
	if err != nil {
		return result, err
	}
	if err := validateScopedAddressRecords(addresses.Objects); err != nil {
		return result, err
	}
	for _, record := range addresses.Objects {
		if record.ConnectionID != id.String() {
			continue
		}
		if record.Scope == nptEdgeScope {
			result.NPTEdges++
		} else {
			result.OrdinaryObjects++
		}
		result.PreviousBoot = result.PreviousBoot || strings.TrimSpace(addresses.BootID) != bootID
	}
	for _, record := range addresses.Promotion {
		if record.ConnectionID == id.String() {
			result.Promotions++
			result.PreviousBoot = result.PreviousBoot || strings.TrimSpace(addresses.BootID) != bootID
		}
	}
	var kernel ownedKernelJournal
	result.KernelPolicyPresent, err = readOwnedJournal(kernelPath, &kernel)
	if err != nil {
		return result, err
	}
	for _, field := range kernel.Fields {
		if field.ConnectionID == id.String() {
			result.KernelFields++
			result.PreviousBoot = result.PreviousBoot || strings.TrimSpace(kernel.BootID) != bootID
		}
	}
	return result, nil
}

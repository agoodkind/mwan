package pd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strconv"

	"github.com/godbus/dbus/v5"
)

const networkdService = "org.freedesktop.network1"

type describeDocument struct {
	DHCPv6       describeDHCPv6 `json:"DHCPv6"`
	DHCPv6Client describeDHCPv6 `json:"DHCPv6Client"`
}

type describeDHCPv6 struct {
	Prefixes []describePrefix `json:"Prefixes"`
}

type describePrefix struct {
	Prefix       []int `json:"Prefix"`
	PrefixLength int   `json:"PrefixLength"`
}

func (s *DefaultSource) prefixFromNetworkdDBus(
	ctx context.Context,
	iface string,
) (netip.Prefix, bool, error) {
	log := s.log
	if log == nil {
		log = slog.Default()
	}

	conn, err := dbus.SystemBusPrivate()
	if err != nil {
		log.WarnContext(ctx, "pd: system bus open failed", "iface", iface, "err", err)
		return netip.Prefix{}, false, fmt.Errorf("open system bus: %w", err)
	}
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			log.WarnContext(ctx, "pd: system bus close failed", "iface", iface, "err", closeErr)
		}
	}()
	if err := conn.Auth(nil); err != nil {
		log.WarnContext(ctx, "pd: system bus auth failed", "iface", iface, "err", err)
		return netip.Prefix{}, false, fmt.Errorf("auth system bus: %w", err)
	}
	if err := conn.Hello(); err != nil {
		log.WarnContext(ctx, "pd: system bus hello failed", "iface", iface, "err", err)
		return netip.Prefix{}, false, fmt.Errorf("hello system bus: %w", err)
	}

	manager := conn.Object(networkdService, dbus.ObjectPath("/org/freedesktop/network1"))
	call := manager.CallWithContext(
		ctx,
		"org.freedesktop.network1.Manager.GetLinkByName",
		0,
		iface,
	)
	if call.Err != nil {
		log.WarnContext(ctx, "pd: networkd GetLinkByName failed", "iface", iface, "err", call.Err)
		return netip.Prefix{}, false, fmt.Errorf("GetLinkByName(%s): %w", iface, call.Err)
	}
	var linkIndex int32
	var linkPath dbus.ObjectPath
	if err := call.Store(&linkIndex, &linkPath); err != nil {
		log.WarnContext(ctx, "pd: networkd GetLinkByName decode failed", "iface", iface, "err", err)
		return netip.Prefix{}, false, fmt.Errorf("decode GetLinkByName(%s): %w", iface, err)
	}

	link := conn.Object(networkdService, linkPath)
	describeCall := link.CallWithContext(ctx, "org.freedesktop.network1.Link.Describe", 0)
	if describeCall.Err != nil {
		log.WarnContext(ctx, "pd: networkd Link.Describe failed", "iface", iface, "err", describeCall.Err)
		return netip.Prefix{}, false, fmt.Errorf("Describe(%s): %w", iface, describeCall.Err)
	}
	var describeJSON string
	if err := describeCall.Store(&describeJSON); err != nil {
		log.WarnContext(ctx, "pd: networkd Link.Describe decode failed", "iface", iface, "err", err)
		return netip.Prefix{}, false, fmt.Errorf("decode Describe(%s): %w", iface, err)
	}

	prefixes, err := UnmarshalDescribePrefixes([]byte(describeJSON))
	if err != nil {
		log.WarnContext(ctx, "pd: parse networkd Describe JSON failed", "iface", iface, "err", err)
		return netip.Prefix{}, false, fmt.Errorf("parse Describe(%s): %w", iface, err)
	}
	if len(prefixes) == 0 {
		return netip.Prefix{}, false, nil
	}
	return prefixes[0], true, nil
}

// UnmarshalDescribePrefixes decodes networkd Link.Describe delegated prefixes.
func UnmarshalDescribePrefixes(data []byte) ([]netip.Prefix, error) {
	var document describeDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, errors.New("decode Describe JSON: " + err.Error())
	}

	entries := document.DHCPv6.Prefixes
	// systemd-networkd Describe currently reports delegated prefixes under
	// DHCPv6.Prefixes; DHCPv6Client.Prefixes is accepted for script-era output.
	if len(entries) == 0 {
		entries = document.DHCPv6Client.Prefixes
	}

	prefixes := make([]netip.Prefix, 0, len(entries))
	entryErrors := make([]error, 0)
	for i, entry := range entries {
		prefix, err := prefixFromDescribeEntry(entry)
		if err != nil {
			entryErrors = append(
				entryErrors,
				errors.New("prefix["+strconv.Itoa(i)+"]: "+err.Error()),
			)
			continue
		}
		prefixes = append(prefixes, prefix)
	}
	if len(prefixes) == 0 && len(entryErrors) > 0 {
		return nil, entryErrors[0]
	}
	return prefixes, nil
}

func prefixFromDescribeEntry(entry describePrefix) (netip.Prefix, error) {
	addr, err := addrFromPrefixBytes(entry.Prefix)
	if err != nil {
		return netip.Prefix{}, err
	}
	if entry.PrefixLength < 0 || entry.PrefixLength > 128 {
		return netip.Prefix{}, fmt.Errorf("prefix length %d out of range", entry.PrefixLength)
	}
	return netip.PrefixFrom(addr, entry.PrefixLength).Masked(), nil
}

func addrFromPrefixBytes(prefixBytes []int) (netip.Addr, error) {
	if len(prefixBytes) != 16 {
		return netip.Addr{}, fmt.Errorf("prefix byte length %d, want 16", len(prefixBytes))
	}

	var addrBytes [16]byte
	for i, value := range prefixBytes {
		if value < 0 || value > 255 {
			return netip.Addr{}, fmt.Errorf("prefix byte[%d]=%d out of range", i, value)
		}
		addrBytes[i] = byte(value)
	}
	return netip.AddrFrom16(addrBytes), nil
}

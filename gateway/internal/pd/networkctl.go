package pd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"sort"
	"strings"
)

const maxNetworkctlPrefixBits = 60

func (s *DefaultSource) prefixFromNetworkctl(
	ctx context.Context,
	iface string,
) (netip.Prefix, bool, error) {
	log := s.log
	if log == nil {
		log = slog.Default()
	}
	output, err := s.command(ctx, log, "networkctl", []string{"status", iface, "--json=short"})
	if err != nil {
		log.WarnContext(ctx, "pd: networkctl failed", "iface", iface, "err", err)
		return netip.Prefix{}, false, fmt.Errorf("networkctl status %s: %w", iface, err)
	}

	prefixes, err := UnmarshalNetworkctlPrefixes(output)
	if err != nil {
		log.WarnContext(ctx, "pd: parse networkctl JSON failed", "iface", iface, "err", err)
		return netip.Prefix{}, false, fmt.Errorf("parse networkctl status %s: %w", iface, err)
	}
	if len(prefixes) == 0 {
		return netip.Prefix{}, false, nil
	}
	return prefixes[0], true, nil
}

// UnmarshalNetworkctlPrefixes decodes networkctl delegated-prefix fields.
func UnmarshalNetworkctlPrefixes(data []byte) ([]netip.Prefix, error) {
	prefixes := make([]netip.Prefix, 0)
	var raw json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, errors.New("decode networkctl JSON: " + err.Error())
	}
	if err := unmarshalNetworkctlPrefixFields(raw, &prefixes); err != nil {
		return nil, errors.New("decode networkctl delegated prefixes: " + err.Error())
	}
	// Fallback matching find-pd-prefixes.sh: networkctl output shape varies
	// across systemd versions, so when no DelegatedPrefix* key is present,
	// scan every string value for an IPv6 CIDR no longer than /60. Without
	// this a valid delegation can be missed and the slower probes take over.
	if len(prefixes) == 0 {
		if err := unmarshalCIDRStrings(raw, maxNetworkctlPrefixBits, &prefixes); err != nil {
			return nil, errors.New("scan networkctl CIDRs: " + err.Error())
		}
	}
	return uniqueSortedPrefixes(prefixes), nil
}

func unmarshalNetworkctlPrefixFields(raw json.RawMessage, prefixes *[]netip.Prefix) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil
	}

	switch raw[0] {
	case '{':
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			return errors.New("decode networkctl object: " + err.Error())
		}
		for key, value := range object {
			if isDelegatedPrefixKey(key) {
				if err := unmarshalCIDRStrings(value, maxNetworkctlPrefixBits, prefixes); err != nil {
					return err
				}
				continue
			}
			if err := unmarshalNetworkctlPrefixFields(value, prefixes); err != nil {
				return err
			}
		}
	case '[':
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return errors.New("decode networkctl array: " + err.Error())
		}
		for _, item := range items {
			if err := unmarshalNetworkctlPrefixFields(item, prefixes); err != nil {
				return err
			}
		}
	}
	return nil
}

func isDelegatedPrefixKey(key string) bool {
	return key == "DelegatedPrefixes" || key == "DelegatedPrefix"
}

func unmarshalCIDRStrings(
	raw json.RawMessage,
	maxBits int,
	prefixes *[]netip.Prefix,
) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil
	}

	switch raw[0] {
	case '"':
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return errors.New("decode CIDR string: " + err.Error())
		}
		prefix, ok := parseIPv6PrefixText(value, maxBits)
		if ok {
			*prefixes = append(*prefixes, prefix)
		}
	case '[':
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return errors.New("decode CIDR array: " + err.Error())
		}
		for _, item := range items {
			if err := unmarshalCIDRStrings(item, maxBits, prefixes); err != nil {
				return err
			}
		}
	case '{':
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			return errors.New("decode CIDR object: " + err.Error())
		}
		for _, value := range object {
			if err := unmarshalCIDRStrings(value, maxBits, prefixes); err != nil {
				return err
			}
		}
	}
	return nil
}

func parseIPv6PrefixText(text string, maxBits int) (netip.Prefix, bool) {
	prefix, err := netip.ParsePrefix(strings.TrimSpace(text))
	if err != nil {
		return netip.Prefix{}, false
	}
	prefix = prefix.Masked()
	if !prefix.Addr().Is6() {
		return netip.Prefix{}, false
	}
	if maxBits >= 0 && prefix.Bits() > maxBits {
		return netip.Prefix{}, false
	}
	return prefix, true
}

func uniqueSortedPrefixes(prefixes []netip.Prefix) []netip.Prefix {
	byString := make(map[string]netip.Prefix, len(prefixes))
	keys := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		key := prefix.String()
		if _, ok := byString[key]; ok {
			continue
		}
		byString[key] = prefix
		keys = append(keys, key)
	}
	sort.Strings(keys)

	result := make([]netip.Prefix, 0, len(keys))
	for _, key := range keys {
		result = append(result, byString[key])
	}
	return result
}

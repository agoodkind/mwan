//go:build linux

package firewall

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

type kernelItem struct {
	Table *struct {
		Family string `json:"family"`
		Name   string `json:"name"`
	} `json:"table"`
	Chain *struct {
		Family   string `json:"family"`
		Table    string `json:"table"`
		Name     string `json:"name"`
		Type     string `json:"type"`
		Hook     string `json:"hook"`
		Priority int    `json:"prio"`
		Policy   string `json:"policy"`
	} `json:"chain"`
	Set *struct {
		Family string   `json:"family"`
		Table  string   `json:"table"`
		Name   string   `json:"name"`
		Type   string   `json:"type"`
		Flags  []string `json:"flags"`
	} `json:"set"`
}

type kernelRuleset struct {
	Items []kernelItem `json:"nftables"`
}

func runNFT(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	slog.DebugContext(ctx, "run nft", "args", args)
	command := exec.CommandContext(ctx, "nft", args...)
	if stdin != nil {
		command.Stdin = bytes.NewReader(stdin)
	}
	output, err := command.CombinedOutput()
	if err != nil {
		slog.ErrorContext(ctx, "nft failed", "args", args, "err", err)
		return nil, fmt.Errorf("nft %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func readKernel(ctx context.Context) (kernelRuleset, error) {
	output, err := runNFT(ctx, nil, "-j", "list", "ruleset")
	if err != nil {
		return kernelRuleset{}, err
	}
	var readback kernelRuleset
	if err := json.Unmarshal(output, &readback); err != nil {
		slog.ErrorContext(ctx, "decode nft ruleset failed", "err", err)
		return kernelRuleset{}, fmt.Errorf("decode nft ruleset: %w", err)
	}
	return readback, nil
}

func kernelChain(items []kernelItem, wanted Chain) (kernelItem, bool) {
	for _, item := range items {
		if item.Chain != nil && item.Chain.Family == wanted.Table.Family && item.Chain.Table == wanted.Table.Name && item.Chain.Name == wanted.Name {
			return item, true
		}
	}
	var missing kernelItem
	return missing, false
}

func kernelSet(items []kernelItem, wanted Set) (kernelItem, bool) {
	for _, item := range items {
		if item.Set != nil && item.Set.Family == wanted.Table.Family && item.Set.Table == wanted.Table.Name && item.Set.Name == wanted.Name {
			return item, true
		}
	}
	var missing kernelItem
	return missing, false
}

type chainPriority string

const (
	priorityFilter chainPriority = "filter"
	priorityDstNAT chainPriority = "dstnat"
	prioritySrcNAT chainPriority = "srcnat"
	priorityMangle chainPriority = "mangle"
)

func kernelPriority(name string) (int, error) {
	switch chainPriority(name) {
	case priorityFilter:
		return 0, nil
	case priorityDstNAT:
		return -100, nil
	case prioritySrcNAT:
		return 100, nil
	case priorityMangle:
		return -150, nil
	default:
		value, err := strconv.Atoi(name)
		if err != nil {
			slog.Warn("invalid nft chain priority", "priority", name, "err", err)
			return 0, fmt.Errorf("nft priority %q: %w", name, err)
		}
		return value, nil
	}
}

func checkDefinitions(readback kernelRuleset, desired Ruleset) error {
	for _, chain := range desired.Chains {
		item, found := kernelChain(readback.Items, chain)
		if !found {
			return fmt.Errorf("missing %s %s chain %s", chain.Table.Family, chain.Table.Name, chain.Name)
		}
		priority, err := kernelPriority(chain.Priority)
		if err != nil {
			return err
		}
		if item.Chain.Type != chain.Type || item.Chain.Hook != chain.Hook || item.Chain.Priority != priority || item.Chain.Policy != chain.Policy {
			return fmt.Errorf("incompatible chain %s %s %s: got %s/%s/%d/%s, want %s/%s/%d/%s", chain.Table.Family, chain.Table.Name, chain.Name,
				item.Chain.Type, item.Chain.Hook, item.Chain.Priority, item.Chain.Policy,
				chain.Type, chain.Hook, priority, chain.Policy)
		}
	}
	for _, set := range desired.Sets {
		item, found := kernelSet(readback.Items, set)
		if !found {
			return fmt.Errorf("missing %s %s set %s", set.Table.Family, set.Table.Name, set.Name)
		}
		interval := false
		for _, flag := range item.Set.Flags {
			if flag == "interval" {
				interval = true
			}
		}
		// nft applies auto-merge in user space and does not reliably report it back.
		if item.Set.Type != set.KeyType || !interval {
			return fmt.Errorf("incompatible set %s %s %s: expected %s interval", set.Table.Family, set.Table.Name, set.Name, set.KeyType)
		}
	}
	return nil
}

// Inspect verifies each configured chain and rule in the kernel and returns a
// normalized text representation. Handles and counters are excluded.
func Inspect(ctx context.Context, desired Ruleset) (string, error) {
	readback, err := readKernel(ctx)
	if err != nil {
		return "", err
	}
	if err := checkDefinitions(readback, desired); err != nil {
		return "", err
	}
	var normalized strings.Builder
	for _, chain := range desired.Chains {
		output, err := runNFT(ctx, nil, "list", "chain", chain.Table.Family, chain.Table.Name, chain.Name)
		if err != nil {
			return "", err
		}
		actual := readRuleLines(string(output))
		if len(actual) != len(chain.Rules) {
			return "", fmt.Errorf("chain %s %s %s has %d rules, expected %d", chain.Table.Family, chain.Table.Name, chain.Name, len(actual), len(chain.Rules))
		}
		for index, rule := range chain.Rules {
			if normalizeRule(actual[index]) != normalizeRule(rule.Expression) {
				return "", fmt.Errorf("chain %s %s %s rule %d differs: got %q, expected %q", chain.Table.Family, chain.Table.Name, chain.Name, index+1, actual[index], rule.Expression)
			}
		}
		normalized.WriteString(chain.Table.Family + " " + chain.Table.Name + " " + chain.Name + "\n")
		for _, rule := range actual {
			normalized.WriteString("  " + rule + "\n")
		}
	}
	for _, set := range desired.Sets {
		normalized.WriteString(set.Table.Family + " " + set.Table.Name + " set " + set.Name + " " + set.KeyType + " interval\n")
	}
	return normalized.String(), nil
}

func readRuleLines(output string) []string {
	var lines []string
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "}" || strings.HasPrefix(line, "table ") || strings.HasPrefix(line, "chain ") || strings.HasPrefix(line, "type ") {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

func normalizeRule(rule string) string {
	rule = strings.Join(strings.Fields(rule), " ")
	if strings.Contains(rule, " ip saddr ") || strings.Contains(rule, " ip daddr ") {
		rule = strings.ReplaceAll(rule, "meta nfproto ipv4 ", "")
	}
	if strings.Contains(rule, " ip6 saddr ") || strings.Contains(rule, " ip6 daddr ") {
		rule = strings.ReplaceAll(rule, "meta nfproto ipv6 ", "")
	}
	if strings.Contains(rule, " tcp sport ") || strings.Contains(rule, " tcp dport ") {
		rule = strings.ReplaceAll(rule, "meta l4proto tcp ", "")
	}
	if strings.Contains(rule, " udp sport ") || strings.Contains(rule, " udp dport ") {
		rule = strings.ReplaceAll(rule, "meta l4proto udp ", "")
	}
	rule = strings.ReplaceAll(rule, "ip6 nexthdr ipv6-icmp", "ip6 nexthdr icmpv6")
	rule = strings.ReplaceAll(rule, "meta l4proto ipv6 ", "meta l4proto "+ProtocolIPv6InIPv4+" ")
	rule = strings.ReplaceAll(rule, "limit rate 1/second burst 5 packets", "limit rate 1/second")
	rule = strings.ReplaceAll(rule, "meta priority & ffff:0 == 4e50:0", "meta priority & 0xffff0000 == 0x4e500000")
	rule = strings.ReplaceAll(rule, "/128 ", " ")
	rule = strings.ReplaceAll(rule, "/32 ", " ")
	for label, number := range dscpLabels {
		rule = strings.ReplaceAll(rule, " dscp "+label+" ", " dscp "+strconv.Itoa(number)+" ")
	}
	return markSetHex.ReplaceAllStringFunc(rule, func(match string) string {
		parts := strings.Split(match, " ")
		value, err := strconv.ParseUint(parts[len(parts)-1], 0, 32)
		if err != nil {
			return match
		}
		return "meta mark set " + strconv.FormatUint(value, 10)
	})
}

var markSetHex = regexp.MustCompile(`meta mark set 0x[0-9a-fA-F]+`)

var dscpLabels = map[string]int{
	"cs0": 0, "cs1": 8, "cs2": 16, "cs3": 24,
	"cs4": 32, "cs5": 40, "cs6": 48, "cs7": 56,
	"af11": 10, "af12": 12, "af13": 14,
	"af21": 18, "af22": 20, "af23": 22,
	"af31": 26, "af32": 28, "af33": 30,
	"af41": 34, "af42": 36, "af43": 38,
	"ef": 46, "va": 44, "le": 1,
}

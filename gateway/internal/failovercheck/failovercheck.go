// Package failovercheck validates failover preconditions for a loaded gateway
// configuration. The check-failover deploy mode and watchdog call the package
// with the same configuration type.
package failovercheck

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"

	"goodkind.io/mwan/internal/config"
)

const (
	portBits          = 16
	maxHostnameLength = 253
	maxLabelLength    = 63
)

var (
	errMissingLXCID      = errors.New("[failover] lxc_id is required")
	errMissingAgentAddr  = errors.New("[failover] agent_tcp_addr is required")
	errMissingVMID       = errors.New("mwan_vmid is required")
	errNoPrimaryEndpoint = errors.New(
		"no primary endpoint: set [watchdog] vsock_cid or mwan_agent_tcp_addr")
)

// Check returns one error per failed failover precondition in the loaded
// configuration and nil when every precondition passes.
// Check does not read [bgp]. Deploy mode check-failover and the watchdog's
// triggerFailover call Check. A configuration that passes check-failover
// also passes the watchdog's precondition check at failover time.
func Check(cfg *config.Config) []error {
	var failures []error

	if cfg.Failover.LXCID == "" {
		failures = append(failures, errMissingLXCID)
	}
	if cfg.MwanVMID == "" {
		failures = append(failures, errMissingVMID)
	}
	if cfg.Failover.LXCID != "" && cfg.Failover.LXCID == cfg.MwanVMID {
		failures = append(failures, fmt.Errorf(
			"[failover] lxc_id and mwan_vmid must differ; both are %q", cfg.MwanVMID))
	}

	if cfg.Failover.AgentTCPAddr == "" {
		failures = append(failures, errMissingAgentAddr)
	} else {
		addrErr := validateHostPort(cfg.Failover.AgentTCPAddr)
		if addrErr != nil {
			failures = append(failures, fmt.Errorf(
				"[failover] agent_tcp_addr %q is not a valid host:port: %w",
				cfg.Failover.AgentTCPAddr, addrErr))
		}
	}

	if cfg.Watchdog.VsockCID == 0 && cfg.Watchdog.MwanAgentTCPAddr == "" {
		failures = append(failures, errNoPrimaryEndpoint)
	}

	return failures
}

func validateHostPort(address string) error {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("split host and port: %s", err.Error())
	}
	if host == "" {
		return errors.New("host is empty")
	}
	if !isAddressOrHostname(host) {
		return errors.New("host is neither an ip address nor a dns hostname")
	}
	port, err := strconv.ParseUint(portText, 10, portBits)
	if err != nil {
		return fmt.Errorf("port %q: %s", portText, err.Error())
	}
	if port == 0 {
		return errors.New("port is zero")
	}
	return nil
}

func isAddressOrHostname(host string) bool {
	_, addrErr := netip.ParseAddr(host)
	if addrErr == nil {
		return true
	}
	if len(host) > maxHostnameLength {
		return false
	}
	for label := range strings.SplitSeq(host, ".") {
		if !isHostnameLabel(label) {
			return false
		}
	}
	return true
}

func isHostnameLabel(label string) bool {
	if label == "" || len(label) > maxLabelLength {
		return false
	}
	if label[0] == '-' || label[len(label)-1] == '-' {
		return false
	}
	for _, character := range label {
		isLetter := (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z')
		isDigit := character >= '0' && character <= '9'
		if !isLetter && !isDigit && character != '-' {
			return false
		}
	}
	return true
}

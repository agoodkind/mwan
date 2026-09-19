package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// GuestExecViaQm runs `qm guest exec` for the given VM with an agent-side
// timeout and returns the raw JSON response. The deploy gate uses it to read
// the guest boot_id from the hypervisor without the watchdog's richer
// channels; the integer vmid keeps the argv free of caller-shaped strings.
// Linux-only because its only caller, the deploy gate, runs on the Proxmox
// host.
func GuestExecViaQm(
	ctx context.Context,
	timeout time.Duration,
	agentTimeout time.Duration,
	vmid int,
	command ...string,
) ([]byte, error) {
	return runQm(ctx, timeout, qmGuestExecArgs(strconv.Itoa(vmid), agentTimeout, command)...)
}

// qmGuestExecArgs builds the `qm guest exec` argv. agentTimeout is how long qm
// waits for the command to exit; past it qm returns without an exit code.
func qmGuestExecArgs(
	vmid string, agentTimeout time.Duration, command []string,
) []string {
	args := []string{
		"guest", "exec", vmid,
		"--timeout", strconv.Itoa(int(agentTimeout.Seconds())),
		"--",
	}
	return append(args, command...)
}

// agentFlag reads one of the guest agent's flags, exited or out-truncated.
// `qm guest exec` prints them as 0 and 1; a JSON boolean is accepted too, so
// a Proxmox release that serializes them as booleans still parses.
type agentFlag bool

// UnmarshalJSON accepts a boolean, a number where non-zero means true, and
// null, which leaves the flag false.
func (f *agentFlag) UnmarshalJSON(data []byte) error {
	var asBool bool
	if err := json.Unmarshal(data, &asBool); err == nil {
		*f = agentFlag(asBool)
		return nil
	}
	var asNumber int
	if err := json.Unmarshal(data, &asNumber); err != nil {
		slog.Warn("qm guest exec flag is neither a boolean nor a number",
			"value", string(data), "err", err)
		return fmt.Errorf("guest agent flag %s: %w", data, err)
	}
	*f = asNumber != 0
	return nil
}

// qmGuestExecStatus is the part of what `qm guest exec` prints that a
// GuestExecResult carries. qm decodes out-data from the agent's base64 before
// printing, so it is plain text, and qm omits the key when the command printed
// nothing. out-truncated is set when the agent cut stdout short. When the
// command outlives the agent-side timeout, qm prints the pid with exited false
// and no exit code.
type qmGuestExecStatus struct {
	Exited       agentFlag `json:"exited"`
	ExitCode     *int      `json:"exitcode"`
	OutData      string    `json:"out-data"`
	OutTruncated agentFlag `json:"out-truncated"`
}

// qmExec runs args inside the guest through `qm guest exec`, which reaches the
// guest agent over QEMU's own channel rather than the guest's network. A guest
// agent that is down makes qm exit non-zero, and that is returned as an error
// so the caller records a failed attempt. A command that ran and exited
// non-zero is a result, not an error.
func (r *RealOps) qmExec(
	ctx context.Context, vmid string, args ...string,
) (GuestExecResult, error) {
	out, err := runQm(ctx, timeoutQmGuestExecWait,
		qmGuestExecArgs(vmid, timeoutQmGuestExec, args)...)
	if err != nil {
		r.log.ErrorContext(ctx, "qm guest exec failed",
			"vmid", vmid, "err", err,
			"output", strings.TrimSpace(string(out)))
		// runQm already names the command and its arguments, so this adds
		// only the output rather than repeating the prefix.
		return GuestExecResult{ExitCode: 1, Stdout: ""},
			fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	var status qmGuestExecStatus
	if err := json.Unmarshal(out, &status); err != nil {
		r.log.WarnContext(ctx, "qm guest exec output is not JSON",
			"vmid", vmid, "err", err,
			"output", strings.TrimSpace(string(out)))
		return GuestExecResult{ExitCode: 1, Stdout: ""},
			fmt.Errorf("parse qm guest exec output: %w", err)
	}
	if !status.Exited {
		return GuestExecResult{ExitCode: 1, Stdout: ""},
			fmt.Errorf("guest command %q did not exit within %s",
				strings.Join(args, " "), timeoutQmGuestExec)
	}
	if status.ExitCode == nil {
		return GuestExecResult{ExitCode: 1, Stdout: ""},
			errors.New("qm guest exec reported an exited command without an exit code")
	}
	// A caller parses Stdout, and a truncated stdout would parse as a wrong
	// value rather than fail, so the attempt fails instead.
	if status.OutTruncated {
		return GuestExecResult{ExitCode: 1, Stdout: ""},
			fmt.Errorf("guest command %q output was truncated by the guest agent",
				strings.Join(args, " "))
	}
	return GuestExecResult{ExitCode: *status.ExitCode, Stdout: status.OutData}, nil
}

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

	"goodkind.io/mwan/internal/config"
)

// GuestCommandResult includes stderr from commands executed through the hypervisor.
type GuestCommandResult struct {
	GuestExecResult
	Stderr string
}

// RunInGuest executes a command through the hypervisor. A guest command failure
// returns an exit status; a hypervisor execution failure returns an error.
// waitTimeout limits the hypervisor process. agentTimeout applies only to QEMU.
func RunInGuest(
	ctx context.Context,
	guestType config.GuestType,
	waitTimeout time.Duration,
	agentTimeout time.Duration,
	vmid int,
	command ...string,
) (GuestCommandResult, error) {
	driver := newGuestDriver(guestType)
	return driver.execGuest(
		ctx, slog.Default(), strconv.Itoa(vmid), waitTimeout, agentTimeout, command)
}

func failedGuestCommand() GuestCommandResult {
	return GuestCommandResult{
		GuestExecResult: GuestExecResult{ExitCode: 1, Stdout: ""},
		Stderr:          "",
	}
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
	ErrData      string    `json:"err-data"`
	OutTruncated agentFlag `json:"out-truncated"`
}

// execGuest uses the QEMU guest agent without requiring guest network access.
// It returns an error if qm cannot execute the command.
func (qemuGuest) execGuest(
	ctx context.Context,
	log *slog.Logger,
	vmid string,
	waitTimeout, agentTimeout time.Duration,
	command []string,
) (GuestCommandResult, error) {
	out, err := runQm(ctx, waitTimeout, qmGuestExecArgs(vmid, agentTimeout, command)...)
	if err != nil {
		log.ErrorContext(ctx, "qm guest exec failed",
			"vmid", vmid, "err", err,
			"output", strings.TrimSpace(string(out)))
		return failedGuestCommand(),
			fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	var status qmGuestExecStatus
	if err := json.Unmarshal(out, &status); err != nil {
		log.WarnContext(ctx, "qm guest exec output is not JSON",
			"vmid", vmid, "err", err,
			"output", strings.TrimSpace(string(out)))
		return failedGuestCommand(),
			fmt.Errorf("parse qm guest exec output: %w", err)
	}
	if !status.Exited {
		return failedGuestCommand(),
			fmt.Errorf("guest command %q did not exit within %s",
				strings.Join(command, " "), agentTimeout)
	}
	if status.ExitCode == nil {
		return failedGuestCommand(),
			errors.New("qm guest exec reported an exited command without an exit code")
	}
	// Reject truncated stdout before parsing a probe result.
	if status.OutTruncated {
		return failedGuestCommand(),
			fmt.Errorf("guest command %q output was truncated by the guest agent",
				strings.Join(command, " "))
	}
	return GuestCommandResult{
		GuestExecResult: GuestExecResult{ExitCode: *status.ExitCode, Stdout: status.OutData},
		Stderr:          status.ErrData,
	}, nil
}

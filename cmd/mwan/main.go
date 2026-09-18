package main

import (
	"fmt"
	"log/slog"
	"os"

	"goodkind.io/mwan/internal/agent"
	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/healthcheck"
	"goodkind.io/mwan/internal/version"
	"goodkind.io/mwan/internal/watchdog"
)

// subcommand is the typed enum of top-level mwan subcommands.
type subcommand string

const (
	subcmdAgent             subcommand = "agent"
	subcmdWatchdog          subcommand = "watchdog"
	subcmdIfmgr             subcommand = "ifmgr"
	subcmdHealth            subcommand = "health"
	subcmdNotify            subcommand = "notify"
	subcmdPD                subcommand = "pd"
	subcmdDebug             subcommand = "debug"
	subcmdTrace             subcommand = "trace-boot"
	subcmdGate              subcommand = "deploy-gate"
	subcmdWanconfigSelftest subcommand = "wanconfig-selftest"
	subcmdVersion           subcommand = "version"
)

// dispatchResult describes how dispatchConfigLess handled a subcommand.
// handled=true means the subcommand ran (caller exits with code).
// handled=false means the caller should fall through to the config-loading path.
type dispatchResult struct {
	handled bool
	code    int
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: mwan <agent|watchdog|health|ifmgr|notify|pd|debug|trace-boot|deploy-gate|wanconfig-selftest|version> [args]")
		os.Exit(1)
	}
	sub := os.Args[1]
	os.Args = append([]string{os.Args[0]}, os.Args[2:]...)

	// Boundary log: every mwan invocation is recorded with build identity
	// and the chosen subcommand, before subcommand-specific logger setup.
	// This is the single grep-anchor that links a binary on disk to the
	// session it executed in.
	slog.Info("mwan boundary",
		"build", version.BuildVersionString(),
		"subcommand", sub)

	if res := dispatchConfigLess(subcommand(sub)); res.handled {
		os.Exit(res.code)
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mwan: %v\n", err)
		os.Exit(1)
	}

	if code := dispatchWithConfig(sub, subcommand(sub), cfg); code != 0 {
		os.Exit(code)
	}
}

// dispatchConfigLess handles subcommands that do not load mwan config
// at the top level.
func dispatchConfigLess(sub subcommand) dispatchResult {
	switch sub {
	case subcmdHealth:
		if err := healthcheck.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "mwan health: %v\n", err)
			return dispatchResult{handled: true, code: 1}
		}
		return dispatchResult{handled: true, code: 0}
	case subcmdPD:
		return dispatchResult{handled: true, code: runPDProbe(os.Args[1:])}
	case subcmdTrace:
		return dispatchResult{handled: true, code: runTraceBoot()}
	case subcmdGate:
		return dispatchResult{handled: true, code: runDeployGate(os.Args[1:])}
	case subcmdWanconfigSelftest:
		return dispatchResult{handled: true, code: runWanconfigSelftest(os.Args[1:])}
	case subcmdVersion:
		return dispatchResult{handled: true, code: runVersion(os.Args[1:])}
	case subcmdAgent, subcmdWatchdog, subcmdIfmgr, subcmdNotify, subcmdDebug:
		return dispatchResult{handled: false}
	}
	return dispatchResult{handled: false}
}

// dispatchWithConfig handles subcommands that need a loaded mwan config.
// Returns the exit code; 0 means success. The helper prints the error
// itself so wrapcheck doesn't fire on the cross-package error returns.
func dispatchWithConfig(rawSub string, sub subcommand, cfg *config.Config) int {
	var runErr error
	switch sub {
	case subcmdAgent:
		runErr = agent.Run(cfg)
	case subcmdWatchdog:
		if len(os.Args) > 1 && os.Args[1] == "failover" {
			os.Args = append([]string{os.Args[0]}, os.Args[2:]...)
			runErr = watchdog.FailoverRun(cfg)
		} else {
			runErr = watchdog.Run(cfg)
		}
	case subcmdIfmgr:
		runErr = runIfMgr(cfg)
	case subcmdNotify:
		runErr = runNotify(cfg)
	case subcmdDebug:
		return runDebug(os.Args[1:], cfg)
	case subcmdHealth, subcmdPD, subcmdTrace, subcmdGate,
		subcmdWanconfigSelftest, subcmdVersion:
		fmt.Fprintf(os.Stderr, "internal dispatch error for subcommand %q\n", rawSub)
		return 1
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", rawSub)
		return 1
	}
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "mwan %s: %v\n", rawSub, runErr)
		return 1
	}
	return 0
}

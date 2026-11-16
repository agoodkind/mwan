package main

import (
	"fmt"
	"strconv"
	"time"
)

func parseDeployOperationOptions(
	mode operationMode,
	arguments []string,
	runtimePath string,
) (deployOperationOptions, error) {
	var options deployOperationOptions
	options.mode, options.runtimePath = mode, runtimePath
	if mode == operationArm {
		if len(arguments) != 1 {
			return options, fmt.Errorf("arm requires one manifest path")
		}

		options.manifestPath = arguments[0]

		return options, nil
	}

	if err := validateExistingOperationArguments(mode, arguments); err != nil {
		return options, err
	}

	options.operationID, options.generation = arguments[0], arguments[1]
	if mode == operationRelease {
		options.leaseID = arguments[2]
	}

	if mode == operationLease {
		options.phase = arguments[2]
		seconds, err := strconv.Atoi(arguments[3])
		if err != nil || seconds <= 0 {
			return options, fmt.Errorf("lease duration must be positive integer seconds")
		}

		options.leaseDuration = time.Duration(seconds) * time.Second
	}

	return options, nil
}

func validateExistingOperationArguments(mode operationMode, arguments []string) error {
	count := 2
	if mode == operationLease {
		count = 4
	}

	if mode == operationRelease {
		count = 3
	}

	if len(arguments) != count {
		return fmt.Errorf("deployment operation %s requires %d arguments", mode, count)
	}

	return nil
}

func parseWaitDeployArgs(arguments []string) (waitDeployInputs, error) {
	var options waitDeployInputs
	if len(arguments) != 9 {
		return options, fmt.Errorf("wait-deploy requires nine arguments")
	}

	vmid, err := parseVMID(arguments[0])
	if err != nil {
		return options, err
	}

	options.vmid = vmid
	options.oldBootID = arguments[1]
	if !bootIDPattern.MatchString(options.oldBootID) {
		return options, fmt.Errorf("old_boot_id must contain a lowercase boot UUID")
	}

	rebootBudget, err := parseBudgetSeconds(arguments[2])
	if err != nil {
		return options, err
	}

	options.rebootBudget = rebootBudget
	egressBudget, err := parseBudgetSeconds(arguments[3])
	if err != nil {
		return options, err
	}

	options.egressBudget = egressBudget
	options.traceID, options.verdictPath = arguments[4], arguments[5]
	if !traceIDPattern.MatchString(options.traceID) {
		return options, fmt.Errorf("trace_id must match %s", traceIDPattern.String())
	}

	families, err := parseRequiredEgressFamilies(arguments[6])
	if err != nil {
		return options, err
	}

	options.families = families
	rounds, err := parseConsecutiveRounds(arguments[7])
	if err != nil {
		return options, err
	}

	options.rounds = rounds
	probe, err := readDownstreamProbeConfig(arguments[8], families)
	if err != nil {
		return options, err
	}

	options.probe = probe

	return options, nil
}

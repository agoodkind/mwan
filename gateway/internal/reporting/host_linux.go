package reporting

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type cpuTicks struct{ total, idle uint64 }

type hostSampler struct {
	directory string
	previous  map[string]cpuTicks
}

func (sampler *hostSampler) sample() (HostUsage, error) {
	var usage HostUsage
	stat, err := os.ReadFile(filepath.Join(sampler.directory, "stat"))
	if err != nil {
		slog.Warn("read reporting CPU measurements failed", "proc_directory", sampler.directory, "error", err)
		return usage, fmt.Errorf("read CPU measurements: %w", err)
	}
	next := make(map[string]cpuTicks)
	for line := range strings.SplitSeq(string(stat), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || !strings.HasPrefix(fields[0], "cpu") {
			continue
		}
		var ticks cpuTicks
		for index, field := range fields[1:] {
			value, err := strconv.ParseUint(field, 10, 64)
			if err != nil {
				slog.Warn("parse reporting CPU ticks failed", "cpu", fields[0], "error", err)
				return usage, fmt.Errorf("parse %s ticks: %w", fields[0], err)
			}
			ticks.total += value
			if index == 3 {
				ticks.idle = value
			}
		}
		next[fields[0]] = ticks
		cpu := CPUUsage{Name: fields[0], Percent: 0, Available: false}
		previous, exists := sampler.previous[fields[0]]
		if exists && ticks.total > previous.total && ticks.idle >= previous.idle {
			total, idle := ticks.total-previous.total, ticks.idle-previous.idle
			if idle <= total {
				cpu.Percent = 100 * (1 - float64(idle)/float64(total))
				cpu.Available = true
			}
		}
		usage.CPU = append(usage.CPU, cpu)
	}
	if len(next) == 0 {
		return usage, fmt.Errorf("CPU measurements did not contain cpu counters")
	}
	sampler.previous = next
	meminfo, err := os.ReadFile(filepath.Join(sampler.directory, "meminfo"))
	if err != nil {
		slog.Warn("read reporting memory measurements failed", "proc_directory", sampler.directory, "error", err)
		return usage, fmt.Errorf("read memory measurements: %w", err)
	}
	values := make(map[string]uint64)
	for line := range strings.SplitSeq(string(meminfo), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || (fields[0] != "MemTotal:" && fields[0] != "MemAvailable:") {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			slog.Warn("parse reporting memory counter failed", "counter", fields[0], "error", err)
			return usage, fmt.Errorf("parse %s: %w", fields[0], err)
		}
		values[fields[0]] = value
	}
	total := values["MemTotal:"]
	available, exists := values["MemAvailable:"]
	if !exists || total == 0 || available > total {
		return usage, fmt.Errorf("memory measurements require valid MemTotal and MemAvailable counters")
	}
	usage.MemoryPercent = 100 * float64(total-available) / float64(total)
	usage.MemoryAvailable = true
	return usage, nil
}

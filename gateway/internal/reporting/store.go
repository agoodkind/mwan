package reporting

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/renameio/v2"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/connectionid"
	"goodkind.io/mwan/internal/networkjson"
)

type Store struct {
	settings config.ReportSection
	network  *networkjson.Config
	step     int64
	mu       sync.Mutex
	closed   bool
}

func OpenStore(settings config.ReportSection, network *networkjson.Config) (*Store, error) {
	if err := settings.Validate(); err != nil {
		return nil, err
	}
	if network == nil {
		return nil, errors.New("reporting history requires a network configuration")
	}
	if _, err := exec.LookPath(settings.RRDToolPath); err != nil {
		return nil, fmt.Errorf("install RRDtool at rrdtool_path: %w", err)
	}
	for _, directory := range []string{settings.StateDirectory, filepath.Join(settings.StateDirectory, "history"), filepath.Join(settings.StateDirectory, "rrd")} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, err
		}
	}
	duration, _ := time.ParseDuration(settings.SampleInterval)
	return &Store{settings: settings, network: network, step: int64(duration / time.Second)}, nil
}

func (store *Store) rrd(ctx context.Context, arguments ...string) ([]byte, error) {
	output, err := exec.CommandContext(ctx, store.settings.RRDToolPath, arguments...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("RRDtool %s: %w: %s", arguments[0], err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func (store *Store) wanPath(id connectionid.ID) string {
	digest := sha256.Sum256([]byte(id))
	return filepath.Join(store.settings.StateDirectory, "rrd", fmt.Sprintf("%x.rrd", digest))
}

func (store *Store) hostPath(name string) string {
	digest := sha256.Sum256([]byte(name))
	return filepath.Join(store.settings.StateDirectory, "rrd", fmt.Sprintf("host-%x.rrd", digest))
}

func probeName(key ProbeKey) string {
	target, mode := "g", "f"
	if key.Target == Cloudflare {
		target = "c"
	}
	if key.Mode == Persistent {
		mode = "p"
	}
	return fmt.Sprintf("%s%d%s", target, key.Family, mode)
}

func wanSources() []string {
	result := []string{"down", "up"}
	for _, family := range []Family{IPv4, IPv6} {
		for _, target := range []Target{Gateway, Cloudflare} {
			for _, mode := range []ProbeMode{Fresh, Persistent} {
				name := probeName(ProbeKey{Family: family, Target: target, Mode: mode})
				for _, suffix := range []string{"mean", "min", "peak", "loss", "dns"} {
					result = append(result, name+"_"+suffix)
				}
			}
		}
	}
	return result
}

func (store *Store) createRRD(ctx context.Context, path string, start time.Time, sources []string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	arguments := []string{"create", path, "--start", strconv.FormatInt(start.Unix()-1, 10), "--step", strconv.FormatInt(store.step, 10)}
	for _, source := range sources {
		arguments = append(arguments, fmt.Sprintf("DS:%s:GAUGE:%d:0:U", source, 2*store.step))
	}
	hourly := max(int64(1), 3600/store.step)
	daily := max(int64(1), 86400/store.step)
	for _, function := range []string{"AVERAGE", "MIN", "MAX"} {
		arguments = append(arguments, fmt.Sprintf("RRA:%s:0.5:1:1440", function), fmt.Sprintf("RRA:%s:0.5:%d:744", function, hourly), fmt.Sprintf("RRA:%s:0.5:%d:730", function, daily))
	}
	_, err := store.rrd(ctx, arguments...)
	return err
}

func sampleValue(value float64, available bool) string {
	if !available || math.IsNaN(value) || math.IsInf(value, 0) {
		return "U"
	}
	return strconv.FormatFloat(value, 'f', 9, 64)
}

func (store *Store) updateRRD(ctx context.Context, path string, minute Minute, sources []string, values map[string]string) error {
	if err := store.createRRD(ctx, path, minute.Start, sources); err != nil {
		return err
	}
	last, err := store.rrd(ctx, "last", path)
	if err != nil {
		return err
	}
	timestamp, err := strconv.ParseInt(strings.TrimSpace(string(last)), 10, 64)
	if err != nil {
		return err
	}
	if timestamp >= minute.End.Unix() {
		return nil
	}
	update := []string{strconv.FormatInt(minute.End.Unix(), 10)}
	for _, source := range sources {
		value, exists := values[source]
		if !exists {
			value = "U"
		}
		update = append(update, value)
	}
	_, err = store.rrd(ctx, "update", path, "--template", strings.Join(sources, ":"), strings.Join(update, ":"))
	return err
}

func (store *Store) Record(ctx context.Context, minute Minute) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed {
		return errors.New("reporting history is closed")
	}
	if !minute.End.After(minute.Start) {
		return errors.New("reporting interval end must follow its start")
	}
	if minute.End.Unix()%store.step != 0 {
		return errors.New("reporting interval end must align with sample_interval")
	}
	for _, id := range store.wanIDs() {
		values := make(map[string]string)
		for _, traffic := range minute.Traffic {
			if traffic.WAN != id {
				continue
			}
			values["down"] = sampleValue(float64(traffic.Downloaded)/minute.End.Sub(minute.Start).Seconds(), traffic.Available)
			values["up"] = sampleValue(float64(traffic.Uploaded)/minute.End.Sub(minute.Start).Seconds(), traffic.Available)
		}
		for _, probe := range minute.Probes {
			if probe.Key.WAN != id {
				continue
			}
			name := probeName(probe.Key)
			values[name+"_mean"] = sampleValue(float64(probe.LatencySum)/float64(time.Millisecond)/float64(probe.Replies), probe.Available && probe.Replies > 0)
			values[name+"_min"] = sampleValue(float64(probe.LatencyMin)/float64(time.Millisecond), probe.Available && probe.Replies > 0)
			values[name+"_peak"] = sampleValue(float64(probe.LatencyMax)/float64(time.Millisecond), probe.Available && probe.Replies > 0)
			values[name+"_loss"] = sampleValue(100*float64(probe.Lost)/float64(probe.Sent), probe.Available && probe.Sent > 0)
			values[name+"_dns"] = sampleValue(100*float64(probe.DNSFailures)/float64(probe.Sent), probe.Available && probe.Sent > 0)
		}
		if err := store.updateRRD(ctx, store.wanPath(id), minute, wanSources(), values); err != nil {
			return err
		}
	}
	for _, cpu := range minute.Host.CPU {
		if err := store.updateRRD(ctx, store.hostPath(cpu.Name), minute, []string{"usage"}, map[string]string{"usage": sampleValue(cpu.Percent, cpu.Available)}); err != nil {
			return err
		}
	}
	if err := store.updateRRD(ctx, store.hostPath("memory"), minute, []string{"usage"}, map[string]string{"usage": sampleValue(minute.Host.MemoryPercent, minute.Host.MemoryAvailable)}); err != nil {
		return err
	}
	data, err := json.Marshal(minute)
	if err != nil {
		return err
	}
	path := filepath.Join(store.settings.StateDirectory, "history", fmt.Sprintf("%d.json", minute.End.Unix()))
	return renameio.WriteFile(path, data, 0o600)
}

func (store *Store) wanIDs() []connectionid.ID {
	ids := make([]connectionid.ID, 0, len(store.network.WAN))
	for key, wan := range store.network.WAN {
		ids = append(ids, connectionid.Resolve(store.network.ConnectionIDs[key].String(), key, wan.Iface))
	}
	slices.Sort(ids)
	return ids
}

func (store *Store) history(start, end time.Time) ([]Minute, error) {
	entries, err := os.ReadDir(filepath.Join(store.settings.StateDirectory, "history"))
	if err != nil {
		return nil, err
	}
	var result []Minute
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		timestamp, err := strconv.ParseInt(strings.TrimSuffix(entry.Name(), ".json"), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid reporting history filename %s: %w", entry.Name(), err)
		}
		if timestamp <= start.Unix() || timestamp > end.Unix() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(store.settings.StateDirectory, "history", entry.Name()))
		if err != nil {
			return nil, err
		}
		var minute Minute
		if err := json.Unmarshal(data, &minute); err != nil {
			return nil, fmt.Errorf("read reporting history %s: %w", entry.Name(), err)
		}
		result = append(result, minute)
	}
	slices.SortFunc(result, func(first, second Minute) int { return first.End.Compare(second.End) })
	return result, nil
}

func (store *Store) periodEnd(after time.Time) time.Time {
	location, _ := time.LoadLocation(store.settings.TimeZone)
	local := after.In(location)
	end := time.Date(local.Year(), local.Month(), local.Day(), store.settings.SummaryHour, 0, 0, 0, location)
	if !end.After(after) {
		end = end.AddDate(0, 0, 1)
	}
	return end
}

func (store *Store) Close() error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.closed = true
	return nil
}

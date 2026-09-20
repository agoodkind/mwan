package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"text/template"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/installfile"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/networkjson"
)

// renderFS carries the templates for the three gateway files that are the
// program's own but hold a few site values. Each one is a file rather than a
// Go string, so a reviewer reads a sysctl file as a sysctl file.
//
//go:embed sysctl-mwan.conf.tmpl rt_tables.tmpl nghttpx-wanconfig.conf.tmpl
var renderFS embed.FS

const (
	// configTOMLPath is where the deploy writes the runtime configuration the
	// rendered files take their site values from.
	configTOMLPath = "/etc/mwan/config.toml"
	// sysctlMwanPath, rtTablesPath and nghttpxConfPath are the paths the
	// deploy has always written these files to. The agent hashes the first
	// two into the composite configuration hash by these exact names, so a
	// moved path would read as a file removed and a file added.
	sysctlMwanPath  = "/etc/sysctl.d/99-mwan.conf"
	rtTablesPath    = "/etc/iproute2/rt_tables"
	nghttpxConfPath = "/etc/nghttpx/wanconfig.conf"
	// rousetteBackendPort is the port rousette listens on. rousette takes no
	// address argument and binds ::1:10080 itself, which rousette.service's
	// own comment records, so this is the program's number rather than a
	// site's.
	rousetteBackendPort = 10080
)

// routingTable is one named routing table: the number the kernel uses and
// the name an operator types.
type routingTable struct {
	TableID int
	Name    string
}

// sysctlInputs are the site values the kernel tunable file carries.
type sysctlInputs struct {
	InternalIface         string
	DisableSLAACIfaces    []string
	DisableRPFilterIfaces []string
}

// rtTablesInputs are the site values the routing table file carries.
type rtTablesInputs struct {
	Providers      []routingTable
	ReservedTables []routingTable
}

// nghttpxInputs are the site values the front-end proxy configuration
// carries. RousettePort is the program's own backend port.
type nghttpxInputs struct {
	MgmtAddr     string
	RestconfPort int
	RousettePort int
}

// renderedFile is one rendered file and the host path it lands at.
type renderedFile struct {
	dest    string
	content []byte
}

// renderOutcome is what the rendering half of an install did.
type renderOutcome struct {
	// changed names each rendered file whose content the run replaced.
	changed []string
	// skipped explains each file the run left alone, one sentence each.
	skipped []string
	// applied names each sysctl key whose live value the run changed.
	applied []string
	// missing names each sysctl key the running kernel does not have.
	missing []string
}

// renderInputs is the closed set of value sets the three templates render
// from. Naming them keeps each template's inputs typed rather than passing an
// empty interface through the one rendering path.
type renderInputs interface {
	sysctlInputs | rtTablesInputs | nghttpxInputs
}

// renderGatewayFile renders one embedded template.
func renderGatewayFile[Inputs renderInputs](name string, data Inputs) ([]byte, error) {
	parsed, err := template.New(name).ParseFS(renderFS, name)
	if err != nil {
		return nil, installFailed("parse the template", name, err)
	}
	var out strings.Builder
	if err := parsed.Execute(&out, data); err != nil {
		return nil, installFailed("render the template", name, err)
	}
	return []byte(out.String()), nil
}

// providerTables lists each provider's routing table, ordered by table
// number. The network configuration holds the providers in a map, so the
// order has to come from the data rather than from iteration; the table
// number is what the file is a list of, so the file reads in that order.
func providerTables(network *networkjson.Config) []routingTable {
	tables := make([]routingTable, 0, len(network.WAN))
	for name, entry := range network.WAN {
		tables = append(tables, routingTable{TableID: entry.TableID, Name: name})
	}
	sortRoutingTables(tables)
	return tables
}

// reservedTables lists the tables held for something other than a provider,
// in the same order.
func reservedTables(reserved map[string]int) []routingTable {
	tables := make([]routingTable, 0, len(reserved))
	for name, id := range reserved {
		tables = append(tables, routingTable{TableID: id, Name: name})
	}
	sortRoutingTables(tables)
	return tables
}

// sortRoutingTables orders by number, then by name so two tables that share
// a number still render in a fixed order rather than a random one.
func sortRoutingTables(tables []routingTable) {
	slices.SortFunc(tables, func(a routingTable, b routingTable) int {
		if a.TableID != b.TableID {
			return a.TableID - b.TableID
		}
		return strings.Compare(a.Name, b.Name)
	})
}

// gatewayRenders renders the files the wan role owns from the loaded
// configuration. A file whose site values the rendered config.toml does not
// carry yet is reported as a skip instead of rendered, so a gateway keeps the
// file its deploy wrote until the inventory names those values.
func gatewayRenders(
	cfg *config.Config, network *networkjson.Config,
) ([]renderedFile, []string, error) {
	files := make([]renderedFile, 0, 3)
	var skipped []string

	if cfg.Sysctl == nil {
		skipped = append(skipped,
			"skipped "+sysctlMwanPath+": config.toml carries no [sysctl] table")
	} else {
		content, err := renderGatewayFile("sysctl-mwan.conf.tmpl", sysctlInputs{
			InternalIface:         network.InternalIface,
			DisableSLAACIfaces:    cfg.Sysctl.DisableSLAACIfaces,
			DisableRPFilterIfaces: cfg.Sysctl.DisableRPFilterIfaces,
		})
		if err != nil {
			return nil, nil, err
		}
		files = append(files, renderedFile{dest: sysctlMwanPath, content: content})
	}

	if cfg.Routing == nil || len(cfg.Routing.ReservedTables) == 0 {
		skipped = append(skipped,
			"skipped "+rtTablesPath+": config.toml carries no [routing.reserved_tables] table")
	} else {
		content, err := renderGatewayFile("rt_tables.tmpl", rtTablesInputs{
			Providers:      providerTables(network),
			ReservedTables: reservedTables(cfg.Routing.ReservedTables),
		})
		if err != nil {
			return nil, nil, err
		}
		files = append(files, renderedFile{dest: rtTablesPath, content: content})
	}

	if cfg.Wanconfig.RestconfPort == 0 {
		skipped = append(skipped,
			"skipped "+nghttpxConfPath+": config.toml carries no [wanconfig] restconf_port")
	} else {
		content, err := renderGatewayFile("nghttpx-wanconfig.conf.tmpl", nghttpxInputs{
			MgmtAddr:     cfg.MwanMgmtAddr,
			RestconfPort: cfg.Wanconfig.RestconfPort,
			RousettePort: rousetteBackendPort,
		})
		if err != nil {
			return nil, nil, err
		}
		files = append(files, renderedFile{dest: nghttpxConfPath, content: content})
	}

	return files, skipped, nil
}

// installRendered renders the wan role's files and writes each one that
// differs from what is on disk, then applies the kernel tunables it wrote. It
// returns what it did even when it fails, so the caller reports the files
// written before the failure.
//
// A run under a root writes the files and applies nothing: its files are not
// the ones this machine's kernel is configured from.
func installRendered(
	ctx context.Context, log *slog.Logger, root string, rooted bool,
) (renderOutcome, error) {
	outcome := renderOutcome{changed: nil, skipped: nil, applied: nil, missing: nil}
	cfg, err := config.LoadFrom(filepath.Join(root, configTOMLPath))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			outcome.skipped = append(outcome.skipped,
				"skipped the rendered files: "+configTOMLPath+" is not on the host yet")
			return outcome, nil
		}
		return outcome, installFailed("read", configTOMLPath, err)
	}
	networkPath := filepath.Join(root, networkjson.DefaultPath)
	network, err := networkjson.Load(networkPath, filepath.Join(root, networkjson.DefaultSchemaDir))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			outcome.skipped = append(outcome.skipped,
				"skipped the rendered files: "+networkjson.DefaultPath+" is not on the host yet")
			return outcome, nil
		}
		return outcome, installFailed("read", networkjson.DefaultPath, err)
	}

	files, skipped, err := gatewayRenders(cfg, network)
	outcome.skipped = skipped
	if err != nil {
		return outcome, err
	}
	var sysctlContent []byte
	for _, file := range files {
		path := filepath.Join(root, file.dest)
		changed, writeErr := installfile.Write(path, file.content, systemdUnitMode)
		if writeErr != nil {
			return outcome, installFailed("install the file", file.dest, writeErr)
		}
		if changed {
			outcome.changed = append(outcome.changed, path)
		}
		if file.dest == sysctlMwanPath {
			sysctlContent = file.content
		}
	}
	if rooted || sysctlContent == nil {
		return outcome, nil
	}
	runner := netif.NewProcSysctlRunner(log, false)
	applied, missing, err := applySysctl(ctx, runner, parseSysctlSettings(sysctlContent))
	outcome.applied = applied
	outcome.missing = missing
	return outcome, err
}

// sysctlSetting is one key a sysctl file sets and the value it sets it to.
type sysctlSetting struct {
	Key   string
	Value string
}

// parseSysctlSettings reads the settings a sysctl.d file carries, in file
// order, the way systemd-sysctl reads them: a line whose first non-blank
// character is # or ; is a comment, and every other non-blank line is
// key = value.
//
// A trailing comment on a value line is dropped, which systemd does not do.
// The gateway's file carries one, and keeping it would make the value read
// back differ from the value written on every run, so the run would never
// settle. The number in front of it is what the line means either way.
func parseSysctlSettings(content []byte) []sysctlSetting {
	var settings []sysctlSetting
	for raw := range strings.SplitSeq(string(content), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		if comment := strings.Index(value, "#"); comment >= 0 {
			value = value[:comment]
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		settings = append(settings, sysctlSetting{Key: key, Value: value})
	}
	return settings
}

// applySysctl writes each setting whose live value differs from the file's,
// and reports the keys it changed and the keys the kernel does not have.
//
// It reads before it writes, because writing a tunable is not always the
// no-op its unchanged value suggests: writing net.ipv6.conf.all.forwarding
// walks every interface in the kernel whatever the value was. Reading first
// makes a converged gateway a true no-op.
//
// A key the kernel does not have is reported and skipped rather than failing
// the run. The install runs before the deploy writes this gateway's networkd
// files, so on a first deploy an interface named here does not exist yet, and
// systemd-sysctl applies the same file at boot with the same tolerance.
func applySysctl(
	ctx context.Context, runner netif.SysctlRunner, settings []sysctlSetting,
) (applied []string, missing []string, err error) {
	for _, setting := range settings {
		live, getErr := runner.Get(ctx, setting.Key)
		if getErr != nil {
			if errors.Is(getErr, fs.ErrNotExist) {
				missing = append(missing, setting.Key)
				continue
			}
			return applied, missing, installFailed("read the sysctl", setting.Key, getErr)
		}
		if sameSysctlValue(live, setting.Value) {
			continue
		}
		if setErr := runner.Set(ctx, setting.Key, setting.Value); setErr != nil {
			return applied, missing, installFailed("write the sysctl", setting.Key, setErr)
		}
		applied = append(applied, fmt.Sprintf("%s = %s", setting.Key, setting.Value))
	}
	return applied, missing, nil
}

// sameSysctlValue compares a value read from the kernel with a value from
// the file. A multi-value tunable reads back tab separated and is written
// space separated, so the fields are compared rather than the text.
func sameSysctlValue(live string, want string) bool {
	return slices.Equal(strings.Fields(live), strings.Fields(want))
}

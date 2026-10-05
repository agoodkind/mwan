package installspec

import (
	"path/filepath"
	"strings"
)

const (
	// BinaryPath is the install path of the mwan binary. The ExecStart lines
	// of the embedded units start it by this path.
	BinaryPath = "/usr/local/bin/mwan"

	// NACMModule is the sysrepo module the NACM policy configures.
	NACMModule = "ietf-netconf-acm"

	unitSuffix        = ".service"
	dropInDirSuffix   = ".d"
	instanceSeparator = "@"
	oneshotType       = "Type=oneshot"
	remainAfterExit   = "RemainAfterExit=yes"
)

// Datastore is the name of a sysrepo datastore.
type Datastore string

const (
	// DatastoreStartup is the datastore sysrepo loads into running at start.
	DatastoreStartup Datastore = "startup"
	// DatastoreRunning is the running configuration datastore.
	DatastoreRunning Datastore = "running"
)

// SysrepoImport is one configuration import into a sysrepo datastore.
type SysrepoImport struct {
	// Datastore receives the import.
	Datastore Datastore
	// Module is the module that the import replaces the whole configuration of.
	Module string
	// Content is the XML document to import.
	Content []byte
}

// SysrepoImports lists the imports the role makes, in import order. Startup
// comes first, then running, the order the deploy imported in. Only the role
// with a datastore has any.
func (s Spec) SysrepoImports() ([]SysrepoImport, error) {
	if !s.Schema {
		return nil, nil
	}
	policy, err := NACMPolicy()
	if err != nil {
		return nil, err
	}
	imports := make([]SysrepoImport, 0, 2)
	for _, datastore := range []Datastore{DatastoreStartup, DatastoreRunning} {
		imports = append(imports, SysrepoImport{
			Datastore: datastore,
			Module:    NACMModule,
			Content:   policy,
		})
	}
	return imports, nil
}

// Unit is one unit the role enables.
type Unit struct {
	// Name is the unit name.
	Name string
	// Enabled is true for every listed unit, because `mwan install` enables each.
	Enabled bool
	// Active is the state the deploy expects after the install. It is false for
	// a oneshot unit without RemainAfterExit, which exits after it runs.
	Active bool
	// Files are the host paths of the role's files that the unit reads: its own
	// unit file, or the template of an instance, and the drop-ins of the unit
	// and of the template.
	Files []string
}

// Units lists the units of the role: the enabled units in enable order, then
// the not-owned units. Both kinds are enabled and active. Only Enable feeds
// `mwan install`.
func (s Spec) Units() ([]Unit, error) {
	units := make([]Unit, 0, len(s.Enable)+len(s.NotOwned))
	for _, name := range s.Enable {
		unit, err := s.unitFor(name)
		if err != nil {
			return nil, err
		}
		units = append(units, unit)
	}
	for _, notOwned := range s.NotOwned {
		units = append(units, Unit{Name: notOwned.Name, Enabled: true, Active: true, Files: notOwned.Files})
	}
	return units, nil
}

func (s Spec) unitFor(name string) (Unit, error) {
	names := []string{name}
	if before, after, found := strings.Cut(name, instanceSeparator); found && strings.HasSuffix(after, unitSuffix) {
		names = append(names, before+instanceSeparator+unitSuffix)
	}
	unit := Unit{Name: name, Enabled: true, Active: true, Files: []string{}}
	for _, file := range s.Files {
		for _, candidate := range names {
			isUnitFile := file.Dest == filepath.Join(SystemdUnitDir, candidate)
			isDropIn := filepath.Dir(file.Dest) == filepath.Join(SystemdUnitDir, candidate+dropInDirSuffix)
			if !isUnitFile && !isDropIn {
				continue
			}
			unit.Files = append(unit.Files, file.Dest)
			if !isUnitFile {
				continue
			}
			content, err := Read(file.Embedded)
			if err != nil {
				return Unit{}, err
			}
			unit.Active = !exitsAfterRun(string(content))
		}
	}
	return unit, nil
}

// exitsAfterRun reports whether a unit file declares a oneshot service that
// does not stay active after its command exits.
func exitsAfterRun(unitFile string) bool {
	oneshot := false
	remains := false
	for line := range strings.SplitSeq(unitFile, "\n") {
		switch strings.TrimSpace(line) {
		case oneshotType:
			oneshot = true
		case remainAfterExit:
			remains = true
		}
	}
	return oneshot && !remains
}

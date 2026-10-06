package installspec

import (
	"path/filepath"
	"strings"
)

const (
	// BinaryPath must match the path used by the embedded service commands.
	BinaryPath = "/usr/local/bin/mwan"

	// NACMModule selects the datastore module configured by the access policy.
	NACMModule = "ietf-netconf-acm"

	unitSuffix        = ".service"
	dropInDirSuffix   = ".d"
	instanceSeparator = "@"
	oneshotType       = "Type=oneshot"
	remainAfterExit   = "RemainAfterExit=yes"
)

// Datastore selects a sysrepo configuration datastore.
type Datastore string

const (
	// DatastoreStartup stores the configuration that sysrepo loads into running at startup.
	DatastoreStartup Datastore = "startup"
	// DatastoreRunning contains the active configuration.
	DatastoreRunning Datastore = "running"
)

// SysrepoImport specifies a complete module configuration for one datastore.
type SysrepoImport struct {
	// Datastore selects the import destination.
	Datastore Datastore
	// Module selects the configuration for replacement.
	Module string
	// Content contains the XML document for the import.
	Content []byte
}

// SysrepoImports returns NACM imports when Schema is enabled.
// The startup import precedes the running import.
func (s Spec) SysrepoImports(policy []byte) []SysrepoImport {
	if !s.Schema {
		return nil
	}
	imports := make([]SysrepoImport, 0, 2)
	for _, datastore := range []Datastore{DatastoreStartup, DatastoreRunning} {
		imports = append(imports, SysrepoImport{
			Datastore: datastore,
			Module:    NACMModule,
			Content:   policy,
		})
	}
	return imports
}

// Unit specifies a service's desired state and configuration paths.
type Unit struct {
	Name    string
	Enabled bool
	// Active is false for oneshot services without RemainAfterExit.
	Active bool
	// Files includes the unit or instance template and their drop-ins.
	Files []string
}

// Units returns the services in Enable followed by the services in NotOwned.
// The installer enables only the services in Enable.
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

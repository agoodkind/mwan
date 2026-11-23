// Package commandconfig defines shared configuration flag and loader helpers
// for gateway commands.
package commandconfig

import "github.com/spf13/pflag"

const configFlagDescription = "Read TOML from this path. The default is " +
	"MWAN_CONFIG or /etc/mwan/config.toml. The first --config value takes precedence."

// AddFlag registers the shared --config flag.
//
// The first supplied path replaces the default. Later values do not replace
// it.
//
// Example:
//
//	commandconfig.AddFlag(command.Flags(), &path)
func AddFlag(flags *pflag.FlagSet, path *string) {
	flags.Var(
		&firstPath{path: path, set: false},
		"config",
		configFlagDescription,
	)
}

type firstPath struct {
	path *string
	set  bool
}

func (value *firstPath) String() string { return *value.path }
func (value *firstPath) Type() string   { return "string" }

func (value *firstPath) Set(path string) error {
	if !value.set {
		*value.path = path
		value.set = true
	}

	return nil
}

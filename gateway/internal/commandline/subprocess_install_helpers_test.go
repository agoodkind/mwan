package commandline

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"

	"goodkind.io/mwan/gateway/internal/yangpub"
)

// Sysrepo selects one repository per process. Use separate children for install and
// inspection.
const (
	childMainEnv    = "MWAN_INSTALL_TEST_MAIN"
	childSysrepoEnv = "MWAN_INSTALL_TEST_SYSREPO"
)

func TestMain(m *testing.M) {
	if os.Getenv(childMainEnv) != "" {
		os.Exit(Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, slog.Default()))
	}

	if step := os.Getenv(childSysrepoEnv); step != "" {
		os.Exit(runSysrepoStep(step, os.Args[1:]))
	}

	code := m.Run()
	if err := removeTestBinary(); err != nil {
		fmt.Fprintf(os.Stderr, "remove test binary: %v\n", err)
		if code == 0 {
			code = 1
		}
	}

	os.Exit(code)
}

func runSysrepoStep(step string, args []string) int {
	// Let the parent remove shared memory under the prefix it assigned.
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	datastore, err := yangpub.New(log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect: %v\n", err)

		return 1
	}

	defer func() { _ = datastore.Close() }()
	switch step {
	case "library":
		tree, found, err := datastore.ExportJSON(
			context.Background(),
			yangpub.DatastoreOperational,
			"/ietf-yang-library:yang-library",
		)
		if err != nil || !found {
			fmt.Fprintf(os.Stderr, "read the yang library: found=%v err=%v\n", found, err)

			return 1
		}

		fmt.Fprint(os.Stdout, tree)

		return 0
	case "export":
		tree, _, err := datastore.ExportJSON(
			context.Background(),
			yangpub.Datastore(args[0]),
			args[1],
		)
		if err != nil {
			fmt.Fprintf(os.Stderr, "export %s from %s: %v\n", args[1], args[0], err)

			return 1
		}

		fmt.Fprint(os.Stdout, tree)

		return 0
	}

	fmt.Fprintf(os.Stderr, "unknown step %q\n", step)

	return 1
}

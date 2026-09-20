// Command render writes the systemd-networkd unit files the daemon would
// write for one network configuration file into a directory of the caller's
// choosing. It exists for one comparison between those files and the
// hand-authored templates in the configs repository. The files it produces
// are the daemon's files: it loads the document through the same loader and
// renders through the same package the daemon uses. No deploy installs this
// program, and the mwan binary has no subcommand for it.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"goodkind.io/mwan/internal/networkd"
	"goodkind.io/mwan/internal/networkjson"
	"goodkind.io/mwan/internal/yangpub"
)

// options are the program's inputs: the document to load and the directory
// that receives every rendered file.
type options struct {
	networkJSON string
	outDir      string
}

func parseFlags(log *slog.Logger, args []string) (options, error) {
	flags := flag.NewFlagSet("render", flag.ContinueOnError)
	networkJSON := flags.String("network", "", "network.json to load")
	outDir := flags.String("out", "", "directory that receives the rendered unit files")
	if err := flags.Parse(args); err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			log.Error("render: bad arguments", "err", err)
		}
		return options{}, fmt.Errorf("parse flags: %w", err)
	}
	opts := options{networkJSON: *networkJSON, outDir: *outDir}
	if opts.networkJSON == "" || opts.outDir == "" {
		err := errors.New("-network and -out are required")
		log.Error("render: bad arguments", "err", err)
		return options{}, err
	}
	return opts, nil
}

// render validates and loads the document against the schema the binary
// embeds, the same modules the daemon validates against, and writes every
// rendered provider's files into the output directory. It returns the
// changes WriteDir reported.
func render(log *slog.Logger, opts options) ([]networkd.Change, error) {
	schemaDir, err := os.MkdirTemp("", "mwan-render-schema-")
	if err != nil {
		log.Error("render: creating the schema directory failed", "err", err)
		return nil, fmt.Errorf("create schema directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(schemaDir) }()
	if _, err := yangpub.WriteSchema(schemaDir); err != nil {
		log.Error("render: writing the embedded schema failed", "dir", schemaDir, "err", err)
		return nil, fmt.Errorf("write schema: %w", err)
	}
	loaded, err := networkjson.Load(opts.networkJSON, schemaDir)
	if err != nil {
		log.Error("render: loading the network configuration failed", "path", opts.networkJSON, "err", err)
		return nil, fmt.Errorf("load %s: %w", opts.networkJSON, err)
	}
	if err := os.MkdirAll(opts.outDir, 0o750); err != nil {
		log.Error("render: creating the output directory failed", "dir", opts.outDir, "err", err)
		return nil, fmt.Errorf("create %s: %w", opts.outDir, err)
	}
	changes, err := networkd.WriteDir(opts.outDir, loaded.Links)
	if err != nil {
		log.Error("render: writing the unit files failed", "dir", opts.outDir, "err", err)
		return nil, fmt.Errorf("write unit files into %s: %w", opts.outDir, err)
	}
	return changes, nil
}

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	opts, err := parseFlags(log, os.Args[1:])
	if err != nil {
		os.Exit(2)
	}
	changes, err := render(log, opts)
	if err != nil {
		os.Exit(1)
	}
	for _, change := range changes {
		fmt.Fprintf(os.Stdout, "%s interface=%s kind=%s removed=%t\n",
			change.File, change.Interface, change.Kind, change.Removed)
	}
}

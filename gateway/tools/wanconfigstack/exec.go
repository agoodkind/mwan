package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/sys/execabs"
)

type program int

const (
	programAptGet program = iota
	programApkg
	programCmake
	programSysrepoctl
	programRousette
)

const (
	aptGetPath     = "apt-get"
	apkgPath       = buildRoot + "/apkg-venv/bin/apkg"
	cmakePath      = "cmake"
	sysrepoctlPath = "/usr/bin/sysrepoctl"
	rousettePath   = stackPrefix + "/bin/rousette"
)

var errUnknownProgram = errors.New("unknown program")

// Fixed program identifiers prevent input from selecting an executable.
func (p program) process(ctx context.Context, args []string) (*exec.Cmd, error) {
	switch p {
	case programAptGet:
		return execabs.CommandContext(ctx, aptGetPath, args...), nil
	case programApkg:
		return execabs.CommandContext(ctx, apkgPath, args...), nil
	case programCmake:
		return execabs.CommandContext(ctx, cmakePath, args...), nil
	case programSysrepoctl:
		return execabs.CommandContext(ctx, sysrepoctlPath, args...), nil
	case programRousette:
		return execabs.CommandContext(ctx, rousettePath, args...), nil
	}

	return nil, errUnknownProgram
}

type command struct {
	program program
	args    []string
	dir     string
	env     []string
}

func cmd(p program, args ...string) command {
	return command{program: p, args: args, dir: "", env: nil}
}

func (c command) in(dir string) command {
	c.dir = dir
	return c
}

func (c command) with(env ...string) command {
	c.env = append(c.env, env...)
	return c
}

func (b *builder) fail(ctx context.Context, msg string, err error, attrs ...slog.Attr) error {
	b.log.LogAttrs(
		ctx,
		slog.LevelError,
		"wanconfigstack: "+msg,
		append(attrs, slog.Any("err", err))...,
	)

	return fmt.Errorf("%s: %w", msg, err)
}

// Preserve compiler and packaging diagnostics in the container's stderr.
func (b *builder) run(ctx context.Context, c command) error {
	argv := strings.Join(c.args, " ")
	proc, err := c.program.process(ctx, c.args)
	if err != nil {
		return b.fail(ctx, "resolve program", err)
	}

	b.log.InfoContext(ctx, "wanconfigstack: run", "program", proc.Path, "args", argv, "dir", c.dir)
	proc.Dir = c.dir
	proc.Env = append(os.Environ(), c.env...)
	proc.Stdout = os.Stderr
	proc.Stderr = os.Stderr
	if err := proc.Run(); err != nil {
		return b.fail(
			ctx,
			"command failed",
			err,
			slog.String("program", proc.Path),
			slog.String("args", argv),
		)
	}

	return nil
}

func (b *builder) runAll(ctx context.Context, commands ...command) error {
	for _, c := range commands {
		if err := b.run(ctx, c); err != nil {
			return err
		}
	}

	return nil
}

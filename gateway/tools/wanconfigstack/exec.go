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

// program is one of the external programs the build runs, the ones with no
// library: apt, the apkg packaging tool and its Python environment, and
// cmake. Each resolves to a fixed path or name at the exec call.
type program int

const (
	programAptGet program = iota
	programPython3
	programPip
	programApkg
	programCmake
	programSysrepoctl
	programRousette
)

const (
	aptGetPath     = "apt-get"
	python3Path    = "python3"
	pipPath        = buildRoot + "/apkg-venv/bin/pip"
	apkgPath       = buildRoot + "/apkg-venv/bin/apkg"
	cmakePath      = "cmake"
	sysrepoctlPath = "/usr/bin/sysrepoctl"
	rousettePath   = stackPrefix + "/bin/rousette"
)

var errUnknownProgram = errors.New("unknown program")

// process builds the [exec.Cmd] for the program. Each case names its
// program as a constant, so the command line is never assembled from data.
func (p program) process(ctx context.Context, args []string) (*exec.Cmd, error) {
	switch p {
	case programAptGet:
		return execabs.CommandContext(ctx, aptGetPath, args...), nil
	case programPython3:
		return execabs.CommandContext(ctx, python3Path, args...), nil
	case programPip:
		return execabs.CommandContext(ctx, pipPath, args...), nil
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

// command is one external invocation with its arguments, working
// directory, and extra environment.
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

// fail logs an error at the point it is known and returns it wrapped with
// the same message, so every failure is both in the build log and in the
// error the caller sees.
func (b *builder) fail(ctx context.Context, msg string, err error, attrs ...slog.Attr) error {
	b.log.LogAttrs(ctx, slog.LevelError, "wanconfigstack: "+msg, append(attrs, slog.Any("err", err))...)
	return fmt.Errorf("%s: %w", msg, err)
}

// run runs one command, streaming its output to stderr so the build log
// carries the compiler and packaging output verbatim.
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
		return b.fail(ctx, "command failed", err, slog.String("program", proc.Path), slog.String("args", argv))
	}
	return nil
}

// runAll runs commands in order and stops at the first failure.
func (b *builder) runAll(ctx context.Context, commands ...command) error {
	for _, c := range commands {
		if err := b.run(ctx, c); err != nil {
			return err
		}
	}
	return nil
}

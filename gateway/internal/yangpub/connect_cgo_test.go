package yangpub_test

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/yangpub"
)

const (
	connectChildEnv      = "MWAN_YANGPUB_CONNECT_CHILD"
	connectChildFailed   = 3
	missingGroupName     = "sysrepo"
	sysrepoTestModeEnv   = "SR_ENV_RUN_TESTS"
	sysrepoRepositoryEnv = "SYSREPO_REPOSITORY_PATH"
	sysrepoShmPrefixEnv  = "SYSREPO_SHM_PREFIX"
	sharedMemoryDir      = "/dev/shm"
)

func TestMain(m *testing.M) {
	if os.Getenv(connectChildEnv) != "" {
		os.Exit(runConnectChild())
	}
	os.Exit(m.Run())
}

func runConnectChild() int {
	publisher, err := yangpub.New(slog.New(slog.DiscardHandler))
	if err != nil {
		fmt.Fprint(os.Stdout, err.Error())
		return connectChildFailed
	}
	if err := publisher.Close(); err != nil {
		fmt.Fprint(os.Stdout, err.Error())
		return connectChildFailed
	}
	return 0
}

func TestNewWithoutSysrepoGroupReturnsErrorNamingTheGroup(t *testing.T) {
	t.Parallel()
	if _, err := user.LookupGroup(missingGroupName); err == nil {
		t.Skip("the sysrepo group exists on this host; the missing-group case cannot run")
	}
	repository := t.TempDir()
	shmPrefix := fmt.Sprintf("yangpubgroup%d_", os.Getpid())
	t.Cleanup(func() {
		segments, err := filepath.Glob(filepath.Join(sharedMemoryDir, shmPrefix+"*"))
		if err != nil {
			t.Errorf("list shared memory: %v", err)
			return
		}
		for _, segment := range segments {
			if err := os.Remove(segment); err != nil {
				t.Errorf("remove shared memory %s: %v", segment, err)
			}
		}
	})

	command := exec.Command(os.Args[0])
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, sysrepoTestModeEnv+"=") {
			continue
		}
		command.Env = append(command.Env, entry)
	}
	command.Env = append(command.Env,
		connectChildEnv+"=1",
		sysrepoRepositoryEnv+"="+repository,
		sysrepoShmPrefixEnv+"="+shmPrefix,
	)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	runErr := command.Run()

	var exitErr *exec.ExitError
	if !errors.As(runErr, &exitErr) || exitErr.ExitCode() != connectChildFailed {
		t.Fatalf("child run error = %v, want exit code %d\nstdout:\n%s\nstderr:\n%s",
			runErr, connectChildFailed, stdout.String(), stderr.String())
	}
	want := `group "` + missingGroupName + `" does not exist`
	if !strings.Contains(stdout.String(), want) {
		t.Fatalf("New error = %q, want it to contain %q", stdout.String(), want)
	}
}

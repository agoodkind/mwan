package config_test

import (
	"strings"
	"testing"
	"time"

	"goodkind.io/mwan/internal/config"
)

const nativeProcessTOML = `
[ifmgr.modules.nativeoffload]
enabled = true
cpu = 0
memory_mib = 256
hugepage_directory = "/dev/hugepages"
file_prefix = "mwan-native-test"
`

func TestLoadNativeOffloadProcessSettings(t *testing.T) {
	t.Parallel()
	path := writeSharedFile(t, sharedFileText+nativeProcessTOML)
	loaded, _, err := config.LoadArguments([]string{"mwan", "--config", path})
	if err != nil {
		t.Fatal(err)
	}
	settings, err := loaded.IfMgr.Modules.NativeOffload.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	want := config.NativeOffloadProcessSettings{
		CPU: 0, MemoryMiB: 256, HugepageDirectory: "/dev/hugepages", FilePrefix: "mwan-native-test",
		PairCapacity: 1024, MbufCount: 8192, FlowCapacity: 4096, EventCapacity: 1024,
		ObservationCapacity: 4096, SocketBufferBytes: 1048576,
		ObservationMaxAge: 500 * time.Millisecond, RequestTimeout: 500 * time.Millisecond,
		ResyncInterval: 30 * time.Second, CounterInterval: time.Second,
		ResyncTimeout: 3 * time.Second, ShutdownTimeout: 10 * time.Second,
		IdleTimeout: 30 * time.Second, IdleWait: 10 * time.Microsecond,
	}
	if settings != want {
		t.Fatalf("resolved settings = %+v, want %+v", settings, want)
	}
}

func TestLoadNativeOffloadObservationDiagnostics(t *testing.T) {
	t.Parallel()
	for _, check := range []struct {
		name  string
		input string
		want  bool
	}{
		{name: "omitted", input: "", want: false},
		{name: "enabled", input: "observation_diagnostics = true\n", want: true},
		{name: "disabled", input: "observation_diagnostics = false\n", want: false},
	} {
		t.Run(check.name, func(t *testing.T) {
			t.Parallel()
			path := writeSharedFile(t, sharedFileText+nativeProcessTOML+check.input)
			loaded, _, err := config.LoadArguments([]string{"mwan", "--config", path})
			if err != nil {
				t.Fatal(err)
			}
			settings, err := loaded.IfMgr.Modules.NativeOffload.Resolve()
			if err != nil || settings.ObservationDiagnostics != check.want {
				t.Fatalf("resolved observation diagnostics = %v, want %v: %v", settings.ObservationDiagnostics, check.want, err)
			}
		})
	}
}

func TestLoadNativeOffloadRejectsInvalidSettings(t *testing.T) {
	t.Parallel()
	for _, check := range []struct{ name, before, after string }{
		{"missing-cpu", "cpu = 0", ""},
		{"zero-memory", "memory_mib = 256", "memory_mib = 0"},
		{"relative-hugepages", "/dev/hugepages", "hugepages"},
		{"empty-prefix", "mwan-native-test", ""},
		{"bad-prefix", "mwan-native-test", "mwan/native"},
		{"pair-zero", "", "pair_capacity = 0"},
		{"pair-overflow", "", "pair_capacity = 65537"},
		{"mbuf-zero", "", "mbuf_count = 0"},
		{"flow-zero", "", "flow_capacity = 0"},
		{"small-flow-capacity", "", "flow_capacity = 1"},
		{"event-zero", "", "event_capacity = 0"},
		{"observations-zero", "", "observation_capacity = 0"},
		{"socket-zero", "", "socket_buffer_bytes = 0"},
		{"socket-int-overflow", "", "socket_buffer_bytes = 2147483648"},
		{"cpu-uint-overflow", "cpu = 0", "cpu = 4294967296"},
		{"duration-zero", "", `request_timeout = "0s"`},
		{"observation-zero", "", `observation_max_age = "0s"`},
		{"counter-zero", "", `counter_interval = "0s"`},
		{"resync-timeout-zero", "", `resync_timeout = "0s"`},
		{"shutdown-zero", "", `shutdown_timeout = "0s"`},
		{"idle-timeout-zero", "", `idle_timeout = "0s"`},
		{"duration-negative", "", `resync_timeout = "-1s"`},
		{"duration-overflow", "", `shutdown_timeout = "999999999999h"`},
		{"duration-invalid", "", `counter_interval = "soon"`},
		{"resync-zero", "", `resync_interval = "0s"`},
		{"observation-negative", "", `observation_max_age = "-1s"`},
		{"idle-too-short", "", `idle_timeout = "1s"`},
		{"idle-wait-negative", "", `idle_wait = "-1us"`},
		{"idle-wait-invalid", "", `idle_wait = "soon"`},
	} {
		t.Run(check.name, func(t *testing.T) {
			t.Parallel()
			text := nativeProcessTOML + check.after
			if check.before != "" {
				text = strings.Replace(nativeProcessTOML, check.before, check.after, 1)
			}
			path := writeSharedFile(t, sharedFileText+text)
			if _, _, err := config.LoadArguments([]string{"mwan", "--config", path}); err == nil {
				t.Fatalf("loader accepted invalid native settings: %s", text)
			}
		})
	}
}

func TestLoadNativeOffloadIdleWait(t *testing.T) {
	t.Parallel()
	for _, check := range []struct {
		name  string
		input string
		want  time.Duration
	}{
		{name: "omitted", input: "", want: 10 * time.Microsecond},
		{name: "zero", input: `idle_wait = "0s"`, want: 0},
		{name: "positive", input: `idle_wait = "1us"`, want: time.Microsecond},
	} {
		t.Run(check.name, func(t *testing.T) {
			t.Parallel()
			path := writeSharedFile(t, sharedFileText+nativeProcessTOML+check.input)
			loaded, _, err := config.LoadArguments([]string{"mwan", "--config", path})
			if err != nil {
				t.Fatal(err)
			}
			settings, err := loaded.IfMgr.Modules.NativeOffload.Resolve()
			if err != nil || settings.IdleWait != check.want {
				t.Fatalf("resolved native idle wait = %v, want %v: %v", settings.IdleWait, check.want, err)
			}
		})
	}
}

func TestLoadNativeOffloadDisabled(t *testing.T) {
	t.Parallel()
	for _, text := range []string{"", "[ifmgr.modules.nativeoffload]\nenabled = false\n"} {
		path := writeSharedFile(t, sharedFileText+text)
		loaded, _, err := config.LoadArguments([]string{"mwan", "--config", path})
		if err != nil {
			t.Fatal(err)
		}
		settings, err := loaded.IfMgr.Modules.NativeOffload.Resolve()
		if err != nil || settings != (config.NativeOffloadProcessSettings{}) {
			t.Fatalf("disabled native settings = %+v, error %v", settings, err)
		}
	}
}

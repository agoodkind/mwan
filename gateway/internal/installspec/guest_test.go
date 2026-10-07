package installspec_test

import (
	"testing"

	"goodkind.io/mwan/internal/installspec"
)

func TestNamespacedSysctlSettingsKeepsEachCommentBesideItsSetting(t *testing.T) {
	t.Parallel()
	content := "# header\n\n" +
		"# about the first setting\nkernel.printk = 4\n" +
		"# about the second setting\nnet.core.somaxconn = 1\n" +
		"# about the third setting\nnet.core.netdev_max_backlog = 2\n" +
		"# trailing comment\n\n" +
		"# only a kernel block\nkernel.panic = 1\n"
	want := "# about the second setting\nnet.core.somaxconn = 1\n" +
		"# about the third setting\nnet.core.netdev_max_backlog = 2\n"

	got := installspec.NamespacedSysctlSettings([]byte(content))

	if string(got) != want {
		t.Fatalf("filtered content:\n%s\nwant:\n%s", got, want)
	}
}

func TestNamespacedSysctlSettingsReturnsNilWithoutANetSetting(t *testing.T) {
	t.Parallel()

	got := installspec.NamespacedSysctlSettings([]byte("# note\nkernel.panic = 1\n"))

	if got != nil {
		t.Fatalf("filtered content = %q, want nil", got)
	}
}

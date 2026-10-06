package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/installspec"
)

type nacmTree struct {
	NACM struct {
		ReadDefault  string `json:"read-default"`
		WriteDefault string `json:"write-default"`
		Groups       struct {
			Group []struct {
				Name      string   `json:"name"`
				UserNames []string `json:"user-name"`
			} `json:"group"`
		} `json:"groups"`
		RuleLists []struct {
			Name   string   `json:"name"`
			Groups []string `json:"group"`
		} `json:"rule-list"`
	} `json:"ietf-netconf-acm:nacm"`
}

func requireAnonymousPolicy(t *testing.T, datastore string, tree string) {
	t.Helper()
	if strings.TrimSpace(tree) == "" {
		t.Errorf("%s holds no NACM configuration", datastore)
		return
	}
	var policy nacmTree
	if err := json.Unmarshal([]byte(tree), &policy); err != nil {
		t.Errorf("decode the %s NACM configuration: %v\n%s", datastore, err, tree)
		return
	}
	nacm := policy.NACM
	if nacm.ReadDefault != "deny" || nacm.WriteDefault != "deny" {
		t.Errorf("%s read-default=%q write-default=%q, want deny and deny",
			datastore, nacm.ReadDefault, nacm.WriteDefault)
	}
	groupFound := false
	for _, group := range nacm.Groups.Group {
		if group.Name == "yangnobody" && slices.Contains(group.UserNames, "yangnobody") {
			groupFound = true
		}
	}
	if !groupFound {
		t.Errorf("%s has no yangnobody group holding the yangnobody user:\n%s", datastore, tree)
	}
	if len(nacm.RuleLists) == 0 || nacm.RuleLists[0].Name != "anonymous" ||
		!slices.Contains(nacm.RuleLists[0].Groups, "yangnobody") {
		t.Errorf("%s does not put the anonymous rule list for yangnobody first:\n%s", datastore, tree)
	}
}

func TestInstallApplyImportsTheNACMPolicy(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	output := runInstallChild(t, root)

	if !strings.Contains(output, "imported the ietf-netconf-acm policy into startup and running") {
		t.Errorf("run output:\n%s\nwant the import line", output)
	}
	for _, datastore := range []string{"running", "startup"} {
		tree := runChild(t, sysrepoChildEnv(t, root, "export", datastore),
			datastore, "/ietf-netconf-acm:nacm")
		requireAnonymousPolicy(t, datastore, tree)
	}
	embedded, err := installspec.NACMPolicy()
	if err != nil {
		t.Fatalf("read the embedded policy: %v", err)
	}
	onDisk, err := os.ReadFile(filepath.Join(root, "/etc/sysrepo-nacm-anonymous.xml"))
	if err != nil {
		t.Fatalf("read the installed policy: %v", err)
	}
	if !bytes.Equal(onDisk, embedded) {
		t.Fatal("the installed policy differs from the embedded copy")
	}

	second := runInstallChild(t, root)
	if strings.Contains(second, "imported") {
		t.Fatalf("second run output:\n%s\nwant no import", second)
	}
}

func TestInstallApplyImportsTheNACMPolicyIntoAFreshDatastore(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	runInstallChild(t, root)
	if err := os.RemoveAll(filepath.Join(root, "/etc/sysrepo")); err != nil {
		t.Fatalf("reset the repository: %v", err)
	}

	runInstallChild(t, root)

	for _, datastore := range []string{"running", "startup"} {
		tree := runChild(t, sysrepoChildEnv(t, root, "export", datastore),
			datastore, "/ietf-netconf-acm:nacm")
		requireAnonymousPolicy(t, datastore, tree)
	}
}

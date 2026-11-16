package schema_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"regexp"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/yangpub/schema"
)

func TestRevisionLock(t *testing.T) {
	t.Parallel()
	lock, err := os.ReadFile("revisions.lock")
	if err != nil {
		t.Fatal(err)
	}

	sums := map[string]string{}

	for line := range strings.Lines(string(lock)) {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			t.Fatalf("%q", line)
		}

		key := fields[0] + " " + fields[1]
		if _, found := sums[key]; found {
			t.Fatalf("%s", key)
		}

		sums[key] = fields[2]
	}

	statement := regexp.MustCompile(`(?m)^\s*revision\s+"?(\d{4}-\d{2}-\d{2})`)

	for _, module := range schema.Modules() {
		name, _, _ := strings.Cut(strings.TrimSuffix(module.File, ".yang"), "@")
		if !strings.HasPrefix(name, "goodkind-") {
			continue
		}

		text, err := schema.Read(module.File)
		if err != nil {
			t.Fatal(err)
		}

		date := ""

		for _, match := range statement.FindAllSubmatch(text, -1) {
			date = max(date, string(match[1]))
		}

		sum := sha256.Sum256(text)
		if got, want := hex.EncodeToString(sum[:]), sums[name+" "+date]; got != want {
			t.Errorf("%s %s: %s != %s", name, date, got, want)
		}
	}
}

package yangpub_test

import (
	"strings"
	"sync"
	"testing"

	"goodkind.io/mwan/internal/yangpub"
)

// rejectedDocument carries a leaf the steering model does not define, which
// strict parsing refuses with a message naming the leaf.
const rejectedDocument = `{
  "ietf-interfaces:interfaces": {
    "interface": [
      {
        "name": "enwebpass0",
        "type": "iana-if-type:other",
        "goodkind-mwan-steering:no-such-leaf": true
      }
    ]
  }
}`

// TestValidateConfigJSONAlwaysCarriesTheMessage pins that a rejection names
// its cause on every call, under the scheduling the daemon and the tests run
// with. libyang records an error per operating system thread, and the parse
// and the read of its record are two cgo calls, so a goroutine moved to
// another thread between them would read nothing and report only that no
// message was recorded. Many goroutines validating at once are what make that
// move likely, and each holds its own context because a context is not shared
// across threads.
func TestValidateConfigJSONAlwaysCarriesTheMessage(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := yangpub.WriteSchema(dir); err != nil {
		t.Fatalf("write the embedded schema: %v", err)
	}

	const (
		validators = 32
		rounds     = 200
	)
	var wait sync.WaitGroup
	failures := make(chan string, validators)
	for i := 0; i < validators; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			schema, err := yangpub.LoadSchema(dir)
			if err != nil {
				failures <- "load schema: " + err.Error()
				return
			}
			defer schema.Close()
			for round := 0; round < rounds; round++ {
				err := schema.ValidateConfigJSON([]byte(rejectedDocument))
				if err == nil {
					failures <- "a document with an undefined leaf was accepted"
					return
				}
				if !strings.Contains(err.Error(), "no-such-leaf") {
					failures <- "rejection lost its message: " + err.Error()
					return
				}
			}
		}()
	}
	wait.Wait()
	close(failures)
	for failure := range failures {
		t.Error(failure)
	}
}

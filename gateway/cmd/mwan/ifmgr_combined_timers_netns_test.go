//go:build linux && firewallnetns

package main

import (
	"encoding/json"
	"testing"
	"time"
)

const combinedGoBGPIdleHold = 5 * time.Second

type combinedTimers struct {
	check, timeout, retry, hold time.Duration
	failure, recovery           int
}

func (c *combinedRun) timers(t *testing.T, device string) combinedTimers {
	t.Helper()
	var document struct {
		Interfaces struct {
			Entries []struct {
				Name string `json:"name"`
				WAN  struct {
					Health struct {
						Check    int `json:"check-interval"`
						Failure  int `json:"failure-threshold"`
						Recovery int `json:"recovery-threshold"`
					} `json:"health"`
					Sessions []struct {
						Retry int `json:"connect-retry"`
						Hold  int `json:"hold"`
					} `json:"bgp-session"`
				} `json:"goodkind-mwan-steering:wan"`
			} `json:"interface"`
			Group struct {
				Health struct {
					Timeout int `json:"probe-timeout"`
				} `json:"health"`
			} `json:"goodkind-mwan-steering:steering-group"`
		} `json:"ietf-interfaces:interfaces"`
	}
	if err := json.Unmarshal([]byte(c.network), &document); err != nil {
		t.Fatal(err)
	}
	for _, entry := range document.Interfaces.Entries {
		if entry.Name != device {
			continue
		}
		timers := combinedTimers{
			check: time.Duration(entry.WAN.Health.Check) * time.Second, failure: entry.WAN.Health.Failure,
			recovery: entry.WAN.Health.Recovery, timeout: time.Duration(document.Interfaces.Group.Health.Timeout) * time.Millisecond,
		}
		if timers.check == 0 || timers.failure == 0 || timers.recovery == 0 || timers.timeout == 0 {
			t.Fatalf("%s has no complete probe timers: %+v", device, timers)
		}
		for _, session := range entry.WAN.Sessions {
			timers.retry, timers.hold = time.Duration(session.Retry)*time.Second, time.Duration(session.Hold)*time.Second
		}
		return timers
	}
	t.Fatalf("network document has no interface %s", device)
	return combinedTimers{}
}

func (c *combinedRun) connectBound(t *testing.T, device string) time.Duration {
	t.Helper()
	timers := c.timers(t, device)
	if timers.retry == 0 {
		t.Fatalf("%s has no configured connect retry", device)
	}
	return 3*timers.retry + 3*time.Duration(timers.recovery)*timers.check
}

func (c *combinedRun) faultBound(t *testing.T, device string) time.Duration {
	t.Helper()
	timers := c.timers(t, device)
	return 2 * (timers.hold + time.Duration(timers.failure)*(timers.check+timers.timeout))
}

func (c *combinedRun) recoverBound(t *testing.T, device string) time.Duration {
	t.Helper()
	timers := c.timers(t, device)
	if timers.retry == 0 {
		return c.faultBound(t, device)
	}
	return combinedGoBGPIdleHold + c.connectBound(t, device)
}

func (r *bgpRuntimeRouter) firstReceipt(since time.Time) (time.Time, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, receipt := range r.receipts {
		if !receipt.Before(since) {
			return receipt, true
		}
	}
	return time.Time{}, false
}

func requireCombinedSince(t *testing.T, read func() string, device, family string) {
	t.Helper()
	path := readCombinedPath(read, device, family)
	start := time.Time{}
	for index := len(path.Transitions) - 1; index >= 0 && path.Transitions[index].Current == path.State; index-- {
		start = path.Transitions[index].At
	}
	if start.IsZero() || !path.Since.Equal(start) {
		t.Fatalf("%s %s path since = %s, want the start %s of the %s state; transitions=%+v",
			device, family, path.Since, start, path.State, path.Transitions)
	}
}

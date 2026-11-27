//go:build linux && (firewall || protocol || systemd)

package testenv

import (
	"encoding/json"
	"testing"
	"time"
)

const CombinedGoBGPIdleHold = 5 * time.Second

type CombinedTimers struct {
	Check, Timeout, Retry, Hold time.Duration
	Failure, Recovery           int
}

func (c *CombinedRun) Timers(t *testing.T, device string) CombinedTimers {
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
	if err := json.Unmarshal([]byte(c.Network), &document); err != nil {
		t.Fatal(err)
	}
	for _, entry := range document.Interfaces.Entries {
		if entry.Name != device {
			continue
		}
		timers := CombinedTimers{
			Check:    time.Duration(entry.WAN.Health.Check) * time.Second,
			Failure:  entry.WAN.Health.Failure,
			Recovery: entry.WAN.Health.Recovery,
			Timeout:  time.Duration(document.Interfaces.Group.Health.Timeout) * time.Millisecond,
		}
		if timers.Check == 0 || timers.Failure == 0 || timers.Recovery == 0 || timers.Timeout == 0 {
			t.Fatalf("%s has no complete probe timers: %+v", device, timers)
		}
		for _, session := range entry.WAN.Sessions {
			timers.Retry, timers.Hold = time.Duration(session.Retry)*time.Second, time.Duration(
				session.Hold,
			)*time.Second
		}
		return timers
	}
	t.Fatalf("network document has no interface %s", device)
	return CombinedTimers{}
}

func (c *CombinedRun) ConnectBound(t *testing.T, device string) time.Duration {
	t.Helper()
	timers := c.Timers(t, device)
	if timers.Retry == 0 {
		t.Fatalf("%s has no configured connect retry", device)
	}
	return 3*timers.Retry + 3*time.Duration(timers.Recovery)*timers.Check
}

func (c *CombinedRun) FaultBound(t *testing.T, device string) time.Duration {
	t.Helper()
	timers := c.Timers(t, device)
	return 2 * (timers.Hold + time.Duration(timers.Failure)*(timers.Check+timers.Timeout))
}

func (c *CombinedRun) RecoverBound(t *testing.T, device string) time.Duration {
	t.Helper()
	timers := c.Timers(t, device)
	if timers.Retry == 0 {
		return c.FaultBound(t, device)
	}
	return CombinedGoBGPIdleHold + c.ConnectBound(t, device)
}

func (r *BgpRuntimeRouter) FirstReceipt(since time.Time) (time.Time, bool) {
	r.Mu.Lock()
	defer r.Mu.Unlock()
	for _, receipt := range r.Receipts {
		if !receipt.Before(since) {
			return receipt, true
		}
	}
	return time.Time{}, false
}

func RequireCombinedSince(t *testing.T, read func() string, device, family string) {
	t.Helper()
	path := ReadCombinedPath(read, device, family)
	start := time.Time{}
	for index := len(
		path.Transitions,
	) - 1; index >= 0 && path.Transitions[index].Current == path.State; index-- {
		start = path.Transitions[index].At
	}
	if start.IsZero() || !path.Since.Equal(start) {
		t.Fatalf(
			"%s %s path since = %s, want the start %s of the %s state; transitions=%+v",
			device,
			family,
			path.Since,
			start,
			path.State,
			path.Transitions,
		)
	}
}

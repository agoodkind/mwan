package netif

import (
	"testing"
	"time"
)

func TestNextBackoff(t *testing.T) {
	cases := []struct {
		cur  time.Duration
		max  time.Duration
		want time.Duration
	}{
		{5 * time.Second, time.Minute, 10 * time.Second},
		{30 * time.Second, time.Minute, time.Minute},
		{time.Minute, time.Minute, time.Minute},
		{2 * time.Minute, time.Minute, time.Minute},
	}
	for _, tc := range cases {
		got := nextBackoff(tc.cur, tc.max)
		if got != tc.want {
			t.Errorf("nextBackoff(%v, %v) = %v, want %v",
				tc.cur, tc.max, got, tc.want)
		}
	}
}

func TestLeaseToInfoNilLease(t *testing.T) {
	now := time.Now()
	info := leaseToInfo(LeaseExpired, nil, now, nil)
	if info.State != LeaseExpired {
		t.Fatalf("State got %v want %v", info.State, LeaseExpired)
	}
	if info.IP != nil {
		t.Errorf("IP should be nil, got %v", info.IP)
	}
	if !info.AcquiredAt.Equal(now) {
		t.Errorf("AcquiredAt got %v want %v", info.AcquiredAt, now)
	}
}

func TestLeaseStateString(t *testing.T) {
	pairs := map[LeaseState]string{
		LeaseInit:       "INIT",
		LeaseSelecting:  "SELECTING",
		LeaseRequesting: "REQUESTING",
		LeaseBound:      "BOUND",
		LeaseRenewing:   "RENEWING",
		LeaseExpired:    "EXPIRED",
	}
	for s, want := range pairs {
		if got := s.String(); got != want {
			t.Errorf("State(%d).String()=%q want %q", s, got, want)
		}
	}
}

func TestStripPrefix(t *testing.T) {
	cases := map[string]string{
		"3d06:bad:b01:ff::1/128": "3d06:bad:b01:ff::1",
		"10.0.0.1/24":            "10.0.0.1",
		"no-slash":               "no-slash",
		"":                       "",
	}
	for in, want := range cases {
		if got := StripPrefix(in); got != want {
			t.Errorf("StripPrefix(%q)=%q want %q", in, got, want)
		}
	}
}

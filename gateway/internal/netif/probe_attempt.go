package netif

import "time"

// PingAttempt shares the raw request and result between probe consumers.
type PingAttempt struct {
	Interface string        `json:"interface"`
	Family    string        `json:"family"`
	Target    string        `json:"target"`
	Attempt   int           `json:"attempt"`
	Timeout   time.Duration `json:"timeout_ns"`
	RTT       time.Duration `json:"rtt_ns"`
	Error     string        `json:"error"`
}

// HTTPAttempt shares the raw request and result between probe consumers.
type HTTPAttempt struct {
	Interface string        `json:"interface"`
	Family    string        `json:"family"`
	Target    string        `json:"target"`
	Iteration int           `json:"iteration"`
	Timeout   time.Duration `json:"timeout_ns"`
	HTTPResult
	Error string `json:"error"`
}

package gateway

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"testing"
)

func TestClassifyAttemptError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"attempt timeout sentinel", errAttemptTimeout, "attempt_timeout"},
		{"attempt timeout wrapped", fmt.Errorf("upstream: %w", errAttemptTimeout), "attempt_timeout"},
		{"first event timeout", errFirstEventTimeout, "first_event"},
		{"generic deadline", context.DeadlineExceeded, "timeout"},
		{"phased dial error", &phasedError{err: errors.New("dial tcp: i/o timeout"), phase: "dial"}, "transport_dial"},
		{"phased write error", &phasedError{err: errAttemptTimeout, phase: "write"}, "attempt_timeout_write"},
		{"plain transport error", errors.New("connection reset by peer"), "transport"},
		{"socket i/o timeout (25s legs)", &net.OpError{Op: "read", Err: os.ErrDeadlineExceeded}, "transport"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyAttemptError(tc.err); got != tc.want {
				t.Fatalf("classifyAttemptError() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWarpFailReason(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{errFirstEventTimeout, "first event timeout"},
		{errAttemptTimeout, "attempt timeout"},
		{errors.New("connection reset"), "transport error"},
		{&net.OpError{Op: "read", Err: os.ErrDeadlineExceeded}, "transport error"},
	}
	for _, tc := range cases {
		if got := warpFailReason(tc.err); got != tc.want {
			t.Fatalf("warpFailReason(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

func TestIsProxyFailure(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"first event timeout is not a proxy failure", errFirstEventTimeout, false},
		{"deadline exceeded evicts proxy", context.DeadlineExceeded, true},
		{"connection refused evicts proxy", &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, true},
		{"plain transport error does not evict", errors.New("tls: bad certificate"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isProxyFailure(tc.err); got != tc.want {
				t.Fatalf("isProxyFailure() = %v, want %v", got, tc.want)
			}
		})
	}
}

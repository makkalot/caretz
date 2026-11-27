package llm

import (
	"context"
	"errors"
	"net"
	"syscall"
)

// IsUnreachable reports whether err means the model's endpoint could not be
// reached at all: the host did not resolve, or the connection was never
// made. A reply of any kind, including an HTTP error status, means the model
// is reachable, and a cancelled or expired context is the caller's own limit,
// so neither counts.
func IsUnreachable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if _, ok := APIErrorStatus(err); ok {
		return false
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return true
	}
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH)
}

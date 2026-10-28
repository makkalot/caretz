package agent

import "context"

// IsCleanStop distinguishes a completed user stop from cancellation joined
// with a cleanup or persistence failure. Adapters must display the latter.
func IsCleanStop(err error) bool {
	if err == nil {
		return false
	}
	if err == context.Canceled || err == ErrGracefulStop {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !IsCleanStop(child) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return IsCleanStop(wrapped.Unwrap())
	}
	return false
}

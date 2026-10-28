//go:build !darwin && !linux

package ansi

import "time"

func newInputWaiter(int) func(time.Duration) (bool, error) { return nil }

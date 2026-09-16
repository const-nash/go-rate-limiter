// Package validate provides shared argument validation for concrete
// Algorithm implementations, so each constructor doesn't duplicate
// the same checks.
package validate

import (
	"errors"
	"time"
)

func Rate(rate float64) error {
	if rate <= 0 {
		return errors.New("ratelimit: rate must be positive")
	}
	return nil
}

func Window(window time.Duration) error {
	if window <= 0 {
		return errors.New("ratelimit: window must be positive")
	}
	return nil
}

func CleanupInterval(interval time.Duration) error {
	if interval <= 0 {
		return errors.New("ratelimit: cleanup interval must be positive")
	}
	return nil
}

func Limit(limit int) error {
	if limit <= 0 {
		return errors.New("ratelimit: limit must be positive")
	}
	return nil
}

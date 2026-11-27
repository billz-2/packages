package inbox

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net"
	"time"
)

// RetryPolicy is how often a failing handler runs before its message becomes a dead letter. The zero policy
// runs it once.
type RetryPolicy struct {
	Attempts int             // in all, the first one included
	Pauses   []time.Duration // before the 2nd, 3rd, … attempt; the last one repeats
}

// DefaultRetry holds a partition for at most 21 s of pauses and 4 runs, well inside sarama's 60 s
// rebalance timeout.
var DefaultRetry = RetryPolicy{
	Attempts: 4, //nolint:mnd // with the pauses below
	Pauses:   []time.Duration{time.Second, 5 * time.Second, 15 * time.Second},
}

func (p RetryPolicy) attempts() int {
	return max(p.Attempts, 1)
}

// pause is the wait after the failed attempt n (1-based).
func (p RetryPolicy) pause(n int) time.Duration {
	if len(p.Pauses) == 0 {
		return 0
	}

	return p.Pauses[min(n, len(p.Pauses))-1]
}

type permanentError struct{ err error }

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

// Permanent marks an error that would fail again: its message becomes a dead letter without more attempts.
func Permanent(err error) error {
	if err == nil {
		return nil
	}

	return permanentError{err: err}
}

func isPermanent(err error) bool {
	var p permanentError

	return errors.As(err, &p)
}

// Transient reports a database or network failure that a later attempt can get past: a lost connection, a
// timeout, a serialization failure or deadlock, too many connections, a server shutting down.
func Transient(err error) bool {
	if err == nil || isPermanent(err) {
		return false
	}
	if errors.Is(err, driver.ErrBadConn) || errors.Is(err, sql.ErrConnDone) ||
		errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	var coded interface{ SQLState() string }
	if errors.As(err, &coded) {
		state := coded.SQLState()
		switch {
		case len(state) == 5 && state[:2] == "08": // connection exception
			return true
		case state == "40001", state == "40P01", state == "53300", state == "57P01":
			return true
		}

		return false
	}

	var netErr net.Error

	return errors.As(err, &netErr)
}

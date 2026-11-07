package platform

import (
	"context"
	"errors"
	"io"
	"net"
	"syscall"
	"time"
)

// defaultRetryDelays allow four attempts, like the curl --retry 3 brew uses.
var defaultRetryDelays = []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second}

// serverError is a 5xx reply: the request was fine, so it may be sent again.
type serverError struct{ message string }

func (e serverError) Error() string { return e.message }

// StatusError reports an unexpected HTTP status, marking 5xx replies retryable.
func StatusError(status int, message string) error {
	if status >= 500 {
		return serverError{message}
	}
	return errors.New(message)
}

// Retry repeats one idempotent fetch after a transport failure, waiting delays
// between attempts (nil means the default). Callers keep verification of what
// they fetched outside this or in fetch itself, so an integrity failure is never
// retried, and hold no prefix lock while this waits.
func Retry(ctx context.Context, delays []time.Duration, fetch func() error) error {
	if delays == nil {
		delays = defaultRetryDelays
	}
	err := fetch()
	for _, delay := range delays {
		if err == nil || ctx.Err() != nil || !transient(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		err = fetch()
	}
	return err
}

func transient(err error) bool {
	var server serverError
	var network net.Error
	switch {
	case errors.As(err, &server):
		return true
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return true
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.ECONNABORTED), errors.Is(err, syscall.EPIPE):
		return true
	}
	return errors.As(err, &network) && network.Timeout()
}

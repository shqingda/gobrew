package platform

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
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
	return RetryWith(ctx, delays, nil, fetch)
}

// RetryWith is Retry that calls retrying, if not nil, before each wait with
// the wait, the attempts left after it, and the failure it follows. Errors
// name only the host they failed on.
func RetryWith(ctx context.Context, delays []time.Duration, retrying func(wait time.Duration, left int, err error), fetch func() error) error {
	if delays == nil {
		delays = defaultRetryDelays
	}
	err := fetch()
	for i, delay := range delays {
		if err == nil || ctx.Err() != nil || !transient(err) {
			return HostOnly(err)
		}
		if retrying != nil {
			retrying(delay, len(delays)-i, HostOnly(err))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		err = fetch()
	}
	return HostOnly(err)
}

// RetryMessage is brew's "Retrying download in 1s... (2 tries left)" for what.
func RetryMessage(what string, wait time.Duration, left int) string {
	tries := "tries"
	if left == 1 {
		tries = "try"
	}
	return fmt.Sprintf("Retrying download of %s in %s... (%d %s left)", what, wait, left, tries)
}

// HostOnly shortens a request error to the host it failed on. A *url.Error
// carries the whole URL, which for a bottle is a long signed redirect that
// says nothing to a person. errors.Is and errors.As still see the original.
func HostOnly(err error) error {
	var request *url.Error
	if !errors.As(err, &request) {
		return err
	}
	host := request.URL
	if u, parseErr := url.Parse(request.URL); parseErr == nil && u.Host != "" {
		host = u.Host
	}
	text := strings.Replace(err.Error(), request.Error(), host+": "+request.Err.Error(), 1)
	return hostError{err, text}
}

type hostError struct {
	err  error
	text string
}

func (e hostError) Error() string { return e.text }
func (e hostError) Unwrap() error { return e.err }

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

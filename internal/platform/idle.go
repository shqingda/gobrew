package platform

import (
	"context"
	"fmt"
	"io"
	"sync/atomic"
	"time"
)

// DefaultIdleTimeout ends a download attempt that has received nothing for
// this long. It replaces a whole-request deadline, which failed large bottles
// on slow but steady connections and then restarted them from zero.
const DefaultIdleTimeout = time.Minute

// Idle cancels one request when it makes no progress: from sending the request
// until the response headers, and between body reads. Callers pass Context to
// the request, read the body through Reader and map errors through Err.
type Idle struct {
	ctx    context.Context
	cancel context.CancelFunc
	timer  *time.Timer
	after  time.Duration
	fired  atomic.Bool
}

func NewIdle(ctx context.Context, after time.Duration) *Idle {
	ctx, cancel := context.WithCancel(ctx)
	i := &Idle{ctx: ctx, cancel: cancel, after: after}
	i.timer = time.AfterFunc(after, func() {
		i.fired.Store(true)
		cancel()
	})
	return i
}

func (i *Idle) Context() context.Context { return i.ctx }

// Stop releases the timer and the attempt's context.
func (i *Idle) Stop() {
	i.timer.Stop()
	i.cancel()
}

func (i *Idle) Reader(r io.Reader) io.Reader { return idleReader{r, i} }

// Err reports a cancellation caused by the watchdog as a timeout, which Retry
// treats as transient; any other error is returned unchanged.
func (i *Idle) Err(err error) error {
	if err != nil && i.fired.Load() {
		return idleTimeout{i.after}
	}
	return err
}

type idleReader struct {
	r io.Reader
	i *Idle
}

func (r idleReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 {
		r.i.timer.Reset(r.i.after)
	}
	return n, r.i.Err(err)
}

type idleTimeout struct{ after time.Duration }

func (e idleTimeout) Error() string {
	return fmt.Sprintf("no data received for %s", e.after)
}
func (idleTimeout) Timeout() bool   { return true }
func (idleTimeout) Temporary() bool { return true }

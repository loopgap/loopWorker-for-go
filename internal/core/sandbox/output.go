package sandbox

import (
	"bytes"
	"sync"

	lwerrors "loopworker/pkg/errors"
)

// cappedWriter accumulates output but fails at the limit instead of after the
// whole payload has been buffered. Crossing the limit cancels the execution
// context, which terminates the wasm module (or a cooperative native plugin)
// immediately, so a hostile plugin cannot make the host allocate unbounded
// memory before being rejected.
type cappedWriter struct {
	mu         sync.Mutex
	buf        bytes.Buffer
	max        int64
	written    int64
	overflow   bool
	onOverflow func()
}

// newCappedWriter returns a writer capped at max bytes (0 means uncapped).
// onOverflow may be nil.
func newCappedWriter(max int64, onOverflow func()) *cappedWriter {
	return &cappedWriter{max: max, onOverflow: onOverflow}
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.written += int64(len(p))
	overflow := w.max > 0 && w.written > w.max
	if !overflow {
		w.buf.Write(p)
	}
	if overflow && !w.overflow {
		w.overflow = true
		notify := w.onOverflow
		w.mu.Unlock()
		if notify != nil {
			notify()
		}
		return 0, lwerrors.ErrSandboxOversized
	}
	w.mu.Unlock()
	if overflow {
		return 0, lwerrors.ErrSandboxOversized
	}
	return len(p), nil
}

// Bytes returns the accumulated output; it is never larger than the cap.
func (w *cappedWriter) Bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]byte, w.buf.Len())
	copy(out, w.buf.Bytes())
	return out
}

func (w *cappedWriter) didOverflow() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.overflow
}

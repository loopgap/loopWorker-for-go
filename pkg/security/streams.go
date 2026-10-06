package security

import "sync"

// LimitKind explains which ceiling rejected a slot request.
type LimitKind string

const (
	LimitNone    LimitKind = ""
	LimitPerKey  LimitKind = "per_caller"
	LimitGlobal  LimitKind = "global"
	LimitUnknown LimitKind = "unknown_caller"
)

// ConcurrencyLimiter bounds simultaneously held slots (SSE streams, long polls)
// per caller and in total. Slots must always be released.
type ConcurrencyLimiter struct {
	mu        sync.Mutex
	perKey    map[string]int
	maxPerKey int
	maxTotal  int
	total     int
}

// NewConcurrencyLimiter creates a limiter; values below 1 are clamped to 1.
func NewConcurrencyLimiter(maxPerKey, maxTotal int) *ConcurrencyLimiter {
	if maxPerKey < 1 {
		maxPerKey = 1
	}
	if maxTotal < 1 {
		maxTotal = 1
	}
	return &ConcurrencyLimiter{
		perKey:    make(map[string]int),
		maxPerKey: maxPerKey,
		maxTotal:  maxTotal,
	}
}

// Acquire takes a slot and returns its release function. When denied, the
// returned release is a no-op and kind reports which ceiling was hit.
func (l *ConcurrencyLimiter) Acquire(key string) (release func(), granted bool, kind LimitKind) {
	noop := func() {}
	if key == "" {
		return noop, false, LimitUnknown
	}

	l.mu.Lock()
	if l.total >= l.maxTotal {
		l.mu.Unlock()
		return noop, false, LimitGlobal
	}
	if l.perKey[key] >= l.maxPerKey {
		l.mu.Unlock()
		return noop, false, LimitPerKey
	}
	l.total++
	l.perKey[key]++
	l.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			l.total--
			l.perKey[key]--
			if l.perKey[key] <= 0 {
				delete(l.perKey, key)
			}
			l.mu.Unlock()
		})
	}, true, LimitNone
}

// Active reports slots currently held by a caller.
func (l *ConcurrencyLimiter) Active(key string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.perKey[key]
}

// Total reports slots currently held overall.
func (l *ConcurrencyLimiter) Total() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.total
}

// MaxPerKey reports the configured per-caller ceiling.
func (l *ConcurrencyLimiter) MaxPerKey() int { return l.maxPerKey }

// MaxTotal reports the configured global ceiling.
func (l *ConcurrencyLimiter) MaxTotal() int { return l.maxTotal }

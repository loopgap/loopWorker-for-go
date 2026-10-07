package security

import (
	"sync"
	"testing"
)

// The limiter bounds held slots (SSE streams, long polls). Two failure modes
// matter to a customer: a slot that is never released eventually refuses every
// caller, and a slot released twice lets one caller hold two streams against a
// per-caller ceiling of one.

func TestConcurrencyLimiterGrantsAndReports(t *testing.T) {
	l := NewConcurrencyLimiter(2, 5)

	release, granted, kind := l.Acquire("alice")
	if !granted || kind != LimitNone {
		t.Fatalf("first acquire: granted=%v kind=%q", granted, kind)
	}
	if got := l.Active("alice"); got != 1 {
		t.Errorf("Active(alice) = %d, want 1", got)
	}
	if got := l.Total(); got != 1 {
		t.Errorf("Total = %d, want 1", got)
	}
	if l.MaxPerKey() != 2 || l.MaxTotal() != 5 {
		t.Errorf("ceilings not reported: perKey=%d total=%d", l.MaxPerKey(), l.MaxTotal())
	}

	release()
	if got := l.Active("alice"); got != 0 {
		t.Errorf("Active(alice) after release = %d, want 0", got)
	}
	if got := l.Total(); got != 0 {
		t.Errorf("Total after release = %d, want 0", got)
	}
}

// A caller with no identity cannot be counted, so it is refused with a kind that
// says why rather than being lumped in with the global ceiling.
func TestConcurrencyLimiterRefusesUnknownCaller(t *testing.T) {
	l := NewConcurrencyLimiter(1, 4)

	release, granted, kind := l.Acquire("")
	if granted {
		t.Fatal("an empty key must not be granted a slot")
	}
	if kind != LimitUnknown {
		t.Errorf("kind = %q, want %q", kind, LimitUnknown)
	}
	release() // the no-op release must be safe to call
	if got := l.Total(); got != 0 {
		t.Errorf("a refused acquire must not change the counters, Total = %d", got)
	}
}

func TestConcurrencyLimiterPerCallerCeiling(t *testing.T) {
	l := NewConcurrencyLimiter(1, 10)

	release, granted, _ := l.Acquire("alice")
	if !granted {
		t.Fatal("the first slot must be granted")
	}

	_, granted, kind := l.Acquire("alice")
	if granted {
		t.Fatal("the per-caller ceiling of 1 must be enforced")
	}
	if kind != LimitPerKey {
		t.Errorf("kind = %q, want %q", kind, LimitPerKey)
	}

	// A different caller is unaffected by alice's slot.
	otherRelease, granted, _ := l.Acquire("bob")
	if !granted {
		t.Error("bob must be granted a slot while alice holds hers")
	}

	otherRelease()
	release()
	if got := l.Total(); got != 0 {
		t.Errorf("Total after both releases = %d, want 0", got)
	}
}

func TestConcurrencyLimiterGlobalCeiling(t *testing.T) {
	l := NewConcurrencyLimiter(5, 2)

	r1, granted, _ := l.Acquire("alice")
	if !granted {
		t.Fatal("first acquire must be granted")
	}
	r2, granted, _ := l.Acquire("bob")
	if !granted {
		t.Fatal("second acquire must be granted")
	}

	_, granted, kind := l.Acquire("carol")
	if granted {
		t.Fatal("the global ceiling of 2 must be enforced across callers")
	}
	if kind != LimitGlobal {
		t.Errorf("kind = %q, want %q", kind, LimitGlobal)
	}

	r1()
	r2()
	// With room again, the previously refused caller must get in.
	release, granted, _ := l.Acquire("carol")
	if !granted {
		t.Fatal("carol must be granted once the global ceiling has room again")
	}
	release()
}

// A double release must not create a slot that nobody holds. Without the guard,
// perKey[alice] would go negative and the caller could exceed its ceiling.
func TestConcurrencyLimiterReleaseIsIdempotent(t *testing.T) {
	l := NewConcurrencyLimiter(1, 2)

	release, granted, _ := l.Acquire("alice")
	if !granted {
		t.Fatal("acquire must be granted")
	}

	release()
	release()
	release()

	if got := l.Active("alice"); got != 0 {
		t.Errorf("Active(alice) after three releases = %d, want 0", got)
	}
	if got := l.Total(); got != 0 {
		t.Errorf("Total after three releases = %d, want 0", got)
	}

	// The ceiling must still hold after the surplus releases.
	r1, granted, _ := l.Acquire("alice")
	if !granted {
		t.Fatal("alice must be able to take her slot back")
	}
	if _, granted, kind := l.Acquire("alice"); granted {
		t.Fatalf("the per-caller ceiling of 1 no longer holds (kind=%q)", kind)
	}
	r1()
}

// A released key must be forgotten, not left behind at zero, so a long-lived
// process does not accumulate one map entry per caller ever seen.
func TestConcurrencyLimiterForgetsFullyReleasedKeys(t *testing.T) {
	l := NewConcurrencyLimiter(1, 4)

	release, _, _ := l.Acquire("alice")
	release()

	l.mu.Lock()
	remaining := len(l.perKey)
	l.mu.Unlock()

	if remaining != 0 {
		t.Errorf("the per-caller map still holds %d entries after release, want 0", remaining)
	}
}

func TestConcurrencyLimiterClampsCeilings(t *testing.T) {
	for _, tc := range []struct{ perKey, total int }{{0, 0}, {-1, -1}, {0, 5}, {5, 0}} {
		l := NewConcurrencyLimiter(tc.perKey, tc.total)
		if l.MaxPerKey() < 1 || l.MaxTotal() < 1 {
			t.Errorf("NewConcurrencyLimiter(%d, %d) kept a ceiling below 1: %d, %d",
				tc.perKey, tc.total, l.MaxPerKey(), l.MaxTotal())
		}
		// A ceiling clamped to 1 must still admit exactly one caller.
		release, granted, _ := l.Acquire("alice")
		if !granted {
			t.Errorf("NewConcurrencyLimiter(%d, %d) granted nothing", tc.perKey, tc.total)
			continue
		}
		if _, granted, _ := l.Acquire("alice"); granted {
			t.Errorf("NewConcurrencyLimiter(%d, %d) allowed two slots for one caller", tc.perKey, tc.total)
		}
		release()
	}
}

func TestConcurrencyLimiterNeverExceedsGlobalCeiling(t *testing.T) {
	const maxTotal = 4
	l := NewConcurrencyLimiter(3, maxTotal)

	var (
		mu      sync.Mutex
		held    int
		peak    int
		wg      sync.WaitGroup
		release []func()
	)

	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := string(rune('a' + i%8))
			r, granted, _ := l.Acquire(key)
			if !granted {
				return
			}
			mu.Lock()
			held++
			if held > peak {
				peak = held
			}
			mu.Unlock()
			mu.Lock()
			release = append(release, r)
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if peak > maxTotal {
		t.Errorf("peak concurrent slots = %d, above the ceiling of %d", peak, maxTotal)
	}
	if got := l.Total(); got != len(release) {
		t.Errorf("Total = %d but %d slots were granted", got, len(release))
	}
	for _, r := range release {
		r()
	}
	if got := l.Total(); got != 0 {
		t.Errorf("Total after releasing everything = %d, want 0", got)
	}
}

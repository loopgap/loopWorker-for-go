package debugger

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

func TestLogLevelString(t *testing.T) {
	tests := []struct {
		level LogLevel
		want  string
	}{
		{LevelDebug, "DEBUG"}, {LevelInfo, "INFO"}, {LevelWarn, "WARN"}, {LevelError, "ERROR"}, {LevelFatal, "FATAL"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.level.String(); got != tt.want {
				t.Errorf("LogLevel.String() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestNewDebugger(t *testing.T) {
	d := NewDebugger()
	if d == nil {
		t.Fatal("NewDebugger returned nil")
	}
	if d.minLevel != LevelDebug {
		t.Errorf("expected default minLevel, got %s", d.minLevel)
	}
}

func TestLogBasic(t *testing.T) {
	d := NewDebugger()
	d.Log(LevelInfo, "core", "hello", map[string]interface{}{"k": "v"})
	logs := d.GetLogs(LevelDebug)
	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}
	e := logs[0]
	if e.Level != LevelInfo {
		t.Errorf("expected INFO, got %s", e.Level)
	}
	if e.Component != "core" {
		t.Errorf("expected core, got %s", e.Component)
	}
	if e.Message != "hello" {
		t.Errorf("expected hello, got %s", e.Message)
	}
	if e.Fields["k"] != "v" {
		t.Errorf("expected k=v, got %v", e.Fields["k"])
	}
	if e.Timestamp.IsZero() {
		t.Error("timestamp should not be zero")
	}
}

func TestLogConvenienceMethods(t *testing.T) {
	d := NewDebugger()
	d.Debug("comp", "d", nil)
	d.Info("comp", "i", nil)
	d.Warn("comp", "w", nil)
	d.Error("comp", "e", nil)
	logs := d.GetLogs(LevelDebug)
	if len(logs) != 4 {
		t.Fatalf("expected 4 logs, got %d", len(logs))
	}
}

func TestLogFilterByLevel(t *testing.T) {
	d := NewDebugger()
	d.Debug("c", "d", nil)
	d.Info("c", "i", nil)
	d.Warn("c", "w", nil)
	d.Error("c", "e", nil)
	tests := []struct {
		filter LogLevel
		want   int
	}{
		{LevelDebug, 4}, {LevelInfo, 3}, {LevelWarn, 2}, {LevelError, 1}, {LevelFatal, 0},
	}
	for _, tt := range tests {
		t.Run(tt.filter.String(), func(t *testing.T) {
			if got := d.GetLogs(tt.filter); len(got) != tt.want {
				t.Errorf("GetLogs(%s) = %d, want %d", tt.filter, len(got), tt.want)
			}
		})
	}
}

func TestSetMinLevel(t *testing.T) {
	d := NewDebugger()
	d.SetMinLevel(LevelWarn)
	d.Debug("c", "x", nil)
	d.Info("c", "x", nil)
	d.Warn("c", "x", nil)
	d.Error("c", "x", nil)
	if len(d.GetLogs(LevelDebug)) != 2 {
		t.Errorf("expected 2 logs, got %d", len(d.GetLogs(LevelDebug)))
	}
}

func TestGetLogsByComponent(t *testing.T) {
	d := NewDebugger()
	d.Info("auth", "login", nil)
	d.Info("db", "query", nil)
	d.Info("auth", "logout", nil)
	if len(d.GetLogsByComponent("auth")) != 2 {
		t.Errorf("expected 2 auth logs")
	}
}

func TestClear(t *testing.T) {
	d := NewDebugger()
	d.Info("c", "msg", nil)
	d.TakeSnapshot()
	d.Clear()
	if len(d.GetLogs(LevelDebug)) != 0 {
		t.Error("expected 0 logs after clear")
	}
	if len(d.GetSnapshots()) != 0 {
		t.Error("expected 0 snapshots after clear")
	}
}

func TestConcurrentLogging(t *testing.T) {
	d := NewDebugger()
	var wg sync.WaitGroup
	n := 100
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(id int) { defer wg.Done(); d.Info("c", fmt.Sprintf("msg-%d", id), nil) }(i)
	}
	wg.Wait()
	if len(d.GetLogs(LevelDebug)) != n {
		t.Errorf("expected %d logs", n)
	}
}

func TestAddBreakpoint(t *testing.T) {
	d := NewDebugger()
	d.AddBreakpoint("bp1", "auth", nil, nil)
	bps := d.GetBreakpoints()
	if len(bps) != 1 {
		t.Fatalf("expected 1 bp, got %d", len(bps))
	}
	if bps[0].ID != "bp1" {
		t.Errorf("expected bp1, got %s", bps[0].ID)
	}
	if !bps[0].Enabled {
		t.Error("bp should be enabled")
	}
}

func TestBreakpointTriggered(t *testing.T) {
	d := NewDebugger()
	var triggered int32
	d.AddBreakpoint("bp1", "auth", nil, func() { atomic.AddInt32(&triggered, 1) })
	d.Info("auth", "login", nil)
	d.Info("db", "query", nil)
	if atomic.LoadInt32(&triggered) != 1 {
		t.Errorf("expected 1 trigger")
	}
	if d.GetBreakpoints()[0].HitCount != 1 {
		t.Error("expected hit count 1")
	}
}

func TestBreakpointWithCondition(t *testing.T) {
	d := NewDebugger()
	var triggered int32
	met := false
	d.AddBreakpoint("bp1", "auth", func() bool { return met }, func() { atomic.AddInt32(&triggered, 1) })
	d.Info("auth", "m1", nil)
	if atomic.LoadInt32(&triggered) != 0 {
		t.Error("should not trigger")
	}
	met = true
	d.Info("auth", "m2", nil)
	if atomic.LoadInt32(&triggered) != 1 {
		t.Errorf("expected 1 trigger")
	}
}

func TestDisableBreakpoint(t *testing.T) {
	d := NewDebugger()
	var triggered int32
	d.AddBreakpoint("bp1", "auth", nil, func() { atomic.AddInt32(&triggered, 1) })
	d.DisableBreakpoint("bp1")
	d.Info("auth", "msg", nil)
	if atomic.LoadInt32(&triggered) != 0 {
		t.Error("disabled bp should not trigger")
	}
}

func TestEnableBreakpoint(t *testing.T) {
	d := NewDebugger()
	var triggered int32
	d.AddBreakpoint("bp1", "auth", nil, func() { atomic.AddInt32(&triggered, 1) })
	d.DisableBreakpoint("bp1")
	d.EnableBreakpoint("bp1")
	d.Info("auth", "msg", nil)
	if atomic.LoadInt32(&triggered) != 1 {
		t.Error("re-enabled bp should trigger")
	}
}

func TestRemoveBreakpoint(t *testing.T) {
	d := NewDebugger()
	var triggered int32
	d.AddBreakpoint("bp1", "auth", nil, func() { atomic.AddInt32(&triggered, 1) })
	d.RemoveBreakpoint("bp1")
	d.Info("auth", "msg", nil)
	if atomic.LoadInt32(&triggered) != 0 {
		t.Error("removed bp should not trigger")
	}
	if len(d.GetBreakpoints()) != 0 {
		t.Error("expected 0 bps")
	}
}

func TestBreakpointNonexistentOps(t *testing.T) {
	d := NewDebugger()
	d.RemoveBreakpoint("x")
	d.EnableBreakpoint("x")
	d.DisableBreakpoint("x")
}

func TestMultipleBreakpointsSameComponent(t *testing.T) {
	d := NewDebugger()
	var c1, c2 int32
	d.AddBreakpoint("bp1", "auth", nil, func() { atomic.AddInt32(&c1, 1) })
	d.AddBreakpoint("bp2", "auth", nil, func() { atomic.AddInt32(&c2, 1) })
	d.Info("auth", "msg", nil)
	if atomic.LoadInt32(&c1) != 1 || atomic.LoadInt32(&c2) != 1 {
		t.Error("both bps should trigger")
	}
}

func TestTakeSnapshot(t *testing.T) {
	d := NewDebugger()
	d.TakeSnapshot()
	ss := d.GetSnapshots()
	if len(ss) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(ss))
	}
	if ss[0].Timestamp.IsZero() {
		t.Error("timestamp should not be zero")
	}
	if ss[0].Goroutines < 1 {
		t.Errorf("expected >= 1 goroutine, got %d", ss[0].Goroutines)
	}
}

func TestMultipleSnapshots(t *testing.T) {
	d := NewDebugger()
	d.TakeSnapshot()
	d.TakeSnapshot()
	d.TakeSnapshot()
	if len(d.GetSnapshots()) != 3 {
		t.Errorf("expected 3, got %d", len(d.GetSnapshots()))
	}
}

func TestGetSnapshotsReturnsCopy(t *testing.T) {
	d := NewDebugger()
	d.TakeSnapshot()
	s := d.GetSnapshots()
	s = append(s, MemorySnapshot{})
	if len(d.GetSnapshots()) != 1 {
		t.Error("should not affect internal state")
	}
}

func TestAddWatcher(t *testing.T) {
	d := NewDebugger()
	d.AddWatcher("counter", func() interface{} { return 42 })
	w := d.GetWatchers()
	if len(w) != 1 {
		t.Fatalf("expected 1 watcher, got %d", len(w))
	}
	if w["counter"] != 42 {
		t.Errorf("expected 42, got %v", w["counter"])
	}
}

func TestMultipleWatchers(t *testing.T) {
	d := NewDebugger()
	d.AddWatcher("a", func() interface{} { return "alpha" })
	d.AddWatcher("b", func() interface{} { return "beta" })
	w := d.GetWatchers()
	if len(w) != 2 {
		t.Fatalf("expected 2 watchers, got %d", len(w))
	}
	if w["a"] != "alpha" || w["b"] != "beta" {
		t.Error("unexpected watcher values")
	}
}

func TestWatcherDynamicValues(t *testing.T) {
	d := NewDebugger()
	counter := 0
	d.AddWatcher("c", func() interface{} { counter++; return counter })
	w1 := d.GetWatchers()
	w2 := d.GetWatchers()
	if w1["c"] == w2["c"] {
		t.Error("watcher should return dynamic values")
	}
}

func TestGetWatchersReturnsCopy(t *testing.T) {
	d := NewDebugger()
	d.AddWatcher("a", func() interface{} { return 1 })
	w := d.GetWatchers()
	w["b"] = 2
	if len(d.GetWatchers()) != 1 {
		t.Error("should not affect internal state")
	}
}

func TestBreakpointHitCountMultiple(t *testing.T) {
	d := NewDebugger()
	d.AddBreakpoint("bp1", "comp", nil, nil)
	for i := 0; i < 10; i++ {
		d.Info("comp", "msg", nil)
	}
	if d.GetBreakpoints()[0].HitCount != 10 {
		t.Errorf("expected 10, got %d", d.GetBreakpoints()[0].HitCount)
	}
}

func TestGetCallerInfo(t *testing.T) {
	caller := getCallerInfo(1)
	if caller == "unknown" {
		t.Error("expected valid caller info")
	}
}

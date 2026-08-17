package skill

import (
	"fmt"
	"runtime"
	"sync"
	"time"
)

type LogLevel int

const (
	LevelDebug LogLevel = iota
	LevelInfo
	LevelWarn
	LevelError
	LevelFatal
)

func (l LogLevel) String() string {
	return []string{"DEBUG", "INFO", "WARN", "ERROR", "FATAL"}[l]
}

type LogEntry struct {
	Timestamp time.Time
	Level     LogLevel
	Component string
	Message   string
	Fields    map[string]interface{}
	Caller    string
}

type Breakpoint struct {
	ID        string
	Component string
	Condition func() bool
	Action    func()
	Enabled   bool
	HitCount  int
}

type MemorySnapshot struct {
	Timestamp  time.Time
	HeapAlloc  uint64
	HeapInuse  uint64
	StackInuse uint64
	Goroutines int
	NumGC      uint32
}

type Debugger struct {
	logs        []LogEntry
	breakpoints map[string]*Breakpoint
	snapshots   []MemorySnapshot
	watchers    map[string]func() interface{}
	mu          sync.RWMutex
	minLevel    LogLevel
}

func NewDebugger() *Debugger {
	return &Debugger{
		logs:        make([]LogEntry, 0),
		breakpoints: make(map[string]*Breakpoint),
		snapshots:   make([]MemorySnapshot, 0),
		watchers:    make(map[string]func() interface{}),
		minLevel:    LevelDebug,
	}
}

func (d *Debugger) Log(level LogLevel, component, message string, fields map[string]interface{}) {
	if level < d.minLevel {
		return
	}

	caller := getCallerInfo(2)

	entry := LogEntry{
		Timestamp: time.Now(),
		Level:     level,
		Component: component,
		Message:   message,
		Fields:    fields,
		Caller:    caller,
	}

	d.mu.Lock()
	d.logs = append(d.logs, entry)
	d.mu.Unlock()

	d.checkBreakpoints(component)
}

func (d *Debugger) Debug(component, message string, fields map[string]interface{}) {
	d.Log(LevelDebug, component, message, fields)
}

func (d *Debugger) Info(component, message string, fields map[string]interface{}) {
	d.Log(LevelInfo, component, message, fields)
}

func (d *Debugger) Warn(component, message string, fields map[string]interface{}) {
	d.Log(LevelWarn, component, message, fields)
}

func (d *Debugger) Error(component, message string, fields map[string]interface{}) {
	d.Log(LevelError, component, message, fields)
}

func (d *Debugger) AddBreakpoint(id, component string, condition func() bool, action func()) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.breakpoints[id] = &Breakpoint{
		ID:        id,
		Component: component,
		Condition: condition,
		Action:    action,
		Enabled:   true,
	}
}

func (d *Debugger) RemoveBreakpoint(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.breakpoints, id)
}

func (d *Debugger) EnableBreakpoint(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if bp, ok := d.breakpoints[id]; ok {
		bp.Enabled = true
	}
}

func (d *Debugger) DisableBreakpoint(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if bp, ok := d.breakpoints[id]; ok {
		bp.Enabled = false
	}
}

func (d *Debugger) checkBreakpoints(component string) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	for _, bp := range d.breakpoints {
		if bp.Enabled && bp.Component == component {
			if bp.Condition == nil || bp.Condition() {
				bp.HitCount++
				if bp.Action != nil {
					bp.Action()
				}
			}
		}
	}
}

func (d *Debugger) TakeSnapshot() {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	snapshot := MemorySnapshot{
		Timestamp:  time.Now(),
		HeapAlloc:  m.HeapAlloc,
		HeapInuse:  m.HeapInuse,
		StackInuse: m.StackInuse,
		Goroutines: runtime.NumGoroutine(),
		NumGC:      m.NumGC,
	}

	d.mu.Lock()
	d.snapshots = append(d.snapshots, snapshot)
	d.mu.Unlock()
}

func (d *Debugger) AddWatcher(name string, fn func() interface{}) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.watchers[name] = fn
}

func (d *Debugger) GetWatchers() map[string]interface{} {
	d.mu.RLock()
	defer d.mu.RUnlock()

	result := make(map[string]interface{})
	for name, fn := range d.watchers {
		result[name] = fn()
	}
	return result
}

func (d *Debugger) GetLogs(level LogLevel) []LogEntry {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var result []LogEntry
	for _, log := range d.logs {
		if log.Level >= level {
			result = append(result, log)
		}
	}
	return result
}

func (d *Debugger) GetLogsByComponent(component string) []LogEntry {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var result []LogEntry
	for _, log := range d.logs {
		if log.Component == component {
			result = append(result, log)
		}
	}
	return result
}

func (d *Debugger) GetBreakpoints() []*Breakpoint {
	d.mu.RLock()
	defer d.mu.RUnlock()

	result := make([]*Breakpoint, 0, len(d.breakpoints))
	for _, bp := range d.breakpoints {
		result = append(result, bp)
	}
	return result
}

func (d *Debugger) GetSnapshots() []MemorySnapshot {
	d.mu.RLock()
	defer d.mu.RUnlock()

	result := make([]MemorySnapshot, len(d.snapshots))
	copy(result, d.snapshots)
	return result
}

func (d *Debugger) SetMinLevel(level LogLevel) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.minLevel = level
}

func (d *Debugger) Clear() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.logs = make([]LogEntry, 0)
	d.snapshots = make([]MemorySnapshot, 0)
}

func getCallerInfo(skip int) string {
	_, file, line, ok := runtime.Caller(skip)
	if ok {
		return fmt.Sprintf("%s:%d", file, line)
	}
	return "unknown"
}

package server

import (
	"fmt"
	"log"
	"strings"
	"time"

	"go.uber.org/zap"

	"loopworker/internal/config"
	"loopworker/pkg/event"
	"loopworker/pkg/logger"
)

// Diagnostics returns the startup self-check plus live runtime state. Host
// commands (for example a doctor subcommand) can call this on a running server
// or on a freshly built one; it never mutates server state.
func (s *Server) Diagnostics() *Diagnostics {
	s.mu.Lock()
	cached, listener, run, workers, st := s.diag, s.listener, s.pluginRun, len(s.workerIDs), s.state
	s.mu.Unlock()

	var d *Diagnostics
	if cached == nil {
		d = Diagnose(s.cfg)
	} else {
		d = cached.clone()
		if st == stateRunning && listener != nil {
			for i := range d.Checks {
				if d.Checks[i].Name == "port" {
					d.Checks[i].Status = StatusOK
					d.Checks[i].Detail = fmt.Sprintf("listening on %s (configured %s)", listener.Addr(), s.cfg.Addr())
					d.Checks[i].Hint = ""
				}
			}
		}
	}

	if run != nil {
		replaced := false
		for i := range d.Checks {
			if d.Checks[i].Name == "plugins" {
				d.Checks[i].Detail = run.Summary()
				if len(run.Failed) > 0 {
					d.Checks[i].Status = StatusFail
				}
				replaced = true
			}
		}
		if !replaced {
			d.add(Check{Name: "plugins", Status: StatusOK, Detail: run.Summary()})
		}
	}

	d.Runtime = s.runtimeState(workers)
	return d
}

func (s *Server) runtimeState(workerIDs int) map[string]any {
	state := map[string]any{
		"state":              s.State(),
		"uptime":             time.Since(s.startedAt()).String(),
		"listen":             s.cfg.Addr(),
		"workers_started":    workerIDs,
		"workers_configured": s.cfg.Workers.Count,
		"workers_running":    0,
		"data_dir":           s.cfg.Data.Dir,
		"plugins_dir":        s.cfg.Plugins.Dir,
		"database":           s.cfg.DBPath(),
		"security":           s.cfg.Security.Posture(),
	}
	components := s.GetComponents()
	if components == nil {
		return state
	}
	stats := components.Executor.GetStats()
	state["workers_running"] = stats.ActiveWorkers
	state["tasks_executed"] = stats.TotalTasksRun
	state["tasks_failed"] = stats.TotalTasksFailed
	state["scheduler"] = components.Scheduler.GetStats()

	busStats := components.EventBus.GetStats()
	state["events"] = map[string]any{
		"published":   busStats.EventsPublished,
		"delivered":   busStats.EventsDelivered,
		"dropped":     busStats.EventsDropped,
		"subscribers": busStats.SubscriberCount,
		"store":       storeStats(components.EventStore),
	}
	if components.eventStoreErr != nil {
		state["event_store_error"] = components.eventStoreErr.Error()
	}
	sbStats := components.Sandbox.GetStats()
	state["sandbox"] = map[string]any{
		"plugins_loaded":    sbStats.PluginsLoaded,
		"executions":        sbStats.ExecutionsTotal,
		"executions_failed": sbStats.ExecutionsFailed,
	}
	state["health"] = s.Health().Status
	return state
}

func storeStats(store event.EventStore) string {
	if store == nil {
		return "not configured"
	}
	if reporter, ok := store.(event.StoreStatistics); ok {
		st := reporter.Stats()
		return fmt.Sprintf("%d events, %d bytes in %d segment(s) at %s", st.Events, st.TotalBytes, st.Segments, st.BasePath)
	}
	return fmt.Sprintf("%T", store)
}

// DoctorDump is the human-readable diagnosis for a doctor subcommand or a bug
// report. It returns the dump even when the self-check failed, together with the
// failure as error.
func (s *Server) DoctorDump() (string, error) {
	d := s.Diagnostics()
	return d.Text(), d.FatalError()
}

func (d *Diagnostics) clone() *Diagnostics {
	cp := *d
	cp.Checks = make([]Check, len(d.Checks))
	copy(cp.Checks, d.Checks)
	cp.Values = append([]config.Entry(nil), d.Values...)
	cp.Unapplied = append([]string(nil), d.Unapplied...)
	return &cp
}

// errorLogWriter forwards net/http's internal error log through the configured
// logger so a process does not write diagnostics to two places.
type errorLogWriter struct{}

func (errorLogWriter) Write(p []byte) (int, error) {
	logger.Warn("http server", zap.String("message", strings.TrimSpace(string(p))))
	return len(p), nil
}

func httpErrorLog() *log.Logger { return log.New(errorLogWriter{}, "", 0) }

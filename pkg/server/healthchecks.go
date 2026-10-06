package server

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"

	"loopworker/internal/config"
	"loopworker/internal/core/selfheal"
	"loopworker/pkg/event"
	"loopworker/pkg/logger"
	"loopworker/pkg/plugin"
)

// Names of the health checks this process registers. They are the operator
// facing identity of a check: they are printed once at boot and again, with the
// reason, every time one of them fails.
const (
	healthCheckTaskDatabase = "task_database"
	healthCheckEventStore   = "event_store"
	healthCheckPlugins      = "plugins"
)

const (
	// healthCheckInterval is deliberately slower than the 10s self-heal default:
	// every check here touches the local filesystem or an already-open handle,
	// so a fast interval would buy nothing but probe noise on the data volume.
	healthCheckInterval = 30 * time.Second
	healthCheckTimeout  = 5 * time.Second
)

// envRequiredPlugin names the plugin that must be loaded for the plugins check
// to pass. It is optional: unset means "at least one plugin must be loaded",
// which is what a fresh install needs in order to run a task at all. Set it
// when a deployment depends on one specific plugin, so losing that one plugin
// is reported instead of being masked by another one that happens to be
// installed.
const envRequiredPlugin = "LOOPWORKER_HEALTH_REQUIRED_PLUGIN"

// RegisterHealthChecks fills the self-healer's registry with checks against the
// dependencies this process actually has, so StartHealthChecks monitors
// something. It must be called before StartHealthChecks: that function snapshots
// the registry, and a check registered afterwards is never run.
//
// It is safe to call with a nil healer or nil components: boot() only reaches
// it when selfheal.enabled is on.
func RegisterHealthChecks(sh *selfheal.SelfHealer, c *Components, cfg *config.Config) {
	if sh == nil || c == nil || cfg == nil {
		return
	}
	checks := []*selfheal.HealthCheck{
		taskDatabaseCheck(c),
		eventStoreCheck(c),
		pluginsCheck(c, cfg),
	}
	names := make([]string, 0, len(checks))
	for _, check := range checks {
		check.Interval, check.Timeout = healthCheckInterval, healthCheckTimeout
		sh.RegisterHealthCheck(logged(check))
		names = append(names, check.Name)
	}
	logger.Info("health checks registered",
		zap.Strings("checks", names),
		zap.Duration("interval", healthCheckInterval),
		zap.String("required_plugin", os.Getenv(envRequiredPlugin)))
}

// taskDatabaseCheck proves the scheduler's storage still takes writes, and that
// it has not degraded. A store that fell back to memory keeps answering reads,
// so the only symptoms are tasks vanishing on restart; LastError is the
// scheduler's own record of that.
func taskDatabaseCheck(c *Components) *selfheal.HealthCheck {
	db := selfheal.WritableFileCheck(healthCheckTaskDatabase, c.Scheduler.StorageInfo().Path)
	writable := db.Check
	db.Check = func(ctx context.Context) error {
		if info := c.Scheduler.StorageInfo(); info.LastError != "" {
			return fmt.Errorf("task storage at %s degraded: %s", info.Path, info.LastError)
		}
		return writable(ctx)
	}
	return db
}

// eventStoreCheck covers the append path, which is where a full disk or a
// revoked permission first shows up: the store itself reports both, and the
// directory it was given is probed for writability so a volume that went away
// or read-only is caught before the next publish silently drops events.
func eventStoreCheck(c *Components) *selfheal.HealthCheck {
	return &selfheal.HealthCheck{
		Name: healthCheckEventStore,
		Check: func(ctx context.Context) error {
			if c.eventStoreErr != nil {
				return fmt.Errorf("event store unavailable, events are not persisted: %w", c.eventStoreErr)
			}
			stats, ok := c.EventStore.(event.StoreStatistics)
			if !ok {
				// A store that cannot report its own state cannot be judged;
				// say nothing rather than invent a verdict.
				return nil
			}
			s := stats.Stats()
			if s.DiskFull {
				return fmt.Errorf("event store under %s is out of disk space after %d write error(s); events are no longer persisted", s.BasePath, s.WriteErrors)
			}
			return selfheal.WritableDirCheck(healthCheckEventStore, s.BasePath).Check(ctx)
		},
	}
}

// pluginsCheck proves there is a plugin to run. A server with none registers
// every worker against "no-plugin-installed" and dead-letters every task with
// "plugin not found" while reporting itself healthy, which is exactly the
// failure this project has already shipped once. Discovery alone is not
// enough: a directory that is present but fails to load produces the same
// dead letters, so the check requires a loaded plugin unless auto_load is off.
func pluginsCheck(c *Components, cfg *config.Config) *selfheal.HealthCheck {
	return &selfheal.HealthCheck{
		Name: healthCheckPlugins,
		Check: func(context.Context) error {
			dirs, err := c.PluginMgr.DiscoverPlugins()
			if err != nil {
				return fmt.Errorf("plugins directory %s is not readable: %w", cfg.Plugins.Dir, err)
			}
			if len(dirs) == 0 {
				return fmt.Errorf("no plugin found in %s, so every task would be dead-lettered with %q", cfg.Plugins.Dir, noPluginID)
			}
			if !cfg.Plugins.AutoLoad {
				return nil
			}
			loaded := c.PluginMgr.ListPlugins()
			if want := strings.TrimSpace(os.Getenv(envRequiredPlugin)); want != "" {
				for _, p := range loaded {
					if p.Name == want {
						return nil
					}
				}
				return fmt.Errorf("%s requires plugin %q, but the loaded plugins in %s are [%s]", envRequiredPlugin, want, cfg.Plugins.Dir, pluginNames(loaded))
			}
			if len(loaded) == 0 {
				return fmt.Errorf("%d plugin(s) discovered in %s but none loaded, so every task would be dead-lettered with %q", len(dirs), cfg.Plugins.Dir, noPluginID)
			}
			return nil
		},
	}
}

// logged adds the operator-visible half of a health check. A failing check
// otherwise reaches exactly one place - the aggregate "selfheal: degraded"
// string in /healthz - which names neither the check nor the cause, and a
// health check whose failure cannot be read is one operators learn to ignore.
// The boot line above names every check once; this names the failing one, with
// its reason, on every interval it stays broken.
func logged(check *selfheal.HealthCheck) *selfheal.HealthCheck {
	probe := check.Check
	check.Check = func(ctx context.Context) error {
		err := probe(ctx)
		if err != nil {
			logger.Warn("health check failed",
				zap.String("check", check.Name),
				zap.Error(err))
		}
		return err
	}
	return check
}

func pluginNames(loaded []*plugin.PluginInfo) string {
	names := make([]string, 0, len(loaded))
	for _, p := range loaded {
		names = append(names, p.Name)
	}
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

// sortedHealthReportNames returns the registered check names in a stable order.
// Health() renders a map, so iterating it directly would give operators a
// different key order on every scrape and make two identical servers look
// different in a diff.
func sortedHealthReportNames(sh *selfheal.SelfHealer) []string {
	reports := sh.HealthReports()
	names := make([]string, 0, len(reports))
	for name := range reports {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

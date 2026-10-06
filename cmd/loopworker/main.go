package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"loopworker/internal/config"
	"loopworker/pkg/logger"
	"loopworker/pkg/server"
	"loopworker/version"
)

// stringFlags maps a flag name to the configuration key it overrides.
var stringFlags = map[string]string{
	"host":             "server.host",
	"plugins-dir":      "plugins.dir",
	"data-dir":         "data.dir",
	"work-dir":         "work_dir",
	"log-level":        "logging.level",
	"shutdown-timeout": "server.shutdown_timeout",
}

var (
	cfgFile        string
	stringFlagVals = map[string]*string{}
)

var rootCmd = &cobra.Command{
	Use:   "loopworker",
	Short: "LoopWorker is a self-service WASM work-loop engine",
	Long: `LoopWorker runs the event bus, scheduler, dispatcher and WASM sandbox in one
process and serves the REST API on the configured port.

Configuration is resolved in this order, each layer overriding the previous one:
  defaults < config file < environment (LOOPWORKER_*) < flags

With no configuration at all it stores data under ~/.loopworker and listens on
port 19527. Every start prints a self-check of the resolved configuration, the
data and plugin directories, free disk, the database file and port availability.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runServer,
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "loopworker: %v\n", err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(version.Get().String())
		},
	})

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "",
		"configuration file (YAML or JSON); otherwise $LOOPWORKER_CONFIG, ./config.yaml, ~/.loopworker/config.yaml, /etc/loopworker/config.yaml")

	usage := map[string]string{
		"host":             "address to bind (default 0.0.0.0)",
		"plugins-dir":      "directory holding WASM plugin folders",
		"data-dir":         "directory holding the task database and event log",
		"work-dir":         "base directory that data.dir and plugins.dir derive from",
		"log-level":        "log level: debug, info, warn, error",
		"shutdown-timeout": "how long draining workers may take on shutdown (default 10s)",
	}
	// Persistent so `doctor` resolves the same configuration the server would.
	for flagName := range stringFlags {
		stringFlagVals[flagName] = rootCmd.PersistentFlags().String(flagName, "", usage[stringFlags[flagName]])
	}
	rootCmd.PersistentFlags().IntP("port", "p", 0, "port to listen on (default 19527)")
	rootCmd.PersistentFlags().Int("workers", 0, "workers pumping the task queue (default 4)")

	rootCmd.AddCommand(doctorCmd)
}

// overrides turns the flags the user actually set into configuration keys, so an
// unset flag never masks the config file or the environment.
func overrides(cmd *cobra.Command) map[string]string {
	out := map[string]string{}
	for flagName, key := range stringFlags {
		if cmd.Flags().Changed(flagName) {
			out[key] = *stringFlagVals[flagName]
		}
	}
	if cmd.Flags().Changed("port") {
		port, _ := cmd.Flags().GetInt("port")
		out["server.port"] = fmt.Sprint(port)
	}
	if cmd.Flags().Changed("workers") {
		count, _ := cmd.Flags().GetInt("workers")
		out["workers.count"] = fmt.Sprint(count)
	}
	return out
}

// loadConfig resolves the configuration this invocation would run with. Other
// host commands (for example a doctor subcommand) call it to report the same
// values the server uses.
func loadConfig(cmd *cobra.Command) (*config.Config, error) {
	return config.Load(config.Options{ConfigFile: cfgFile, Flags: overrides(cmd)})
}

func runServer(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig(cmd)
	if err != nil {
		return err
	}
	if err := server.SetupLogging(cfg.Logging); err != nil {
		return err
	}

	// The scheduler is given the absolute database path from configuration, so the
	// process keeps the caller's working directory untouched.
	srv, err := server.New(cfg)
	if err != nil {
		return err
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Start() }()

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	select {
	case err := <-errCh:
		if err != nil {
			return err
		}
		logger.Info("server stopped", zap.String("state", srv.State()))
		return nil
	case sig := <-sigCh:
		fmt.Fprintf(os.Stderr, "\n%s received: draining connections and workers for up to %s\n", sig, cfg.Server.ShutdownTimeout)
		go func() {
			<-sigCh
			fmt.Fprintln(os.Stderr, "second signal: exiting immediately, drain skipped")
			os.Exit(130)
		}()
		return srv.Stop()
	}
}

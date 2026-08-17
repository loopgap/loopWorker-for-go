package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"loopworker/internal/config"
	"loopworker/pkg/server"
	"loopworker/pkg/utils"
	"loopworker/version"
)

var cfgFile string

var rootCmd = &cobra.Command{
	Use:   "loopworker",
	Short: "LoopWorker is an industrial-grade WASM workflow engine",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Load configuration
		cfg, err := config.LoadConfig(cfgFile)
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}

		// Create server config from internal config
		serverCfg := &server.Config{
			Port:       cfg.Port,
			PluginsDir: cfg.PluginsDir,
			DataDir:    cfg.DataDir,
		}

		srv := server.New(serverCfg)
		fmt.Printf("Starting LoopWorker on port %d...\n", cfg.Port)

		errCh := make(chan error, 1)
		utils.GoSafe(context.Background(), func(ctx context.Context) {
			if err := srv.Start(); err != nil {
				errCh <- err
			}
		})

		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

		select {
		case err := <-errCh:
			return fmt.Errorf("server error: %w", err)
		case <-sigCh:
			fmt.Println("\nShutting down gracefully...")
			return srv.Stop()
		}
	},
}

func init() {
	cobra.OnInitialize(initConfig)

	rootCmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(version.Get().String())
		},
	})

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.loopworker/config.yaml)")
	rootCmd.Flags().IntP("port", "p", 19527, "Port to run the server on")
	rootCmd.Flags().String("plugins-dir", "", "Directory for WASM plugins")
	rootCmd.Flags().String("data-dir", "", "Directory for SQLite data")

	viper.BindPFlag("port", rootCmd.Flags().Lookup("port"))
	viper.BindPFlag("plugins_dir", rootCmd.Flags().Lookup("plugins-dir"))
	viper.BindPFlag("data_dir", rootCmd.Flags().Lookup("data-dir"))
}

func initConfig() {
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		home, err := os.UserHomeDir()
		cobra.CheckErr(err)

		viper.AddConfigPath(home + "/.loopworker")
		viper.AddConfigPath("/etc/loopworker")
		viper.AddConfigPath(".")
		viper.SetConfigType("yaml")
		viper.SetConfigName("config")
	}

	viper.AutomaticEnv() // read in environment variables that match

	// Default values
	home, _ := os.UserHomeDir()
	viper.SetDefault("work_dir", home+"/.loopworker")
	viper.SetDefault("plugins_dir", home+"/.loopworker/plugins")
	viper.SetDefault("data_dir", home+"/.loopworker/data")
	viper.SetDefault("language", "en")
	viper.SetDefault("theme", "glass")

	if err := viper.ReadInConfig(); err == nil {
		fmt.Fprintln(os.Stderr, "Using config file:", viper.ConfigFileUsed())
	}
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

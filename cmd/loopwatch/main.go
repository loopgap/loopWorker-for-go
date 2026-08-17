// Package main implements loopwatch, the LoopWorker monitoring tool.
//
// loopwatch is a command-line tool for monitoring LoopWorker server,
// including real-time metrics, logs, and system health.
//
// Usage:
//
//	loopwatch [flags]
//
// Flags:
//
//	-s, --server    Server URL (default: http://localhost:19527)
//	-i, --interval  Refresh interval (default: 5s)
//	-m, --metrics   Show metrics
//	-l, --logs      Show logs
//	-h, --health    Show health status
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
)

type Metrics struct {
	TasksCreated   int64   `json:"tasks_created"`
	TasksCompleted int64   `json:"tasks_completed"`
	TasksFailed    int64   `json:"tasks_failed"`
	ActiveWorkers  int     `json:"active_workers"`
	IdleWorkers    int     `json:"idle_workers"`
	QueueSize      int     `json:"queue_size"`
	AvgExecTime    string  `json:"avg_exec_time"`
	TasksPerSecond float64 `json:"tasks_per_second"`
}

type HealthStatus struct {
	Status     string            `json:"status"`
	Uptime     string            `json:"uptime"`
	Components map[string]string `json:"components"`
}

type LogEntry struct {
	Timestamp string `json:"timestamp"`
	Level     string `json:"level"`
	Message   string `json:"message"`
	TaskID    string `json:"task_id,omitempty"`
	WorkerID  string `json:"worker_id,omitempty"`
}

var rootCmd = &cobra.Command{
	Use:   "loopwatch",
	Short: "LoopWorker monitoring tool",
	Long:  "A command-line tool for monitoring LoopWorker server in real-time.",
	RunE: func(cmd *cobra.Command, args []string) error {
		server, _ := cmd.Flags().GetString("server")
		interval, _ := cmd.Flags().GetDuration("interval")
		showMetrics, _ := cmd.Flags().GetBool("metrics")
		showLogs, _ := cmd.Flags().GetBool("logs")
		showHealth, _ := cmd.Flags().GetBool("health")

		fmt.Printf("Monitoring LoopWorker at %s (refresh every %v)\n", server, interval)
		fmt.Println("Press Ctrl+C to stop")
		fmt.Println()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Handle Ctrl+C
		go func() {
			// In real implementation, handle signal
			time.Sleep(time.Hour)
			cancel()
		}()

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				// Clear screen
				fmt.Print("\033[H\033[2J")

				if showHealth {
					if err := showHealthStatus(server); err != nil {
						fmt.Fprintf(os.Stderr, "Error fetching health: %v\n", err)
					}
					fmt.Println()
				}

				if showMetrics {
					if err := showMetricsData(server); err != nil {
						fmt.Fprintf(os.Stderr, "Error fetching metrics: %v\n", err)
					}
					fmt.Println()
				}

				if showLogs {
					if err := showLogsData(server); err != nil {
						fmt.Fprintf(os.Stderr, "Error fetching logs: %v\n", err)
					}
				}
			}
		}
	},
}

func showHealthStatus(server string) error {
	c := NewAPIClient(server)
	health, err := c.HealthCheck()
	if err != nil {
		return fmt.Errorf("get health: %w", err)
	}

	fmt.Println("=== Health Status ===")
	if status, ok := health["status"]; ok {
		fmt.Printf("Status: %v\n", status)
	}
	if uptime, ok := health["uptime"]; ok {
		fmt.Printf("Uptime: %v\n", uptime)
	}
	if components, ok := health["components"].(map[string]interface{}); ok {
		fmt.Println("Components:")
		for name, status := range components {
			fmt.Printf("  %s: %v\n", name, status)
		}
	}

	return nil
}

func showMetricsData(server string) error {
	c := NewAPIClient(server)
	metrics, err := c.GetMetrics()
	if err != nil {
		return fmt.Errorf("get metrics: %w", err)
	}

	fmt.Println("=== Metrics ===")
	for key, value := range metrics {
		fmt.Printf("  %s: %v\n", key, value)
	}

	return nil
}

func showLogsData(server string) error {
	// TODO: Implement API call to get logs
	logs := []LogEntry{
		{
			Timestamp: time.Now().Format(time.RFC3339),
			Level:     "info",
			Message:   "Task completed successfully",
			TaskID:    "task-123",
			WorkerID:  "worker-1",
		},
		{
			Timestamp: time.Now().Add(-time.Second).Format(time.RFC3339),
			Level:     "info",
			Message:   "Worker started",
			WorkerID:  "worker-2",
		},
	}

	fmt.Println("=== Recent Logs ===")
	for _, entry := range logs {
		fmt.Printf("[%s] %s: %s", entry.Timestamp, entry.Level, entry.Message)
		if entry.TaskID != "" {
			fmt.Printf(" (task=%s)", entry.TaskID)
		}
		if entry.WorkerID != "" {
			fmt.Printf(" (worker=%s)", entry.WorkerID)
		}
		fmt.Println()
	}

	return nil
}

func init() {
	rootCmd.Flags().StringP("server", "s", "http://localhost:19527", "Server URL")
	rootCmd.Flags().DurationP("interval", "i", 5*time.Second, "Refresh interval")
	rootCmd.Flags().BoolP("metrics", "m", false, "Show metrics")
	rootCmd.Flags().BoolP("logs", "l", false, "Show logs")
	rootCmd.Flags().Bool("health", true, "Show health status")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

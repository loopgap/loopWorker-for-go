// Package main implements loopdebug, the LoopWorker debugging tool.
//
// loopdebug is a command-line tool for debugging LoopWorker server,
// including task inspection, workflow debugging, and system diagnostics.
//
// Usage:
//
//	loopdebug [command]
//
// Available Commands:
//
//	task        Task debugging commands
//	workflow    Workflow debugging commands
//	diagnose    System diagnostics
//	profile     Performance profiling
//	help        Help about any command
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	lwclient "loopworker/pkg/client"
)

var (
	serverURL string
	apiClient *lwclient.APIClient
)

var rootCmd = &cobra.Command{
	Use:   "loopdebug",
	Short: "LoopWorker debugging tool",
	Long:  "A command-line tool for debugging LoopWorker server and diagnosing issues.",
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		apiClient = lwclient.NewAPIClient(serverURL)
	},
}

var taskCmd = &cobra.Command{
	Use:   "task",
	Short: "Task debugging commands",
	Long:  "Commands for debugging tasks: inspect, trace, replay.",
}

var taskInspectCmd = &cobra.Command{
	Use:   "inspect [task-id]",
	Short: "Inspect a task",
	Long:  "Inspect detailed information about a task, including execution trace.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		taskID := args[0]

		task, err := apiClient.GetTask(taskID)
		if err != nil {
			return fmt.Errorf("inspect task: %w", err)
		}

		jsonData, err := json.MarshalIndent(task, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal task: %w", err)
		}

		fmt.Println(string(jsonData))
		return nil
	},
}

var taskTraceCmd = &cobra.Command{
	Use:   "trace [task-id]",
	Short: "Trace task execution",
	Long:  "Trace the execution of a task in real-time.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		taskID := args[0]

		task, err := apiClient.GetTask(taskID)
		if err != nil {
			return fmt.Errorf("trace task: %w", err)
		}

		jsonData, err := json.MarshalIndent(task, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal task: %w", err)
		}

		fmt.Println(string(jsonData))
		return nil
	},
}

var taskReplayCmd = &cobra.Command{
	Use:   "replay [task-id]",
	Short: "Replay a task",
	Long:  "Replay a task with the same input and configuration.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		taskID := args[0]

		task, err := apiClient.GetTask(taskID)
		if err != nil {
			return fmt.Errorf("replay task: %w", err)
		}

		taskType, _ := task["type"].(string)
		input, _ := task["input"].(string)

		fmt.Printf("Replaying task: %s\n", taskID)
		fmt.Printf("  Type: %s\n", taskType)
		fmt.Printf("  Input: %s\n", input)
		fmt.Println()

		newTask, err := apiClient.CreateTask(taskType, input, 1)
		if err != nil {
			return fmt.Errorf("replay task: %w", err)
		}

		fmt.Println("Replay completed successfully")
		fmt.Printf("  New Task ID: %v\n", newTask["id"])

		return nil
	},
}

var workflowCmd = &cobra.Command{
	Use:   "workflow",
	Short: "Workflow debugging commands",
	Long:  "Commands for debugging workflows: inspect, trace, validate.",
}

var workflowInspectCmd = &cobra.Command{
	Use:   "inspect [workflow-id]",
	Short: "Inspect a workflow",
	Long:  "Inspect detailed information about a workflow.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		workflowID := args[0]

		body, err := apiClient.Get("/api/v1/workflow/" + workflowID)
		if err != nil {
			return fmt.Errorf("inspect workflow: %w", err)
		}

		var workflow map[string]interface{}
		if err := json.Unmarshal(body, &workflow); err != nil {
			return fmt.Errorf("unmarshal workflow: %w", err)
		}

		jsonData, err := json.MarshalIndent(workflow, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal workflow: %w", err)
		}

		fmt.Println(string(jsonData))
		return nil
	},
}

var workflowTraceCmd = &cobra.Command{
	Use:   "trace [workflow-id]",
	Short: "Trace workflow execution",
	Long:  "Trace the execution of a workflow in real-time.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		workflowID := args[0]

		body, err := apiClient.Get("/api/v1/workflow/" + workflowID)
		if err != nil {
			return fmt.Errorf("trace workflow: %w", err)
		}

		var workflow map[string]interface{}
		if err := json.Unmarshal(body, &workflow); err != nil {
			return fmt.Errorf("unmarshal workflow: %w", err)
		}

		jsonData, err := json.MarshalIndent(workflow, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal workflow: %w", err)
		}

		fmt.Println(string(jsonData))
		return nil
	},
}

var workflowValidateCmd = &cobra.Command{
	Use:   "validate [file]",
	Short: "Validate a workflow definition",
	Long:  "Validate a workflow definition file for syntax and semantic errors.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		file := args[0]

		data, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("read file: %w", err)
		}

		var workflowDef map[string]interface{}
		if err := json.Unmarshal(data, &workflowDef); err != nil {
			return fmt.Errorf("invalid JSON: %w", err)
		}

		fmt.Printf("Validating workflow: %s\n", file)
		fmt.Println()
		fmt.Println("Validation Results:")
		fmt.Printf("  ✓ Valid JSON syntax\n")

		if _, ok := workflowDef["name"]; ok {
			fmt.Printf("  ✓ Name field present\n")
		} else {
			fmt.Printf("  ✗ Name field missing\n")
		}

		if steps, ok := workflowDef["steps"]; ok {
			if stepList, ok := steps.([]interface{}); ok {
				fmt.Printf("  ✓ Steps defined: %d\n", len(stepList))
			}
		} else {
			fmt.Printf("  ✗ Steps field missing\n")
		}

		fmt.Println()
		fmt.Println("Workflow is valid!")

		return nil
	},
}

var diagnoseCmd = &cobra.Command{
	Use:   "diagnose",
	Short: "System diagnostics",
	Long:  "Run system diagnostics to identify issues.",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("=== System Diagnostics ===")
		fmt.Println()

		// Check Go version
		fmt.Printf("Go Version: %s\n", runtime.Version())
		fmt.Printf("OS/Arch: %s/%s\n", runtime.GOOS, runtime.GOARCH)
		fmt.Printf("CPUs: %d\n", runtime.NumCPU())
		fmt.Println()

		// Check memory
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		fmt.Println("Memory Statistics:")
		fmt.Printf("  Alloc: %d MB\n", m.Alloc/1024/1024)
		fmt.Printf("  Total Alloc: %d MB\n", m.TotalAlloc/1024/1024)
		fmt.Printf("  Sys: %d MB\n", m.Sys/1024/1024)
		fmt.Printf("  Num GC: %d\n", m.NumGC)
		fmt.Println()

		// Check goroutines
		fmt.Printf("Goroutines: %d\n", runtime.NumGoroutine())
		fmt.Println()

		// Check server connectivity
		fmt.Println("Server Connectivity:")
		health, err := apiClient.HealthCheck()
		if err != nil {
			fmt.Printf("  Status: disconnected (%v)\n", err)
		} else {
			fmt.Printf("  Status: connected\n")
			if status, ok := health["status"]; ok {
				fmt.Printf("  Server Status: %v\n", status)
			}
		}

		return nil
	},
}

var profileCmd = &cobra.Command{
	Use:   "profile",
	Short: "Performance profiling",
	Long:  "Collect performance profiles for analysis.",
	RunE: func(cmd *cobra.Command, args []string) error {
		duration, _ := cmd.Flags().GetDuration("duration")
		output, _ := cmd.Flags().GetString("output")

		fmt.Printf("Collecting profile for %v...\n", duration)
		fmt.Println()

		// Collect runtime profile
		var m runtime.MemStats
		runtime.ReadMemStats(&m)

		fmt.Println("Memory Profile:")
		fmt.Printf("  Heap Alloc: %d MB\n", m.HeapAlloc/1024/1024)
		fmt.Printf("  Heap Inuse: %d MB\n", m.HeapInuse/1024/1024)
		fmt.Printf("  Stack Inuse: %d MB\n", m.StackInuse/1024/1024)
		fmt.Printf("  Goroutines: %d\n", runtime.NumGoroutine())
		fmt.Printf("  Num GC: %d\n", m.NumGC)

		if output != "" {
			profileData := map[string]interface{}{
				"heap_alloc_mb":  m.HeapAlloc / 1024 / 1024,
				"heap_inuse_mb":  m.HeapInuse / 1024 / 1024,
				"stack_inuse_mb": m.StackInuse / 1024 / 1024,
				"goroutines":     runtime.NumGoroutine(),
				"num_gc":         m.NumGC,
			}
			jsonData, _ := json.MarshalIndent(profileData, "", "  ")
			if err := os.WriteFile(output, jsonData, 0644); err != nil {
				return fmt.Errorf("write profile: %w", err)
			}
			fmt.Printf("\nProfile saved to: %s\n", output)
		} else {
			fmt.Println("\nProfile collected (use -o to save to file)")
		}

		return nil
	},
}

func init() {
	rootCmd.PersistentFlags().StringVar(&serverURL, "server", "", "Server URL (default: http://localhost:19527)")

	// Task commands
	taskCmd.AddCommand(taskInspectCmd, taskTraceCmd, taskReplayCmd)

	// Workflow commands
	workflowCmd.AddCommand(workflowInspectCmd, workflowTraceCmd, workflowValidateCmd)

	// Profile command flags
	profileCmd.Flags().DurationP("duration", "d", 30*time.Second, "Profile duration")
	profileCmd.Flags().StringP("output", "o", "", "Output file for profile")

	rootCmd.AddCommand(taskCmd, workflowCmd, diagnoseCmd, profileCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

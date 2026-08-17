// Package main implements loopctl, the LoopWorker control CLI.
//
// loopctl is a command-line tool for managing LoopWorker server,
// including task management, workflow control, and system monitoring.
//
// Usage:
//
//	loopctl [command]
//
// Available Commands:
//
//	task        Task management commands
//	workflow    Workflow management commands
//	config      Configuration management
//	status      Show server status
//	help        Help about any command
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "loopctl",
	Short: "LoopWorker control CLI",
	Long:  "A command-line tool for managing LoopWorker server, tasks, workflows, and configuration.",
}

var taskCmd = &cobra.Command{
	Use:   "task",
	Short: "Task management commands",
	Long:  "Commands for managing tasks: create, list, get, cancel, delete.",
}

var taskListCmd = &cobra.Command{
	Use:   "list",
	Short: "List tasks",
	Long:  "List all tasks with optional filters.",
	RunE: func(cmd *cobra.Command, args []string) error {
		state, _ := cmd.Flags().GetString("state")
		taskType, _ := cmd.Flags().GetString("type")
		limit, _ := cmd.Flags().GetInt("limit")

		// TODO: Implement API client to list tasks
		fmt.Printf("Listing tasks (state=%s, type=%s, limit=%d)\n", state, taskType, limit)
		return nil
	},
}

var taskCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new task",
	Long:  "Create a new task with specified type and input.",
	RunE: func(cmd *cobra.Command, args []string) error {
		taskType, _ := cmd.Flags().GetString("type")
		input, _ := cmd.Flags().GetString("input")
		priority, _ := cmd.Flags().GetInt("priority")

		// TODO: Implement API client to create task
		fmt.Printf("Creating task: type=%s, input=%s, priority=%d\n", taskType, input, priority)
		return nil
	},
}

var taskGetCmd = &cobra.Command{
	Use:   "get [task-id]",
	Short: "Get task details",
	Long:  "Get detailed information about a specific task.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		taskID := args[0]

		// TODO: Implement API client to get task
		fmt.Printf("Getting task: %s\n", taskID)
		return nil
	},
}

var taskCancelCmd = &cobra.Command{
	Use:   "cancel [task-id]",
	Short: "Cancel a task",
	Long:  "Cancel a running or pending task.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		taskID := args[0]

		// TODO: Implement API client to cancel task
		fmt.Printf("Cancelling task: %s\n", taskID)
		return nil
	},
}

var taskDeleteCmd = &cobra.Command{
	Use:   "delete [task-id]",
	Short: "Delete a task",
	Long:  "Delete a completed or failed task.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		taskID := args[0]

		// TODO: Implement API client to delete task
		fmt.Printf("Deleting task: %s\n", taskID)
		return nil
	},
}

var workflowCmd = &cobra.Command{
	Use:   "workflow",
	Short: "Workflow management commands",
	Long:  "Commands for managing workflows: create, list, get, execute.",
}

var workflowListCmd = &cobra.Command{
	Use:   "list",
	Short: "List workflows",
	Long:  "List all workflows.",
	RunE: func(cmd *cobra.Command, args []string) error {
		// TODO: Implement API client to list workflows
		fmt.Println("Listing workflows")
		return nil
	},
}

var workflowCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new workflow",
	Long:  "Create a new workflow from a definition file.",
	RunE: func(cmd *cobra.Command, args []string) error {
		file, _ := cmd.Flags().GetString("file")

		// TODO: Implement API client to create workflow
		fmt.Printf("Creating workflow from file: %s\n", file)
		return nil
	},
}

var workflowExecuteCmd = &cobra.Command{
	Use:   "execute [workflow-id]",
	Short: "Execute a workflow",
	Long:  "Execute a workflow with optional input.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		workflowID := args[0]
		input, _ := cmd.Flags().GetString("input")

		// TODO: Implement API client to execute workflow
		fmt.Printf("Executing workflow: %s, input=%s\n", workflowID, input)
		return nil
	},
}

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Configuration management",
	Long:  "Commands for managing LoopWorker configuration.",
}

var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show current configuration",
	Long:  "Display the current LoopWorker configuration.",
	RunE: func(cmd *cobra.Command, args []string) error {
		// TODO: Implement config loading and display
		fmt.Println("Current configuration:")
		fmt.Println("  Port: 19527")
		fmt.Println("  Plugins Dir: ~/.loopworker/plugins")
		fmt.Println("  Data Dir: ~/.loopworker/data")
		return nil
	},
}

var configSetCmd = &cobra.Command{
	Use:   "set [key] [value]",
	Short: "Set configuration value",
	Long:  "Set a configuration value.",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		key := args[0]
		value := args[1]

		// TODO: Implement config set
		fmt.Printf("Setting config: %s = %s\n", key, value)
		return nil
	},
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show server status",
	Long:  "Display the current status of the LoopWorker server.",
	RunE: func(cmd *cobra.Command, args []string) error {
		// TODO: Implement API client to get status
		type Status struct {
			Status    string `json:"status"`
			Uptime    string `json:"uptime"`
			Workers   int    `json:"workers"`
			Tasks     int    `json:"tasks"`
			QueueSize int    `json:"queue_size"`
		}

		status := Status{
			Status:    "running",
			Uptime:    "24h",
			Workers:   4,
			Tasks:     100,
			QueueSize: 10,
		}

		jsonData, err := json.MarshalIndent(status, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal status: %w", err)
		}

		fmt.Println(string(jsonData))
		return nil
	},
}

func init() {
	// Task commands
	taskListCmd.Flags().StringP("state", "s", "", "Filter by task state")
	taskListCmd.Flags().StringP("type", "t", "", "Filter by task type")
	taskListCmd.Flags().IntP("limit", "l", 20, "Limit number of results")

	taskCreateCmd.Flags().StringP("type", "t", "", "Task type (required)")
	taskCreateCmd.Flags().StringP("input", "i", "", "Task input")
	taskCreateCmd.Flags().IntP("priority", "p", 1, "Task priority (0-3)")
	taskCreateCmd.MarkFlagRequired("type")

	workflowCreateCmd.Flags().StringP("file", "f", "", "Workflow definition file (required)")
	workflowCreateCmd.MarkFlagRequired("file")

	workflowExecuteCmd.Flags().StringP("input", "i", "", "Workflow input")

	// Add subcommands
	taskCmd.AddCommand(taskListCmd, taskCreateCmd, taskGetCmd, taskCancelCmd, taskDeleteCmd)
	workflowCmd.AddCommand(workflowListCmd, workflowCreateCmd, workflowExecuteCmd)
	configCmd.AddCommand(configShowCmd, configSetCmd)

	rootCmd.AddCommand(taskCmd, workflowCmd, configCmd, statusCmd)
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
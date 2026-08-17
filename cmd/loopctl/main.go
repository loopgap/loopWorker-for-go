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

var (
	serverURL string
	client    *APIClient
)

var rootCmd = &cobra.Command{
	Use:   "loopctl",
	Short: "LoopWorker control CLI",
	Long:  "A command-line tool for managing LoopWorker server, tasks, workflows, and configuration.",
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		client = NewAPIClient(serverURL)
	},
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

		tasks, err := client.ListTasks(state, taskType, limit)
		if err != nil {
			return fmt.Errorf("list tasks: %w", err)
		}

		jsonData, err := json.MarshalIndent(tasks, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal tasks: %w", err)
		}

		fmt.Println(string(jsonData))
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

		task, err := client.CreateTask(taskType, input, priority)
		if err != nil {
			return fmt.Errorf("create task: %w", err)
		}

		jsonData, err := json.MarshalIndent(task, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal task: %w", err)
		}

		fmt.Println(string(jsonData))
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

		task, err := client.GetTask(taskID)
		if err != nil {
			return fmt.Errorf("get task: %w", err)
		}

		jsonData, err := json.MarshalIndent(task, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal task: %w", err)
		}

		fmt.Println(string(jsonData))
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

		_, err := client.post("/api/v1/tasks/"+taskID+"/cancel", nil)
		if err != nil {
			return fmt.Errorf("cancel task: %w", err)
		}

		fmt.Printf("Task %s cancelled successfully\n", taskID)
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

		if err := client.DeleteTask(taskID); err != nil {
			return fmt.Errorf("delete task: %w", err)
		}

		fmt.Printf("Task %s deleted successfully\n", taskID)
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
		workflows, err := client.ListWorkflows()
		if err != nil {
			return fmt.Errorf("list workflows: %w", err)
		}

		jsonData, err := json.MarshalIndent(workflows, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal workflows: %w", err)
		}

		fmt.Println(string(jsonData))
		return nil
	},
}

var workflowCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new workflow",
	Long:  "Create a new workflow from a definition file.",
	RunE: func(cmd *cobra.Command, args []string) error {
		file, _ := cmd.Flags().GetString("file")

		data, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("read file: %w", err)
		}

		var workflowDef map[string]interface{}
		if err := json.Unmarshal(data, &workflowDef); err != nil {
			return fmt.Errorf("parse workflow definition: %w", err)
		}

		_, err = client.post("/api/v1/workflow", workflowDef)
		if err != nil {
			return fmt.Errorf("create workflow: %w", err)
		}

		fmt.Println("Workflow created successfully")
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

		var inputData interface{}
		if input != "" {
			if err := json.Unmarshal([]byte(input), &inputData); err != nil {
				inputData = input
			}
		}

		result, err := client.ExecuteWorkflow(workflowID, inputData)
		if err != nil {
			return fmt.Errorf("execute workflow: %w", err)
		}

		jsonData, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal result: %w", err)
		}

		fmt.Println(string(jsonData))
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
		body, err := client.get("/api/v1/config")
		if err != nil {
			fmt.Println("Current configuration:")
			fmt.Println("  Port: 19527")
			fmt.Println("  Plugins Dir: ~/.loopworker/plugins")
			fmt.Println("  Data Dir: ~/.loopworker/data")
			return nil
		}

		var config map[string]interface{}
		if err := json.Unmarshal(body, &config); err != nil {
			return fmt.Errorf("unmarshal config: %w", err)
		}

		jsonData, err := json.MarshalIndent(config, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal config: %w", err)
		}

		fmt.Println(string(jsonData))
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

		data := map[string]interface{}{key: value}

		_, err := client.post("/api/v1/config", data)
		if err != nil {
			return fmt.Errorf("set config: %w", err)
		}

		fmt.Printf("Configuration updated: %s = %s\n", key, value)
		return nil
	},
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show server status",
	Long:  "Display the current status of the LoopWorker server.",
	RunE: func(cmd *cobra.Command, args []string) error {
		health, err := client.HealthCheck()
		if err != nil {
			return fmt.Errorf("server not reachable: %w", err)
		}

		jsonData, err := json.MarshalIndent(health, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal status: %w", err)
		}

		fmt.Println(string(jsonData))
		return nil
	},
}

var metricsCmd = &cobra.Command{
	Use:   "metrics",
	Short: "Show server metrics",
	Long:  "Display the current metrics of the LoopWorker server.",
	RunE: func(cmd *cobra.Command, args []string) error {
		metrics, err := client.GetMetrics()
		if err != nil {
			return fmt.Errorf("get metrics: %w", err)
		}

		jsonData, err := json.MarshalIndent(metrics, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal metrics: %w", err)
		}

		fmt.Println(string(jsonData))
		return nil
	},
}

var logsCmd = &cobra.Command{
	Use:   "logs",
	Short: "Show server logs",
	Long:  "Display recent logs from the LoopWorker server.",
	RunE: func(cmd *cobra.Command, args []string) error {
		logs, err := client.GetLogs()
		if err != nil {
			return fmt.Errorf("get logs: %w", err)
		}

		jsonData, err := json.MarshalIndent(logs, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal logs: %w", err)
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

	rootCmd.AddCommand(taskCmd, workflowCmd, configCmd, statusCmd, metricsCmd, logsCmd)
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

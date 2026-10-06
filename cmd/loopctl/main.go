// Package main implements loopctl, the LoopWorker control CLI.
//
// loopctl drives a running loopworker server over HTTP: tasks and workflows.
// Every endpoint it uses is registered by pkg/api — see docs/api/api-reference.md.
//
// Usage:
//
//	loopctl [command]
//
// Available Commands:
//
//	task        Task management commands
//	workflow    Workflow management commands
//	status      Show server status
//	version     Print version information
//	help        Help about any command
//
// Commands that are deliberately absent:
//
//	config      loopworker has no configuration endpoint. Inspect the server's
//	            own view with `loopworker doctor` on the host, or GET
//	            /runtime/stats on the admin listener.
//	metrics     /metrics and /logs live on the separate admin listener and
//	logs        /metrics is Prometheus text, not JSON. Use curl:
//	            curl -H "Authorization: Bearer $TOKEN" http://127.0.0.1:19528/metrics
//	workflow    There is no POST /api/v1/workflow. Workflows are registered
//	create       in-process, not over HTTP.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	lwclient "loopworker/pkg/client"
	"loopworker/version"
)

var (
	serverURL string
	apiClient *lwclient.APIClient
)

var rootCmd = &cobra.Command{
	Use:   "loopctl",
	Short: "LoopWorker control CLI",
	Long:  "A command-line tool for managing LoopWorker server tasks and workflows.",
	// Runtime failures (server down, 401, bad task id) are not usage errors;
	// dumping the full usage block after them buries the actual message.
	// SilenceUsage is set here rather than on the struct because cobra parses
	// flags before PersistentPreRun: a bad flag still earns the usage block.
	// main() prints the error itself, prefixed with the program name.
	SilenceErrors: true,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		cmd.SilenceUsage = true
		apiClient = lwclient.NewAPIClient(serverURL)
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

		tasks, err := apiClient.ListTasks(state, taskType, limit)
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

		task, err := apiClient.CreateTask(taskType, input, priority)
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

		task, err := apiClient.GetTask(taskID)
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

		_, err := apiClient.Post("/api/v1/tasks/"+taskID+"/cancel", nil)
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

		if err := apiClient.DeleteTask(taskID); err != nil {
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
		workflows, err := apiClient.ListWorkflows()
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

		result, err := apiClient.ExecuteWorkflow(workflowID, inputData)
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

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show server status",
	Long:  "Display the current status of the LoopWorker server.",
	RunE: func(cmd *cobra.Command, args []string) error {
		health, err := apiClient.HealthCheck()
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

func init() {
	// Task commands
	taskListCmd.Flags().StringP("state", "s", "", "Filter by task state")
	taskListCmd.Flags().StringP("type", "t", "", "Filter by task type")
	taskListCmd.Flags().IntP("limit", "l", 20, "Limit number of results")

	taskCreateCmd.Flags().StringP("type", "t", "", "Task type (required)")
	taskCreateCmd.Flags().StringP("input", "i", "", "Task input")
	taskCreateCmd.Flags().IntP("priority", "p", 1, "Task priority (0-3)")
	_ = taskCreateCmd.MarkFlagRequired("type")

	workflowExecuteCmd.Flags().StringP("input", "i", "", "Workflow input")

	// The flag loopctl always needed: without it serverURL stayed "" and the
	// only way to reach a non-default server was the LOOPWORKER_URL env var.
	rootCmd.PersistentFlags().StringVar(&serverURL, "server", "",
		"Server URL (default: $LOOPWORKER_URL, else http://localhost:19527)")

	// Add subcommands
	taskCmd.AddCommand(taskListCmd, taskCreateCmd, taskGetCmd, taskCancelCmd, taskDeleteCmd)
	workflowCmd.AddCommand(workflowListCmd, workflowExecuteCmd)

	rootCmd.AddCommand(taskCmd, workflowCmd, statusCmd)

	rootCmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(version.Get().String())
		},
	})
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "loopctl: "+err.Error())
		os.Exit(1)
	}
}

// Package main implements loopsim, the LoopWorker simulation tool.
//
// loopsim is a command-line tool for simulating LoopWorker workloads,
// including task generation, workflow simulation, and load testing.
//
// Usage:
//
//	loopsim [flags]
//
// Flags:
//
//	-d, --duration    Simulation duration (default: 30s)
//	-c, --concurrency Number of concurrent workers (default: 4)
//	-r, --rate        Tasks per second (default: 10)
//	-t, --type        Task type to simulate (default: "echo")
//	-w, --workflow    Simulate workflow execution
//	-o, --output      Output file for results
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"
)

type SimulationResult struct {
	Duration        time.Duration `json:"duration"`
	TotalTasks      int64         `json:"total_tasks"`
	CompletedTasks  int64         `json:"completed_tasks"`
	FailedTasks     int64         `json:"failed_tasks"`
	AvgTaskDuration time.Duration `json:"avg_task_duration"`
	TasksPerSecond  float64       `json:"tasks_per_second"`
	Concurrency     int           `json:"concurrency"`
	Rate            int           `json:"rate"`
}

var rootCmd = &cobra.Command{
	Use:   "loopsim",
	Short: "LoopWorker simulation tool",
	Long:  "A command-line tool for simulating LoopWorker workloads and load testing.",
	RunE: func(cmd *cobra.Command, args []string) error {
		duration, _ := cmd.Flags().GetDuration("duration")
		concurrency, _ := cmd.Flags().GetInt("concurrency")
		rate, _ := cmd.Flags().GetInt("rate")
		taskType, _ := cmd.Flags().GetString("type")
		workflow, _ := cmd.Flags().GetBool("workflow")
		outputFile, _ := cmd.Flags().GetString("output")

		fmt.Printf("Starting simulation...\n")
		fmt.Printf("  Duration: %v\n", duration)
		fmt.Printf("  Concurrency: %d\n", concurrency)
		fmt.Printf("  Rate: %d tasks/sec\n", rate)
		fmt.Printf("  Task Type: %s\n", taskType)
		fmt.Printf("  Workflow: %v\n", workflow)
		fmt.Println()

		var result *SimulationResult
		var err error

		if workflow {
			result, err = simulateWorkflow(duration, concurrency, rate)
		} else {
			result, err = simulateTasks(duration, concurrency, rate, taskType)
		}

		if err != nil {
			return fmt.Errorf("simulation failed: %w", err)
		}

		printResult(result)

		if outputFile != "" {
			if err := saveResult(result, outputFile); err != nil {
				return fmt.Errorf("save result: %w", err)
			}
			fmt.Printf("\nResults saved to: %s\n", outputFile)
		}

		return nil
	},
}

func simulateTasks(duration time.Duration, concurrency, rate int, taskType string) (*SimulationResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()

	var (
		totalTasks     int64
		completedTasks int64
		failedTasks    int64
		totalDuration  int64
	)

	start := time.Now()

	// Create task generator
	taskCh := make(chan int, rate*2)

	// Start task generator
	go func() {
		ticker := time.NewTicker(time.Second / time.Duration(rate))
		defer ticker.Stop()

		taskID := 0
		for {
			select {
			case <-ctx.Done():
				close(taskCh)
				return
			case <-ticker.C:
				taskCh <- taskID
				taskID++
			}
		}
	}()

	// Start workers
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for range taskCh {
				select {
				case <-ctx.Done():
					return
				default:
					taskStart := time.Now()
					atomic.AddInt64(&totalTasks, 1)

					// Simulate task execution with random duration
					execTime := time.Duration(rand.Intn(100)+10) * time.Millisecond
					time.Sleep(execTime)

					taskDuration := time.Since(taskStart).Nanoseconds()

					// Simulate random failures (5% failure rate)
					if rand.Float64() < 0.05 {
						atomic.AddInt64(&failedTasks, 1)
					} else {
						atomic.AddInt64(&completedTasks, 1)
					}

					atomic.AddInt64(&totalDuration, taskDuration)
				}
			}
		}(i)
	}

	wg.Wait()
	totalTime := time.Since(start)

	completed := atomic.LoadInt64(&completedTasks)
	failed := atomic.LoadInt64(&failedTasks)
	totalDur := atomic.LoadInt64(&totalDuration)

	var avgDur time.Duration
	if completed > 0 {
		avgDur = time.Duration(totalDur / completed)
	}

	var tasksPerSecond float64
	if totalTime.Seconds() > 0 {
		tasksPerSecond = float64(completed) / totalTime.Seconds()
	}

	return &SimulationResult{
		Duration:        totalTime,
		TotalTasks:      atomic.LoadInt64(&totalTasks),
		CompletedTasks:  completed,
		FailedTasks:     failed,
		AvgTaskDuration: avgDur,
		TasksPerSecond:  tasksPerSecond,
		Concurrency:     concurrency,
		Rate:            rate,
	}, nil
}

func simulateWorkflow(duration time.Duration, concurrency, rate int) (*SimulationResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()

	var (
		totalTasks     int64
		completedTasks int64
		failedTasks    int64
		totalDuration  int64
	)

	start := time.Now()

	// Create workflow generator
	workflowCh := make(chan int, rate)

	// Start workflow generator
	go func() {
		ticker := time.NewTicker(time.Second / time.Duration(rate))
		defer ticker.Stop()

		workflowID := 0
		for {
			select {
			case <-ctx.Done():
				close(workflowCh)
				return
			case <-ticker.C:
				workflowCh <- workflowID
				workflowID++
			}
		}
	}()

	// Start workers
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for range workflowCh {
				select {
				case <-ctx.Done():
					return
				default:
					workflowStart := time.Now()

					// Simulate workflow with multiple steps
					numSteps := rand.Intn(5) + 2
					for step := 0; step < numSteps; step++ {
						atomic.AddInt64(&totalTasks, 1)

						// Simulate step execution
						stepTime := time.Duration(rand.Intn(50)+10) * time.Millisecond
						time.Sleep(stepTime)

						// Simulate random failures
						if rand.Float64() < 0.1 {
							atomic.AddInt64(&failedTasks, 1)
						} else {
							atomic.AddInt64(&completedTasks, 1)
						}
					}

					workflowDuration := time.Since(workflowStart).Nanoseconds()
					atomic.AddInt64(&totalDuration, workflowDuration)
				}
			}
		}(i)
	}

	wg.Wait()
	totalTime := time.Since(start)

	completed := atomic.LoadInt64(&completedTasks)
	failed := atomic.LoadInt64(&failedTasks)
	totalDur := atomic.LoadInt64(&totalDuration)

	var avgDur time.Duration
	if completed > 0 {
		avgDur = time.Duration(totalDur / completed)
	}

	var tasksPerSecond float64
	if totalTime.Seconds() > 0 {
		tasksPerSecond = float64(completed) / totalTime.Seconds()
	}

	return &SimulationResult{
		Duration:        totalTime,
		TotalTasks:      atomic.LoadInt64(&totalTasks),
		CompletedTasks:  completed,
		FailedTasks:     failed,
		AvgTaskDuration: avgDur,
		TasksPerSecond:  tasksPerSecond,
		Concurrency:     concurrency,
		Rate:            rate,
	}, nil
}

func printResult(result *SimulationResult) {
	fmt.Println("=== Simulation Results ===")
	fmt.Printf("Duration:         %v\n", result.Duration)
	fmt.Printf("Total Tasks:      %d\n", result.TotalTasks)
	fmt.Printf("Completed Tasks:  %d\n", result.CompletedTasks)
	fmt.Printf("Failed Tasks:     %d\n", result.FailedTasks)
	fmt.Printf("Avg Task Duration: %v\n", result.AvgTaskDuration)
	fmt.Printf("Tasks/Second:     %.2f\n", result.TasksPerSecond)
	fmt.Printf("Concurrency:      %d\n", result.Concurrency)
	fmt.Printf("Rate:             %d tasks/sec\n", result.Rate)
}

func saveResult(result *SimulationResult, filename string) error {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filename, data, 0644)
}

func init() {
	rootCmd.Flags().DurationP("duration", "d", 30*time.Second, "Simulation duration")
	rootCmd.Flags().IntP("concurrency", "c", 4, "Number of concurrent workers")
	rootCmd.Flags().IntP("rate", "r", 10, "Tasks per second")
	rootCmd.Flags().StringP("type", "t", "echo", "Task type to simulate")
	rootCmd.Flags().BoolP("workflow", "w", false, "Simulate workflow execution")
	rootCmd.Flags().StringP("output", "o", "", "Output file for results")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

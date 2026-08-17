// Package main implements loopbench, the LoopWorker benchmarking tool.
//
// loopbench is a command-line tool for benchmarking LoopWorker performance,
// including task execution, workflow processing, and system throughput.
//
// Usage:
//
//	loopbench [flags]
//
// Flags:
//
//	-d, --duration    Benchmark duration (default: 10s)
//	-c, --concurrency Number of concurrent workers (default: 4)
//	-n, --tasks       Number of tasks to execute (default: 100)
//	-t, --type        Task type to benchmark (default: "echo")
//	-o, --output      Output file for results (default: stdout)
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"
)

type BenchmarkResult struct {
	TotalTasks      int           `json:"total_tasks"`
	CompletedTasks  int           `json:"completed_tasks"`
	FailedTasks     int           `json:"failed_tasks"`
	TotalDuration   time.Duration `json:"total_duration"`
	AvgTaskDuration time.Duration `json:"avg_task_duration"`
	MinTaskDuration time.Duration `json:"min_task_duration"`
	MaxTaskDuration time.Duration `json:"max_task_duration"`
	TasksPerSecond  float64       `json:"tasks_per_second"`
	Concurrency     int           `json:"concurrency"`
}

var rootCmd = &cobra.Command{
	Use:   "loopbench",
	Short: "LoopWorker benchmarking tool",
	Long:  "A command-line tool for benchmarking LoopWorker performance.",
	RunE: func(cmd *cobra.Command, args []string) error {
		duration, _ := cmd.Flags().GetDuration("duration")
		concurrency, _ := cmd.Flags().GetInt("concurrency")
		numTasks, _ := cmd.Flags().GetInt("tasks")
		taskType, _ := cmd.Flags().GetString("type")
		outputFile, _ := cmd.Flags().GetString("output")

		fmt.Printf("Starting benchmark...\n")
		fmt.Printf("  Duration: %v\n", duration)
		fmt.Printf("  Concurrency: %d\n", concurrency)
		fmt.Printf("  Tasks: %d\n", numTasks)
		fmt.Printf("  Task Type: %s\n", taskType)
		fmt.Println()

		result, err := runBenchmark(duration, concurrency, numTasks, taskType)
		if err != nil {
			return fmt.Errorf("benchmark failed: %w", err)
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

func runBenchmark(duration time.Duration, concurrency, numTasks int, taskType string) (*BenchmarkResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()

	var (
		completedTasks int64
		failedTasks    int64
		totalDuration  int64
		minDuration    int64 = 1<<63 - 1
		maxDuration    int64
	)

	start := time.Now()

	var wg sync.WaitGroup
	taskCh := make(chan int, numTasks)

	// Start workers
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

					// Simulate task execution
					// In real implementation, this would call the LoopWorker API
					time.Sleep(10 * time.Millisecond) // Simulate work

					taskDuration := time.Since(taskStart).Nanoseconds()
					atomic.AddInt64(&completedTasks, 1)
					atomic.AddInt64(&totalDuration, taskDuration)

					// Update min/max duration
					for {
						currentMin := atomic.LoadInt64(&minDuration)
						if taskDuration >= currentMin {
							break
						}
						if atomic.CompareAndSwapInt64(&minDuration, currentMin, taskDuration) {
							break
						}
					}
					for {
						currentMax := atomic.LoadInt64(&maxDuration)
						if taskDuration <= currentMax {
							break
						}
						if atomic.CompareAndSwapInt64(&maxDuration, currentMax, taskDuration) {
							break
						}
					}
				}
			}
		}(i)
	}

	// Send tasks
	for i := 0; i < numTasks; i++ {
		select {
		case <-ctx.Done():
			break
		case taskCh <- i:
		}
	}
	close(taskCh)

	wg.Wait()
	totalTime := time.Since(start)

	completed := atomic.LoadInt64(&completedTasks)
	failed := atomic.LoadInt64(&failedTasks)
	totalDur := atomic.LoadInt64(&totalDuration)
	minDur := atomic.LoadInt64(&minDuration)
	maxDur := atomic.LoadInt64(&maxDuration)

	var avgDur time.Duration
	if completed > 0 {
		avgDur = time.Duration(totalDur / completed)
	}

	var tasksPerSecond float64
	if totalTime.Seconds() > 0 {
		tasksPerSecond = float64(completed) / totalTime.Seconds()
	}

	return &BenchmarkResult{
		TotalTasks:      numTasks,
		CompletedTasks:  int(completed),
		FailedTasks:     int(failed),
		TotalDuration:   totalTime,
		AvgTaskDuration: avgDur,
		MinTaskDuration: time.Duration(minDur),
		MaxTaskDuration: time.Duration(maxDur),
		TasksPerSecond:  tasksPerSecond,
		Concurrency:     concurrency,
	}, nil
}

func printResult(result *BenchmarkResult) {
	fmt.Println("=== Benchmark Results ===")
	fmt.Printf("Total Tasks:      %d\n", result.TotalTasks)
	fmt.Printf("Completed Tasks:  %d\n", result.CompletedTasks)
	fmt.Printf("Failed Tasks:     %d\n", result.FailedTasks)
	fmt.Printf("Total Duration:   %v\n", result.TotalDuration)
	fmt.Printf("Avg Task Duration: %v\n", result.AvgTaskDuration)
	fmt.Printf("Min Task Duration: %v\n", result.MinTaskDuration)
	fmt.Printf("Max Task Duration: %v\n", result.MaxTaskDuration)
	fmt.Printf("Tasks/Second:     %.2f\n", result.TasksPerSecond)
	fmt.Printf("Concurrency:      %d\n", result.Concurrency)
}

func saveResult(result *BenchmarkResult, filename string) error {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filename, data, 0644)
}

func init() {
	rootCmd.Flags().DurationP("duration", "d", 10*time.Second, "Benchmark duration")
	rootCmd.Flags().IntP("concurrency", "c", 4, "Number of concurrent workers")
	rootCmd.Flags().IntP("tasks", "n", 100, "Number of tasks to execute")
	rootCmd.Flags().StringP("type", "t", "echo", "Task type to benchmark")
	rootCmd.Flags().StringP("output", "o", "", "Output file for results")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

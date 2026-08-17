// Package main demonstrates a simple LoopWorker example.
//
// This example shows how to:
// 1. Create a simple plugin
// 2. Register it with the sandbox
// 3. Execute a task through the plugin
//
// Usage:
//
//	go run examples/simple/main.go
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"loopworker/internal/core/sandbox"
	"loopworker/pkg/event"
	"loopworker/pkg/skill"
)

// SimplePlugin is a basic plugin that echoes its input.
type SimplePlugin struct {
	name string
}

func (p *SimplePlugin) Name() string {
	return p.name
}

func (p *SimplePlugin) Version() string {
	return "1.0.0"
}

func (p *SimplePlugin) RequiredSkills() []string {
	return []string{}
}

func (p *SimplePlugin) Execute(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
	// Simple echo with transformation
	output := fmt.Sprintf("Processed by %s: %s", p.name, string(input))
	return []byte(output), nil
}

func main() {
	fmt.Println("=== LoopWorker Simple Example ===")
	fmt.Println()

	// Create event bus
	bus := event.NewEventBus(nil)
	defer bus.Close()

	// Create sandbox
	sb := sandbox.NewSandbox(sandbox.SandboxConfig{
		MaxMemoryMB:   128,
		MaxCPUSeconds: 10,
		MaxOutputMB:   16,
		MaxConcurrent: 5,
	})
	sb.SetEventBus(bus)

	// Load plugin
	plugin := &SimplePlugin{name: "echo-plugin"}
	if err := sb.LoadPlugin("echo-plugin", plugin); err != nil {
		log.Fatalf("Failed to load plugin: %v", err)
	}

	fmt.Println("✓ Plugin loaded successfully")

	// Create skill context
	skillRegistry := skill.NewSkillRegistry()
	skillConfig := map[string]interface{}{}
	skillCtx := skillRegistry.BuildContext(nil, bus, nil, skillConfig)

	// Execute task
	fmt.Println("\nExecuting task...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	input := []byte("Hello, LoopWorker!")
	output, err := sb.Execute(ctx, "echo-plugin", input, skillCtx)
	if err != nil {
		log.Fatalf("Failed to execute task: %v", err)
	}

	fmt.Printf("✓ Task executed successfully\n")
	fmt.Printf("  Input:  %s\n", string(input))
	fmt.Printf("  Output: %s\n", string(output))

	// Execute multiple tasks
	fmt.Println("\nExecuting multiple tasks...")
	for i := 0; i < 5; i++ {
		input := []byte(fmt.Sprintf("Task %d", i+1))
		output, err := sb.Execute(ctx, "echo-plugin", input, skillCtx)
		if err != nil {
			log.Printf("Failed to execute task %d: %v", i+1, err)
			continue
		}
		fmt.Printf("  Task %d: %s\n", i+1, string(output))
	}

	// Get sandbox stats
	stats := sb.GetStats()
	fmt.Printf("\n=== Sandbox Statistics ===\n")
	fmt.Printf("Total Executions: %d\n", stats.ExecutionsTotal)
	fmt.Printf("Failed Executions: %d\n", stats.ExecutionsFailed)
	fmt.Printf("Total Exec Time: %v\n", stats.TotalExecTime)
	fmt.Printf("Max Exec Time: %v\n", stats.MaxExecTime)
	fmt.Printf("Plugins Loaded: %d\n", stats.PluginsLoaded)

	fmt.Println("\n✓ Example completed successfully!")
}

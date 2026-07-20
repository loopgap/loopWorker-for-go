package main

import (
	"context"
	"fmt"
	"loopworker/pkg/workflow"
)

func main() {
	engine := workflow.NewWorkflowEngine()
	wf := workflow.NewWorkflow("data-pipeline", "Data Processing Pipeline")

	wf.AddStep(&workflow.Step{
		ID: "extract",
		Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
			fmt.Println("Step 1: Extracting data...")
			return map[string]interface{}{"extracted": true}, nil
		},
	})

	wf.AddStep(&workflow.Step{
		ID:        "transform",
		DependsOn: []string{"extract"},
		Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
			fmt.Println("Step 2: Transforming data...")
			return map[string]interface{}{"transformed": true}, nil
		},
	})

	wf.AddStep(&workflow.Step{
		ID:        "validate",
		DependsOn: []string{"transform"},
		Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
			fmt.Println("Step 3: Validating data...")
			return map[string]interface{}{"validated": true}, nil
		},
	})

	wf.AddStep(&workflow.Step{
		ID:        "load",
		DependsOn: []string{"validate"},
		Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
			fmt.Println("Step 4: Loading data...")
			return map[string]interface{}{"loaded": true}, nil
		},
	})

	engine.Register(wf)
	if err := engine.Execute(context.Background(), "data-pipeline"); err != nil {
		fmt.Printf("Error: %v\n", err)
	}
}

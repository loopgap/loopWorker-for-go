package generator

import (
	"fmt"
	"strings"
	"time"
)

type TemplateType string

const (
	TemplateWorkflow  TemplateType = "workflow"
	TemplatePlugin    TemplateType = "plugin"
	TemplateAPI       TemplateType = "api"
	TemplateScheduler TemplateType = "scheduler"
	TemplateSecurity  TemplateType = "security"
)

type Template struct {
	Name        string
	Type        TemplateType
	Description string
	Content     string
	Variables   map[string]string
}

type Generator struct {
	templates map[TemplateType][]Template
}

func NewGenerator() *Generator {
	g := &Generator{
		templates: make(map[TemplateType][]Template),
	}
	g.loadTemplates()
	return g
}

func (g *Generator) loadTemplates() {
	g.templates[TemplateWorkflow] = []Template{
		{
			Name:        "basic-workflow",
			Type:        TemplateWorkflow,
			Description: "Basic sequential workflow",
			Content: `package main

import (
	"context"
	"fmt"
	"loopworker/pkg/workflow"
)

func main() {
	engine := workflow.NewWorkflowEngine()
	wf := workflow.NewWorkflow("{{.Name}}", "{{.Description}}")

	wf.AddStep(&workflow.Step{
		ID: "init",
		Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
			fmt.Println("Initializing...")
			return map[string]interface{}{"initialized": true}, nil
		},
	})

	wf.AddStep(&workflow.Step{
		ID:        "process",
		DependsOn: []string{"init"},
		Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
			fmt.Println("Processing...")
			return map[string]interface{}{"processed": true}, nil
		},
	})

	wf.AddStep(&workflow.Step{
		ID:        "cleanup",
		DependsOn: []string{"process"},
		Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
			fmt.Println("Cleaning up...")
			return map[string]interface{}{"cleaned": true}, nil
		},
	})

	engine.Register(wf)
	if err := engine.Execute(context.Background(), "{{.Name}}"); err != nil {
		fmt.Printf("Error: %v\n", err)
	}
}`,
		},
		{
			Name:        "parallel-workflow",
			Type:        TemplateWorkflow,
			Description: "Parallel execution workflow",
			Content: `package main

import (
	"context"
	"fmt"
	"loopworker/pkg/workflow"
)

func main() {
	engine := workflow.NewWorkflowEngine()
	wf := workflow.NewWorkflow("{{.Name}}", "{{.Description}}")

	wf.AddStep(&workflow.Step{
		ID: "task-a",
		Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
			fmt.Println("Task A executing...")
			return map[string]interface{}{"a_done": true}, nil
		},
	})

	wf.AddStep(&workflow.Step{
		ID: "task-b",
		Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
			fmt.Println("Task B executing...")
			return map[string]interface{}{"b_done": true}, nil
		},
	})

	wf.AddStep(&workflow.Step{
		ID:        "merge",
		DependsOn: []string{"task-a", "task-b"},
		Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
			fmt.Println("Merging results...")
			return map[string]interface{}{"merged": true}, nil
		},
	})

	engine.Register(wf)
	if err := engine.Execute(context.Background(), "{{.Name}}"); err != nil {
		fmt.Printf("Error: %v\n", err)
	}
}`,
		},
	}

	g.templates[TemplatePlugin] = []Template{
		{
			Name:        "basic-plugin",
			Type:        TemplatePlugin,
			Description: "Basic plugin template",
			Content: `package main

import (
	"context"
	"fmt"
)

func main() {
	fmt.Println("Plugin {{.Name}} loaded")
}

func Execute(ctx context.Context, input []byte) ([]byte, error) {
	fmt.Printf("Processing input: %s\n", string(input))
	return input, nil
}`,
		},
	}

	g.templates[TemplateAPI] = []Template{
		{
			Name:        "rest-endpoint",
			Type:        TemplateAPI,
			Description: "REST API endpoint",
			Content: `package api

import (
	"encoding/json"
	"net/http"
)

func Handle{{.Name}}(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		// Handle GET
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	case "POST":
		// Handle POST
		json.NewEncoder(w).Encode(map[string]string{"status": "created"})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}`,
		},
	}

	g.templates[TemplateScheduler] = []Template{
		{
			Name:        "periodic-task",
			Type:        TemplateScheduler,
			Description: "Periodic task scheduler",
			Content: `package main

import (
	"context"
	"fmt"
	"time"
	"loopworker/internal/core/scheduler"
	"loopworker/pkg/event"
)

func main() {
	ctx := context.Background()
	bus := event.NewEventBus(nil)
	s := scheduler.NewScheduler(bus)

	task, _ := s.CreateTask(ctx, "{{.Name}}", nil, nil)
	s.QueueTask(ctx, task.ID)

	ticker := time.NewTicker({{.Interval}})
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fmt.Println("Running periodic task...")
		}
	}
}`,
		},
	}

	g.templates[TemplateSecurity] = []Template{
		{
			Name:        "auth-middleware",
			Type:        TemplateSecurity,
			Description: "Authentication middleware",
			Content: `package middleware

import (
	"net/http"
	"strings"
)

func AuthMiddleware(tokenValidator func(string) bool) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			auth := r.Header.Get("Authorization")
			if auth == "" {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			token := strings.TrimPrefix(auth, "Bearer ")
			if !tokenValidator(token) {
				http.Error(w, "Invalid token", http.StatusUnauthorized)
				return
			}

			next(w, r)
		}
	}
}`,
		},
	}
}

func (g *Generator) Generate(templateType TemplateType, name string, variables map[string]string) (string, error) {
	templates, exists := g.templates[templateType]
	if !exists {
		return "", fmt.Errorf("template type %s not found", templateType)
	}

	for _, tmpl := range templates {
		if tmpl.Name == name {
			content := tmpl.Content
			for key, value := range variables {
				content = strings.ReplaceAll(content, "{{."+key+"}}", value)
			}
			return content, nil
		}
	}

	return "", fmt.Errorf("template %s not found", name)
}

func (g *Generator) ListTemplates(templateType TemplateType) []Template {
	templates, exists := g.templates[templateType]
	if !exists {
		return nil
	}
	return templates
}

func (g *Generator) ListAllTemplates() map[TemplateType][]Template {
	return g.templates
}

func (g *Generator) AddTemplate(tmpl Template) {
	if g.templates[tmpl.Type] == nil {
		g.templates[tmpl.Type] = make([]Template, 0)
	}
	g.templates[tmpl.Type] = append(g.templates[tmpl.Type], tmpl)
}

func CreateExampleWorkflow(name string) string {
	return fmt.Sprintf(`package main

import (
	"context"
	"fmt"
	"loopworker/pkg/workflow"
)

func main() {
	engine := workflow.NewWorkflowEngine()
	wf := workflow.NewWorkflow("%s", "Example workflow created at %s")

	wf.AddStep(&workflow.Step{
		ID: "step-1",
		Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
			fmt.Println("Step 1: Initialize")
			return map[string]interface{}{"step1": true}, nil
		},
	})

	wf.AddStep(&workflow.Step{
		ID:        "step-2",
		DependsOn: []string{"step-1"},
		Action: func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
			fmt.Println("Step 2: Process")
			return map[string]interface{}{"step2": true}, nil
		},
	})

	engine.Register(wf)
	if err := engine.Execute(context.Background(), "%s"); err != nil {
		fmt.Printf("Error: %%v\n", err)
	}
}`, name, time.Now().Format("2006-01-02"), name)
}

func CreateExamplePlugin(name string) string {
	return fmt.Sprintf(`package main

import (
	"context"
	"fmt"
)

func main() {
	fmt.Println("Plugin %s loaded")
}

func Execute(ctx context.Context, input []byte) ([]byte, error) {
	fmt.Printf("Plugin %s processing: %%s\n", string(input))
	return input, nil
}`, name, name)
}

func CreateExampleAPI(name string) string {
	return fmt.Sprintf(`package api

import (
	"encoding/json"
	"net/http"
)

func Handle%s(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	
	switch r.Method {
	case "GET":
		json.NewEncoder(w).Encode(map[string]string{
			"status": "ok",
			"action": "get",
			"name":   "%s",
		})
	case "POST":
		json.NewEncoder(w).Encode(map[string]string{
			"status": "created",
			"action": "post",
			"name":   "%s",
		})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}`, name, name, name)
}

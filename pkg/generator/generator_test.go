package generator

import (
	"testing"
)

func TestNewGenerator(t *testing.T) {
	gen := NewGenerator()
	if gen == nil {
		t.Fatal("generator should not be nil")
	}
}

func TestListTemplates(t *testing.T) {
	gen := NewGenerator()
	templates := gen.ListTemplates(TemplateWorkflow)
	if len(templates) == 0 {
		t.Error("expected workflow templates")
	}
}

func TestListAllTemplates(t *testing.T) {
	gen := NewGenerator()
	all := gen.ListAllTemplates()
	if len(all) == 0 {
		t.Error("expected templates")
	}
}

func TestGenerateWorkflow(t *testing.T) {
	gen := NewGenerator()
	variables := map[string]string{
		"Name":        "test-workflow",
		"Description": "Test workflow",
	}

	content, err := gen.Generate(TemplateWorkflow, "basic-workflow", variables)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}

	if !containsString(content, "test-workflow") {
		t.Error("generated content should contain workflow name")
	}
}

func TestGeneratePlugin(t *testing.T) {
	gen := NewGenerator()
	variables := map[string]string{
		"Name": "test-plugin",
	}

	content, err := gen.Generate(TemplatePlugin, "basic-plugin", variables)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}

	if !containsString(content, "test-plugin") {
		t.Error("generated content should contain plugin name")
	}
}

func TestGenerateAPI(t *testing.T) {
	gen := NewGenerator()
	variables := map[string]string{
		"Name": "Test",
	}

	content, err := gen.Generate(TemplateAPI, "rest-endpoint", variables)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}

	if !containsString(content, "Test") {
		t.Error("generated content should contain API name")
	}
}

func TestGenerateNotFound(t *testing.T) {
	gen := NewGenerator()
	_, err := gen.Generate(TemplateWorkflow, "nonexistent", nil)
	if err == nil {
		t.Error("expected error for nonexistent template")
	}
}

func TestCreateExampleWorkflow(t *testing.T) {
	content := CreateExampleWorkflow("my-workflow")
	if !containsString(content, "my-workflow") {
		t.Error("example should contain workflow name")
	}
}

func TestCreateExamplePlugin(t *testing.T) {
	content := CreateExamplePlugin("my-plugin")
	if !containsString(content, "my-plugin") {
		t.Error("example should contain plugin name")
	}
}

func TestCreateExampleAPI(t *testing.T) {
	content := CreateExampleAPI("MyAPI")
	if !containsString(content, "MyAPI") {
		t.Error("example should contain API name")
	}
}

func TestAddTemplate(t *testing.T) {
	gen := NewGenerator()
	tmpl := Template{
		Name:        "custom",
		Type:        TemplateWorkflow,
		Description: "Custom template",
		Content:     "custom content",
	}
	gen.AddTemplate(tmpl)

	templates := gen.ListTemplates(TemplateWorkflow)
	found := false
	for _, tmpl := range templates {
		if tmpl.Name == "custom" {
			found = true
			break
		}
	}
	if !found {
		t.Error("custom template should be added")
	}
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && (s[:len(substr)] == substr || containsString(s[1:], substr)))
}

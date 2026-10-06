package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const goodYAML = `
id: nightly-audit
name: Nightly audit
steps:
  - id: verify
    action: skill:research.anomaly
    from: scan
    depends_on: [scan, fetch]
    timeout: 30s
    max_retries: 2
  - id: scan
    action: plugin
    input: "audit"
  - id: fetch
    action: plugin
`

func TestParseDefinitionYAMLAndJSONAgree(t *testing.T) {
	yamlDef, err := ParseDefinition([]byte(goodYAML), "a.yaml")
	if err != nil {
		t.Fatalf("YAML: %v", err)
	}
	jsonDef, err := ParseDefinition([]byte(`{
	  "id": "nightly-audit",
	  "name": "Nightly audit",
	  "steps": [
	    {"id":"verify","action":"skill:research.anomaly","from":"scan","depends_on":["scan","fetch"],"timeout":"30s","max_retries":2},
	    {"id":"scan","action":"plugin","input":"audit"},
	    {"id":"fetch","action":"plugin"}
	  ]
	}`), "a.json")
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}

	if yamlDef.ID != jsonDef.ID || len(yamlDef.Steps) != len(jsonDef.Steps) {
		t.Fatalf("YAML and JSON decoded differently: %+v vs %+v", yamlDef, jsonDef)
	}
	if yamlDef.Source != "a.yaml" || jsonDef.Source != "a.json" {
		t.Errorf("Source must record the file: %q / %q", yamlDef.Source, jsonDef.Source)
	}
	timeout, err := yamlDef.Steps[0].TimeoutDuration()
	if err != nil || timeout.Seconds() != 30 {
		t.Errorf("timeout 30s decoded as %v (%v)", timeout, err)
	}
	if yamlDef.Steps[0].MaxRetries != 2 {
		t.Errorf("max_retries: got %d", yamlDef.Steps[0].MaxRetries)
	}
}

// TestValidateRejectsUnrunnableDefinitions is the important one: every reason a
// definition could never execute must be caught when the file is read, not when
// an operator presses execute.
func TestValidateRejectsUnrunnableDefinitions(t *testing.T) {
	cases := []struct{ name, yaml, wantSubstring string }{
		{"no id", "steps:\n  - {id: a, action: plugin}", "workflow id is required"},
		{"no steps", "id: x\nsteps: []", "declares no steps"},
		{"step without id", "id: x\nsteps:\n  - {action: plugin}", "every step needs an id"},
		{"duplicate step id", "id: x\nsteps:\n  - {id: a, action: plugin}\n  - {id: a, action: plugin}", "appears twice"},
		{"step without action", "id: x\nsteps:\n  - {id: a}", "action is required"},
		{"unknown dependency", "id: x\nsteps:\n  - {id: a, action: plugin, depends_on: [ghost]}", "not a step of this workflow"},
		{"from without depends_on", "id: x\nsteps:\n  - {id: a, action: plugin}\n  - {id: b, action: plugin, from: a}", "does not list it in depends_on"},
		{"bad timeout", "id: x\nsteps:\n  - {id: a, action: plugin, timeout: soon}", "is not a duration"},
		{"zero timeout", "id: x\nsteps:\n  - {id: a, action: plugin, timeout: 0s}", "must be positive"},
		{"cycle", "id: x\nsteps:\n  - {id: a, action: plugin, depends_on: [b]}\n  - {id: b, action: plugin, depends_on: [a]}", "dependency cycle"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseDefinition([]byte(tc.yaml), "bad.yaml")
			if err == nil {
				t.Fatal("expected a rejection")
			}
			if !strings.Contains(err.Error(), tc.wantSubstring) {
				t.Errorf("error %q does not mention %q", err, tc.wantSubstring)
			}
			if !strings.Contains(err.Error(), "bad.yaml") {
				t.Errorf("error %q does not name the file", err)
			}
		})
	}
}

func TestLoadDefinitions(t *testing.T) {
	t.Run("missing directory is not an error", func(t *testing.T) {
		defs, failures := LoadDefinitions(filepath.Join(t.TempDir(), "absent"))
		if failures != nil || defs != nil {
			t.Fatalf("a server with no definition directory must start with none: %v %v", defs, failures)
		}
	})

	t.Run("reads every extension in filename order", func(t *testing.T) {
		dir := t.TempDir()
		write := func(name, body string) {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		write("b.yaml", "id: b\nsteps:\n  - {id: s, action: plugin}")
		write("a.json", `{"id":"a","steps":[{"id":"s","action":"plugin"}]}`)
		write("c.yml", "id: c\nsteps:\n  - {id: s, action: plugin}")
		write("notes.txt", "ignored")
		if err := os.Mkdir(filepath.Join(dir, "nested.yaml"), 0o755); err != nil {
			t.Fatal(err)
		}

		defs, failures := LoadDefinitions(dir)
		if len(failures) != 0 {
			t.Fatalf("unexpected failures: %+v", failures)
		}
		var ids []string
		for _, d := range defs {
			ids = append(ids, d.ID)
		}
		if strings.Join(ids, ",") != "a,b,c" {
			t.Errorf("want a,b,c in filename order, got %v", ids)
		}
	})

	// One bad file must not hide the good ones, and each failure keeps its own
	// path: an operator fixing two files should be told about two files.
	t.Run("one bad file does not hide the others", func(t *testing.T) {
		dir := t.TempDir()
		write := func(name, body string) {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		write("a-good.yaml", "id: good\nsteps:\n  - {id: s, action: plugin}")
		write("b-broken.json", `{"id":`)
		write("c-cycle.yaml", "id: cyc\nsteps:\n  - {id: a, action: plugin, depends_on: [b]}\n  - {id: b, action: plugin, depends_on: [a]}")
		write("d-good.json", `{"id":"good2","steps":[{"id":"s","action":"plugin"}]}`)

		defs, failures := LoadDefinitions(dir)
		if len(defs) != 2 || defs[0].ID != "good" || defs[1].ID != "good2" {
			t.Errorf("the good definitions must still load, got %+v", defs)
		}
		if len(failures) != 2 {
			t.Fatalf("want 2 failures, got %+v", failures)
		}
		if !strings.Contains(failures[0].Path, "b-broken.json") || !strings.Contains(failures[1].Path, "c-cycle.yaml") {
			t.Errorf("each failure must name its own file: %+v", failures)
		}
		if !strings.Contains(failures[1].Reason, "dependency cycle") {
			t.Errorf("the cycle reason is lost: %q", failures[1].Reason)
		}
	})
}

func TestStepDefinitionTimeoutDurationEmpty(t *testing.T) {
	if d, err := (StepDefinition{ID: "a"}).TimeoutDuration(); d != 0 || err != nil {
		t.Errorf("an absent timeout means no deadline, got %v (%v)", d, err)
	}
}

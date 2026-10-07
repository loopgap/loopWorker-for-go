// Workflow definitions on disk.
//
// A Go function cannot be written in a JSON or YAML file, so a definition names
// the action each step runs and the host resolves it when it registers the
// workflow (see pkg/server/workflows.go, which resolves "plugin" and
// "skill:<name>" against the running server).
//
//	id: nightly-audit
//	name: Nightly audit
//	steps:
//	  - id: scan
//	    action: plugin
//	    input: "audit"
//	  - id: steady
//	    action: skill:research.anomaly
//	    input: "[1,2,3,4,5]"
//	  - id: review
//	    action: skill:research.anomaly
//	    from: steady
//	    depends_on: [scan, steady]
package workflow

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	yaml "go.yaml.in/yaml/v3"
)

// Definition is a workflow read from a JSON or YAML file.
type Definition struct {
	ID    string           `json:"id" yaml:"id"`
	Name  string           `json:"name" yaml:"name"`
	Steps []StepDefinition `json:"steps" yaml:"steps"`
	// Source is the file the definition came from, empty for one built in Go.
	// Errors raised after loading name it, so a broken file is identifiable.
	Source string `json:"-" yaml:"-"`
}

// StepDefinition is one step of a Definition.
type StepDefinition struct {
	ID   string `json:"id" yaml:"id"`
	Name string `json:"name" yaml:"name"`
	// Action is what the step runs: "plugin" for a task on an installed WASM
	// plugin, or "skill:<name>" for a registered skill.
	Action string `json:"action" yaml:"action"`
	// Input is the literal payload handed to the action.
	Input string `json:"input" yaml:"input"`
	// From names a step in DependsOn whose output becomes the payload, so data
	// really flows along the edge instead of the DAG being decorative.
	From string `json:"from" yaml:"from"`
	// DependsOn are the step ids that must complete first; these are the edges
	// Kahn's algorithm sorts on.
	DependsOn  []string `json:"depends_on" yaml:"depends_on"`
	Timeout    string   `json:"timeout" yaml:"timeout"`
	MaxRetries int      `json:"max_retries" yaml:"max_retries"`
}

// TimeoutDuration parses Timeout. An empty value means no per-step deadline.
func (s StepDefinition) TimeoutDuration() (time.Duration, error) {
	text := strings.TrimSpace(s.Timeout)
	if text == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(text)
	if err != nil {
		return 0, fmt.Errorf("step %q: timeout %q is not a duration (write 30s, 2m, 1h): %w", s.ID, s.Timeout, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("step %q: timeout must be positive, got %q", s.ID, s.Timeout)
	}
	return d, nil
}

// ParseDefinition decodes one definition file. path is used in errors so an
// operator is told which file to fix.
func ParseDefinition(data []byte, path string) (*Definition, error) {
	var def Definition
	if strings.EqualFold(filepath.Ext(path), ".json") {
		if err := json.Unmarshal(data, &def); err != nil {
			return nil, fmt.Errorf("%s is not valid JSON: %w", path, err)
		}
	} else if err := yaml.Unmarshal(data, &def); err != nil {
		return nil, fmt.Errorf("%s is not valid YAML: %w", path, err)
	}
	if err := def.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	def.Source = path
	return &def, nil
}

// LoadError is one definition file that could not be read or parsed.
type LoadError struct {
	Path   string
	Reason string
}

// LoadDefinitions reads every *.json, *.yaml and *.yml file in dir, in filename
// order so registration is deterministic. A missing directory is not an error:
// most installs have no definition files and rely on the built-in workflows.
//
// A bad file is reported and skipped rather than aborting the scan, so one
// typo does not hide the definitions that are fine, and every failure keeps its
// own path for the operator.
func LoadDefinitions(dir string) ([]Definition, []LoadError) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []LoadError{{Path: dir, Reason: fmt.Sprintf("read workflow directory: %v", err)}}
	}

	var paths []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".json", ".yaml", ".yml":
			paths = append(paths, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(paths)

	var defs []Definition
	var failures []LoadError
	for _, p := range paths {
		data, readErr := os.ReadFile(p)
		if readErr != nil {
			failures = append(failures, LoadError{Path: p, Reason: readErr.Error()})
			continue
		}
		def, parseErr := ParseDefinition(data, p)
		if parseErr != nil {
			failures = append(failures, LoadError{Path: p, Reason: parseErr.Error()})
			continue
		}
		defs = append(defs, *def)
	}
	return defs, failures
}

// Validate rejects a definition that could never run: a missing id, duplicate
// or unknown step ids, a dependency on a step that does not exist, a `from` that
// is not a declared dependency, or a dependency cycle. Kahn's algorithm on the
// step ids detects the cycle here, so a bad file fails at startup instead of at
// execute time.
func (d *Definition) Validate() error {
	if strings.TrimSpace(d.ID) == "" {
		return fmt.Errorf("workflow id is required (want: id: my-workflow)")
	}
	if len(d.Steps) == 0 {
		return fmt.Errorf("workflow %q declares no steps", d.ID)
	}

	known := make(map[string]bool, len(d.Steps))
	for _, s := range d.Steps {
		id := strings.TrimSpace(s.ID)
		if id == "" {
			return fmt.Errorf("workflow %q: every step needs an id", d.ID)
		}
		if known[id] {
			return fmt.Errorf("workflow %q: step id %q appears twice", d.ID, id)
		}
		if strings.TrimSpace(s.Action) == "" {
			return fmt.Errorf("workflow %q step %q: action is required (want: plugin, or skill:<name>)", d.ID, id)
		}
		known[id] = true
	}

	indegree := make(map[string]int, len(d.Steps))
	for _, s := range d.Steps {
		for _, dep := range s.DependsOn {
			if !known[dep] {
				return fmt.Errorf("workflow %q step %q depends on %q, which is not a step of this workflow (steps: %s)",
					d.ID, s.ID, dep, strings.Join(stepIDs(d.Steps), ", "))
			}
			indegree[s.ID]++
		}
		if from := strings.TrimSpace(s.From); from != "" && !contains(s.DependsOn, from) {
			return fmt.Errorf("workflow %q step %q reads from %q but does not list it in depends_on (%v)", d.ID, s.ID, from, s.DependsOn)
		}
		if _, err := s.TimeoutDuration(); err != nil {
			return fmt.Errorf("workflow %q: %w", d.ID, err)
		}
	}

	queue := make([]string, 0, len(d.Steps))
	for _, id := range stepIDs(d.Steps) {
		if indegree[id] == 0 {
			queue = append(queue, id)
		}
	}
	sorted := 0
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		sorted++
		for _, s := range d.Steps {
			if contains(s.DependsOn, current) {
				indegree[s.ID]--
				if indegree[s.ID] == 0 {
					queue = append(queue, s.ID)
				}
			}
		}
	}
	if sorted != len(d.Steps) {
		return fmt.Errorf("workflow %q has a dependency cycle: %d of %d steps are reachable; "+
			"depends_on must form a DAG", d.ID, sorted, len(d.Steps))
	}
	return nil
}

func stepIDs(steps []StepDefinition) []string {
	ids := make([]string, 0, len(steps))
	for _, s := range steps {
		ids = append(ids, s.ID)
	}
	return ids
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

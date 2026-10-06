package server

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"loopworker/internal/config"
	"loopworker/pkg/workflow"
)

// Workflow actions a definition file may name. "plugin" runs a task on an
// installed WASM plugin through the scheduler; "skill:<name>" runs a registered
// skill in-process. Both go through the running engine, so a workflow is a real
// orchestration of the same primitives the REST API exposes.
const (
	actionPlugin      = "plugin"
	actionSkillPrefix = "skill:"
)

// WorkflowFailure records one definition that could not be turned into a
// runnable workflow.
type WorkflowFailure struct {
	Source string `json:"source"`
	Reason string `json:"reason"`
}

// WorkflowRun is the outcome of workflow registration for this process. It is
// reported in the startup log and the health endpoint so "the API lists no
// workflows" is answerable without reading code.
type WorkflowRun struct {
	Dir      string            `json:"dir"`
	Builtin  []string          `json:"builtin"`
	Loaded   []string          `json:"loaded"`
	Overrode []string          `json:"overrode,omitempty"`
	Failed   []WorkflowFailure `json:"failed,omitempty"`
}

// Summary describes the registered workflows in one line for operators.
func (r *WorkflowRun) Summary() string {
	if r == nil {
		return "workflow state unavailable"
	}
	ids := append(append([]string{}, r.Builtin...), r.Loaded...)
	sort.Strings(ids)
	switch {
	case len(r.Failed) > 0:
		return fmt.Sprintf("%d workflow(s) registered (%s), %d definition(s) rejected - see the startup log for the reason",
			len(ids), strings.Join(ids, ", "), len(r.Failed))
	case len(ids) == 0:
		return "no workflow registered - put a JSON or YAML definition in " + r.Dir + " or run `loopworker doctor`"
	default:
		return fmt.Sprintf("%d workflow(s) registered: %s (definitions in %s)", len(ids), strings.Join(ids, ", "), r.Dir)
	}
}

// RegisterWorkflows builds the workflows this server serves and registers them
// with the engine.
//
// Two sources feed the list, and both are always applied:
//
//   - built-in definitions, so a fresh install with no configuration at all can
//     still execute a workflow over GET/POST /api/v1/workflow;
//   - definition files in workflow.definition_dir, which override a built-in of
//     the same id so an operator can retune a shipped workflow without forking.
//
// A rejected definition never stops the server: one bad file is an operator
// problem with one file, exactly like one bad plugin directory. The reason is
// returned and logged instead of dropped.
func RegisterWorkflows(cfg *config.Config, c *Components) (*WorkflowRun, error) {
	run := &WorkflowRun{Dir: workflowDefinitionDir(cfg)}

	builtins := BuiltinWorkflowDefinitions()
	for _, def := range builtins {
		wf, err := buildWorkflow(def, c)
		if err != nil {
			run.Failed = append(run.Failed, WorkflowFailure{Source: "built-in:" + def.ID, Reason: err.Error()})
			continue
		}
		c.WorkflowEngine.Register(wf)
		run.Builtin = append(run.Builtin, def.ID)
	}

	files, loadFailures := workflow.LoadDefinitions(run.Dir)
	for _, f := range loadFailures {
		run.Failed = append(run.Failed, WorkflowFailure{Source: f.Path, Reason: f.Reason})
	}
	for _, def := range files {
		wf, buildErr := buildWorkflow(def, c)
		if buildErr != nil {
			run.Failed = append(run.Failed, WorkflowFailure{Source: def.Source, Reason: buildErr.Error()})
			continue
		}
		c.WorkflowEngine.Register(wf)
		if wasBuiltin(builtins, def.ID) {
			run.Overrode = append(run.Overrode, def.ID)
		}
		run.Loaded = append(run.Loaded, def.ID)
	}

	if len(run.Failed) > 0 {
		var b strings.Builder
		for _, f := range run.Failed {
			fmt.Fprintf(&b, "\n  - %s: %s", f.Source, f.Reason)
		}
		return run, fmt.Errorf("%d workflow definition(s) rejected%s\n"+
			"  next steps: fix the file named above, or remove it; the built-in workflows are registered either way",
			len(run.Failed), b.String())
	}
	return run, nil
}

func wasBuiltin(builtins []workflow.Definition, id string) bool {
	for _, def := range builtins {
		if def.ID == id {
			return true
		}
	}
	return false
}

// workflowDefinitionDir is the directory workflow definition files are read
// from. It hangs off work_dir, which is already configurable through the config
// file, --work-dir and LOOPWORKER_WORK_DIR, so pointing a server at a different
// set of definitions needs no new configuration key.
func workflowDefinitionDir(cfg *config.Config) string {
	return filepath.Join(cfg.WorkDir, "workflows")
}

// buildWorkflow turns a validated definition into a runnable workflow, resolving
// each step's declared action against the components this server built.
func buildWorkflow(def workflow.Definition, c *Components) (*workflow.Workflow, error) {
	if err := def.Validate(); err != nil {
		return nil, err
	}
	name := def.Name
	if name == "" {
		name = def.ID
	}
	wf := workflow.NewWorkflow(def.ID, name)
	for _, sd := range def.Steps {
		action, err := resolveAction(def, sd, c)
		if err != nil {
			return nil, err
		}
		timeout, err := sd.TimeoutDuration()
		if err != nil {
			return nil, fmt.Errorf("workflow %q: %w", def.ID, err)
		}
		step := &workflow.Step{
			ID:        sd.ID,
			Name:      stepName(sd),
			Action:    action,
			DependsOn: sd.DependsOn,
			Timeout:   timeout,
		}
		if sd.MaxRetries > 0 {
			step.RetryPolicy = &workflow.RetryPolicy{MaxRetries: sd.MaxRetries, InitialWait: time.Second, MaxWait: 30 * time.Second, Multiplier: 2}
		}
		wf.AddStep(step)
	}
	return wf, nil
}

func stepName(sd workflow.StepDefinition) string {
	if strings.TrimSpace(sd.Name) != "" {
		return sd.Name
	}
	return sd.ID
}

// resolveAction maps a declared action onto a function the engine can run. An
// unknown action is a hard error: a workflow whose step silently did nothing is
// exactly the kind of ghost success this codebase keeps paying for.
func resolveAction(def workflow.Definition, sd workflow.StepDefinition, c *Components) (func(context.Context, map[string]interface{}) (map[string]interface{}, error), error) {
	action := strings.TrimSpace(sd.Action)
	switch {
	case action == actionPlugin:
		return pluginStep(c, sd), nil
	case strings.HasPrefix(action, actionSkillPrefix):
		name := strings.TrimSpace(strings.TrimPrefix(action, actionSkillPrefix))
		if name == "" {
			return nil, fmt.Errorf("workflow %q step %q: %q needs a skill name (want %sresearch.anomaly)",
				def.ID, sd.ID, action, actionSkillPrefix)
		}
		if c.SkillRegistry == nil || !c.SkillRegistry.Has(name) {
			return nil, fmt.Errorf("workflow %q step %q: skill %q is not registered on this server (registered: %s)",
				def.ID, sd.ID, name, registeredSkillNames(c))
		}
		return skillStep(c, sd, name), nil
	default:
		return nil, fmt.Errorf("workflow %q step %q: unknown action %q; use %q to run a task on an installed plugin, or %q<skill-name> (registered skills: %s)",
			def.ID, sd.ID, action, actionPlugin, actionSkillPrefix, registeredSkillNames(c))
	}
}

func registeredSkillNames(c *Components) string {
	if c.SkillRegistry == nil {
		return "none"
	}
	names := make([]string, 0, 2)
	for _, def := range c.SkillRegistry.List() {
		names = append(names, def.Name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

// pluginStep turns a workflow step into a task on the running executor and waits
// for its terminal state, so a workflow drives the same WASM sandbox the REST
// task endpoint does.
func pluginStep(c *Components, sd workflow.StepDefinition) func(context.Context, map[string]interface{}) (map[string]interface{}, error) {
	return func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
		dispatcher := c.WorkflowEngine.GetDispatcher()
		if dispatcher == nil {
			return nil, fmt.Errorf("workflow step %q runs a plugin task but no task dispatcher is wired into this engine", sd.ID)
		}
		// A created task is not a runnable task: the scheduler queues it
		// separately, exactly as the REST handler does. Without this the step
		// would sit in "pending" until the caller's context expired, and the
		// workflow would look like it was still working.
		queuer, ok := dispatcher.(workflow.TaskQueuer)
		if !ok {
			return nil, fmt.Errorf("workflow step %q cannot run a plugin task: this engine's task dispatcher (%T) can create tasks but does not implement workflow.TaskQueuer, so the task it just created stays in \"pending\" and nothing would ever execute it\n"+
				"  fix:   give the dispatcher a QueueTask(ctx, taskID) error method; internal/core/scheduler.SchedulerBridge needs one wrapping (*Scheduler).QueueTask",
				sd.ID, dispatcher)
		}
		payload, err := stepPayload(sd, state)
		if err != nil {
			return nil, err
		}
		ref, err := dispatcher.CreateTask(ctx, workflowTaskType, map[string]interface{}{
			"workflow_step": sd.ID,
		}, payload)
		if err != nil {
			return nil, fmt.Errorf("workflow step %q: schedule task: %w", sd.ID, err)
		}
		taskID := ref.ID
		if err := queuer.QueueTask(ctx, taskID); err != nil {
			return nil, fmt.Errorf("workflow step %q: queue task %s: %w", sd.ID, taskID, err)
		}
		// WaitForTask returns (nil, err) on a cancelled context, so the task id
		// is kept above rather than read back off ref.
		ref, err = dispatcher.WaitForTask(ctx, taskID)
		if err != nil {
			return nil, fmt.Errorf("workflow step %q: wait for task %s: %w", sd.ID, taskID, err)
		}
		if ref == nil || ref.State != "completed" {
			state, reason := "<no result>", ""
			if ref != nil {
				state, reason = ref.State, ref.Error
			}
			return nil, fmt.Errorf("workflow step %q: task %s ended %s: %s\n"+
				"  cause: the task ran but did not reach completed\n"+
				"  fix:   GET /api/v1/tasks/%s for the full task, and check that a plugin is loaded",
				sd.ID, taskID, state, reason, taskID)
		}
		return map[string]interface{}{sd.ID: string(ref.Result)}, nil
	}
}

// workflowTaskType is the task type every workflow step creates, so a task
// created by a workflow is distinguishable from a hand-posted one.
const workflowTaskType = "workflow-step"

// skillStep runs a registered skill in-process.
func skillStep(c *Components, sd workflow.StepDefinition, skillName string) func(context.Context, map[string]interface{}) (map[string]interface{}, error) {
	return func(ctx context.Context, state map[string]interface{}) (map[string]interface{}, error) {
		provider, ok := c.SkillRegistry.Get(skillName)
		if !ok {
			return nil, fmt.Errorf("workflow step %q: skill %q is not registered (registered: %s)",
				sd.ID, skillName, registeredSkillNames(c))
		}
		payload, err := stepPayload(sd, state)
		if err != nil {
			return nil, err
		}
		out, err := provider.Execute(ctx, payload, nil)
		if err != nil {
			return nil, fmt.Errorf("workflow step %q: skill %s: %w", sd.ID, skillName, err)
		}
		return map[string]interface{}{sd.ID: string(out)}, nil
	}
}

// stepPayload is the input handed to a step: the literal from the definition, or
// the output of the step named by `from`. Returning the upstream value is what
// makes depends_on carry data instead of only ordering.
func stepPayload(sd workflow.StepDefinition, state map[string]interface{}) ([]byte, error) {
	if from := strings.TrimSpace(sd.From); from != "" {
		v, ok := state[from]
		if !ok {
			return nil, fmt.Errorf("workflow step %q reads the output of step %q, which has not run; check depends_on", sd.ID, from)
		}
		switch t := v.(type) {
		case []byte:
			return t, nil
		case string:
			return []byte(t), nil
		default:
			return json.Marshal(v)
		}
	}
	if input := strings.TrimSpace(sd.Input); input != "" {
		return []byte(input), nil
	}
	return nil, nil
}

// Built-in workflow ids. They are part of the product surface: an operator reads
// them out of GET /api/v1/workflow/list, so renaming one is a breaking change.
const (
	// AnomalyReviewID is the DAG demonstration: two independent branches, a join
	// that depends on both, and a post-processing step that reads a branch's
	// output. It needs no plugin, no API key and no network, so a fresh install
	// can execute it.
	AnomalyReviewID = "builtin.anomaly-review"
	// PluginSmokeID proves the advertised "run untrusted code" path end to end:
	// a real task is scheduled onto the executor and into the WASM sandbox.
	PluginSmokeID = "builtin.plugin-smoke"
)

// BuiltinWorkflowDefinitions returns the workflows every LoopWorker server
// registers, whether or not any configuration or definition file exists.
//
// This is the default set, not scaffolding: without it a fresh install advertises
// "sequential, DAG, and parallel workflows" on the README while
// GET /api/v1/workflow/list returns [] and POST /api/v1/workflow/execute answers
// 404 WORKFLOW_NOT_FOUND. An operator overrides any of these by dropping a
// definition file with the same id into the workflow directory.
func BuiltinWorkflowDefinitions() []workflow.Definition {
	return []workflow.Definition{
		{
			ID:   AnomalyReviewID,
			Name: "Anomaly review (DAG: two branches, a join, no plugin or API key required)",
			// The steps are declared out of topological order on purpose: verify
			// depends on both branches, so the engine's Kahn sort has to move it
			// last. A definition already listed in order would prove nothing
			// about dependency handling.
			//
			// The join re-analyses its own literal series instead of consuming a
			// branch's output: research.anomaly turns a series into a report, so
			// its output is not a series and cannot be fed back into it. Data
			// travelling along an edge is demonstrated by builtin.plugin-smoke,
			// whose `from: prepare` hands the previous step's output to a plugin.
			Steps: []workflow.StepDefinition{
				{
					ID:        "verify",
					Name:      "Re-check both branches together",
					Action:    actionSkillPrefix + "research.anomaly",
					Input:     `{"values":[10,10.2,9.8,10.1,9.9,240],"threshold":1.5}`,
					DependsOn: []string{"steady", "spike"},
				},
				{
					ID:     "steady",
					Name:   "Steady series, no outlier",
					Action: actionSkillPrefix + "research.anomaly",
					Input:  `{"values":[10,10.2,9.8,10.1,9.9],"threshold":3}`,
				},
				{
					ID:     "spike",
					Name:   "Series with one injected spike",
					Action: actionSkillPrefix + "research.anomaly",
					Input:  `{"values":[10,10.1,9.9,10,240],"threshold":1.5}`,
				},
			},
		},
		{
			ID:   PluginSmokeID,
			Name: "Prepare a payload, then run one real task on the loaded WASM plugin",
			Steps: []workflow.StepDefinition{
				{
					ID:        "run",
					Name:      "Run the prepared payload on the plugin",
					Action:    actionPlugin,
					From:      "prepare",
					DependsOn: []string{"prepare"},
					Timeout:   "2m",
				},
				{
					ID:     "prepare",
					Name:   "Prepare the payload",
					Action: actionSkillPrefix + "research.anomaly",
					Input:  `[1,2,3]`,
				},
			},
		},
	}
}

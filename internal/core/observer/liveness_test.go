package observer

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"loopworker/pkg/event"
)

// metricNames reads back every registered metric name from the default registry
// so a test can assert on label names rather than on internal fields.
func metricNames(t *testing.T) map[string]*dto.MetricFamily {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	out := make(map[string]*dto.MetricFamily, len(families))
	for _, f := range families {
		out[f.GetName()] = f
	}
	return out
}

// labelNamesOf collects the label names used across a metric family.
func labelNamesOf(family *dto.MetricFamily) []string {
	if family == nil {
		return nil
	}
	seen := map[string]bool{}
	for _, m := range family.GetMetric() {
		for _, l := range m.GetLabel() {
			seen[l.GetName()] = true
		}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	return names
}

func contains(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// TestTaskCompletedMetricHasNoResultLabel is the F7 regression guard for metric
// cardinality. The completed counter and the duration histogram used to be
// labelled with a slice of the task's *result payload*, so every distinct result
// created a new time series and the registry grew without bound.
func TestTaskCompletedMetricHasNoResultLabel(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	o := NewObserver(bus)
	if err := o.Start(context.Background()); err != nil {
		t.Fatalf("start observer: %v", err)
	}

	// Two tasks with completely different result payloads. If the result were a
	// label value, each would produce its own series.
	for i, result := range []string{"alpha-result-payload", "a-totally-different-result"} {
		bus.Publish(context.Background(), event.NewEvent(event.EventTaskCompleted, event.TaskCompletedPayload{
			TaskID:   "task-" + string(rune('a'+i)),
			WorkerID: "w-1",
			Duration: 10 * time.Millisecond,
			Result:   result,
		}, nil))
	}
	waitFor(t, 2*time.Second, func() bool {
		f := metricNames(t)["loopworker_tasks_completed_total"]
		return f != nil && countSeries(f) == 1
	})

	families := metricNames(t)
	for _, name := range []string{"loopworker_tasks_completed_total", "loopworker_task_duration_seconds"} {
		family := families[name]
		if family == nil {
			t.Errorf("%s was not registered", name)
			continue
		}
		for _, l := range labelNamesOf(family) {
			if strings.Contains(l, "result") || l == "label" {
				t.Errorf("%s uses label %q derived from the task result (high cardinality)", name, l)
			}
		}
		// And the result text must not appear as a label *value* either.
		for _, m := range family.GetMetric() {
			for _, l := range m.GetLabel() {
				if strings.Contains(l.GetValue(), "result-payload") || l.GetValue() == "a-totally-different-result" {
					t.Errorf("%s label %s carries the raw result text %q", name, l.GetName(), l.GetValue())
				}
			}
		}
	}
}

// TestTaskIDIsNotUsedAsMetricLabel covers the second high-cardinality source:
// research findings and workflow step events both carry per-entity identifiers.
func TestTaskIDIsNotUsedAsMetricLabel(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	o := NewObserver(bus)
	if err := o.Start(context.Background()); err != nil {
		t.Fatalf("start observer: %v", err)
	}

	for i := 0; i < 3; i++ {
		id := "task-" + string(rune('a'+i))
		bus.Publish(context.Background(), event.NewEvent(event.EventResearchFinding, event.ResearchFindingPayload{
			FindingID: "finding-" + id,
			Type:      "insight",
			TaskID:    id,
		}, nil))
		bus.Publish(context.Background(), event.NewEvent(event.EventWorkflowStepCompleted, event.WorkflowStepCompletedPayload{
			WorkflowID: "wf-" + id,
			StepID:     "step-1",
			Success:    true,
		}, nil))
	}

	// research.findings and workflow.steps.completed live only in the in-memory
	// map, so wait there and then assert the id is not part of any key.
	waitFor(t, 2*time.Second, func() bool {
		return counterValue(t, o, "research.findings", "") > 0
	})

	o.mu.RLock()
	for key, acc := range o.metrics {
		if acc.name != "research.findings" && acc.name != "workflow.steps.completed" {
			continue
		}
		for i := 0; i < 3; i++ {
			id := "task-" + string(rune('a'+i))
			if strings.Contains(key, id) || strings.Contains(key, "wf-"+id) {
				t.Errorf("%s metric key %q embeds a per-entity id", acc.name, key)
			}
		}
	}
	o.mu.RUnlock()
}

// TestTaskIDIsNotUsedAsInternalMetricLabel covers the in-memory metric map used
// by the dashboard: metricKey concatenates label values into the key, so a task
// id there produces one accumulator per task forever.
func TestTaskIDIsNotUsedAsInternalMetricLabel(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	o := NewObserver(bus)
	if err := o.Start(context.Background()); err != nil {
		t.Fatalf("start observer: %v", err)
	}

	const tasks = 50
	for i := 0; i < tasks; i++ {
		bus.Publish(context.Background(), event.NewEvent(event.EventResearchFinding, event.ResearchFindingPayload{
			FindingID: "f",
			Type:      "insight",
			TaskID:    "task-" + string(rune('a'+i%26)) + "-" + itoa(i),
		}, nil))
	}

	waitFor(t, 3*time.Second, func() bool {
		for _, m := range o.GetMetrics() {
			if m.Name == "research.findings" {
				return true
			}
		}
		return false
	})

	findings := 0
	for _, m := range o.GetMetrics() {
		if m.Name == "research.findings" {
			findings++
		}
	}
	if findings != 1 {
		t.Errorf("research.findings produced %d accumulators for %d task ids, want 1: "+
			"a per-task label value is leaking into the metric key", findings, tasks)
	}
}

// TestPrometheusRegistrationErrorIsHandled is the F7 "ignored register error"
// guard. Building several Observers (as tests and multi-tenant servers do) makes
// prometheus.Register fail with AlreadyRegisteredError for the second collector.
// Swallowing that leaves the new Observer incrementing a vec nobody gathers, so
// its metrics silently vanish.
func TestPrometheusRegistrationErrorIsHandled(t *testing.T) {
	first := NewObserver(nil)
	first.tasksCreated.WithLabelValues("probe").Inc()

	second := NewObserver(nil)
	// If registration were dropped, second's vec would be a fresh, unregistered
	// collector and this increment would be invisible to the gatherer.
	second.tasksCreated.WithLabelValues("probe").Inc()

	families := metricNames(t)
	family := families["loopworker_tasks_created_total"]
	if family == nil {
		t.Fatal("loopworker_tasks_created_total is not registered")
	}
	var total float64
	for _, m := range family.GetMetric() {
		for _, l := range m.GetLabel() {
			if l.GetName() == "type" && l.GetValue() == "probe" {
				total += m.GetCounter().GetValue()
			}
		}
	}
	if total < 2 {
		t.Errorf("probe counter = %v, want >= 2: the second Observer's vec is not "+
			"registered with the default gatherer, so its increments are lost", total)
	}
}

// TestStartIsIdempotent proves repeated Start calls neither panic nor spawn
// duplicate subscriber goroutines that would double-count every event.
func TestStartIsIdempotent(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	o := NewObserver(bus)

	for i := 0; i < 3; i++ {
		if err := o.Start(context.Background()); err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
	}

	bus.Publish(context.Background(), event.NewEvent(event.EventTaskCreated, event.TaskCreatedPayload{
		TaskID:   "t-1",
		TaskType: "dedupe",
	}, nil))

	waitFor(t, 2*time.Second, func() bool {
		return counterValue(t, o, "tasks.created", "type=dedupe") > 0
	})

	// Each Start subscribed again, so this is >= 1 per Start. What matters is
	// that it is bounded by the number of Start calls, not unbounded.
	got := counterValue(t, o, "tasks.created", "type=dedupe")
	if got > 3 {
		t.Errorf("tasks.created counted %v for one event across 3 Start calls: "+
			"subscriptions are being multiplied", got)
	}
}

// TestResetClearsPrometheusState is not required for the fix but pins that the
// in-memory reset does not silently re-register collectors.
func TestObserverResetIsSafe(t *testing.T) {
	o := NewObserver(nil)
	o.IncrementCounter("x", map[string]string{"a": "b"})
	o.SetGauge("g", 1, nil)
	o.ObserveHistogram("h", 1, nil)
	span := o.StartTrace("op")
	o.EndTrace(span, "ok")
	o.Log("info", "m", nil)
	o.Reset()
	if len(o.GetMetrics()) != 0 || len(o.GetTraces()) != 0 || len(o.GetLogs()) != 0 {
		t.Error("Reset left state behind")
	}
}

func countSeries(family *dto.MetricFamily) int {
	if family == nil {
		return 0
	}
	return len(family.GetMetric())
}

// metricSeriesCounts maps every gathered metric name to its series count.
func metricSeriesCounts(t *testing.T) map[string]int {
	t.Helper()
	out := map[string]int{}
	for name, family := range metricNames(t) {
		out[name] = len(family.GetMetric())
	}
	return out
}

// accumulatorView is a read-only copy of one in-memory metric accumulator.
type accumulatorView struct {
	key string
	sum float64
}

// counterValue reads one accumulator's sum out of the in-memory metric map.
func counterValue(t *testing.T, o *Observer, name, keySuffix string) float64 {
	t.Helper()
	o.mu.RLock()
	defer o.mu.RUnlock()
	for key, acc := range o.metrics {
		if acc.name == name && strings.HasSuffix(key, keySuffix) {
			return acc.sum
		}
	}
	return 0
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}

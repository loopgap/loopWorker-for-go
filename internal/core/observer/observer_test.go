package observer

import (
	"context"
	"testing"
	"time"

	"loopworker/pkg/event"
)

func TestRecordMetric(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	o := NewObserver(bus)
	o.IncrementCounter("test.metric", nil)

	metrics := o.GetMetrics()
	if len(metrics) != 1 {
		t.Errorf("expected 1 metric, got %d", len(metrics))
	}
	if metrics[0].Name != "test.metric" {
		t.Errorf("expected name 'test.metric', got '%s'", metrics[0].Name)
	}
}

func TestRecordMetricWithLabels(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	o := NewObserver(bus)
	labels := map[string]string{"env": "test"}
	o.SetGauge("test.metric", 42.0, labels)

	metrics := o.GetMetrics()
	if len(metrics) != 1 {
		t.Errorf("expected 1 metric, got %d", len(metrics))
	}
}

func TestMetricFamilies(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	o := NewObserver(bus)
	o.IncrementCounter("requests", map[string]string{"method": "GET"})
	o.IncrementCounter("requests", map[string]string{"method": "POST"})

	families := o.GetMetricFamilies()
	if len(families) != 1 {
		t.Errorf("expected 1 metric family, got %d", len(families))
	}
}

func TestHistogram(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	o := NewObserver(bus)
	o.ObserveHistogram("latency", 100, nil)
	o.ObserveHistogram("latency", 200, nil)
	o.ObserveHistogram("latency", 150, nil)

	metrics := o.GetMetrics()
	if len(metrics) != 1 {
		t.Errorf("expected 1 metric, got %d", len(metrics))
	}
}

func TestStartTrace(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	o := NewObserver(bus)
	span := o.StartTrace("test-operation")

	if span.TraceID == "" {
		t.Error("trace ID should not be empty")
	}
	if span.Operation != "test-operation" {
		t.Errorf("expected operation 'test-operation', got '%s'", span.Operation)
	}
}

func TestEndTrace(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	o := NewObserver(bus)
	span := o.StartTrace("test-operation")
	time.Sleep(10 * time.Millisecond)
	o.EndTrace(span, "completed")

	traces := o.GetTraces()
	if len(traces) != 1 {
		t.Errorf("expected 1 trace, got %d", len(traces))
	}
	if traces[0].EndTime == nil {
		t.Error("end time should be set")
	}
	if traces[0].Status != "completed" {
		t.Errorf("expected status 'completed', got '%s'", traces[0].Status)
	}
	if traces[0].Duration == 0 {
		t.Error("duration should be set")
	}
}

func TestLog(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	o := NewObserver(bus)
	o.Log("info", "test message", map[string]interface{}{"key": "value"})

	logs := o.GetLogs()
	if len(logs) != 1 {
		t.Errorf("expected 1 log, got %d", len(logs))
	}
	if logs[0].Message != "test message" {
		t.Errorf("expected message 'test message', got '%s'", logs[0].Message)
	}
	if logs[0].Level != "info" {
		t.Errorf("expected level 'info', got '%s'", logs[0].Level)
	}
}

func TestGetHealth(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	o := NewObserver(bus)
	o.IncrementCounter("test", nil)
	o.Log("info", "test", nil)

	health := o.GetHealth()
	if health["metrics_count"] != 1 {
		t.Errorf("expected metrics_count 1, got %v", health["metrics_count"])
	}
	if health["logs_count"] != 1 {
		t.Errorf("expected logs_count 1, got %v", health["logs_count"])
	}
}

func TestObserverHandlesEvents(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	o := NewObserver(bus)
	_ = o.Start(context.Background())

	evt := event.NewEvent(event.EventTaskCreated, event.TaskCreatedPayload{TaskID: "t1"}, nil)
	_ = bus.Publish(context.Background(), evt)

	time.Sleep(50 * time.Millisecond)

	metrics := o.GetMetrics()
	if len(metrics) != 1 {
		t.Errorf("expected 1 metric from event, got %d", len(metrics))
	}
}

func TestReset(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	o := NewObserver(bus)
	o.IncrementCounter("test", nil)
	o.Log("info", "test", nil)

	o.Reset()

	metrics := o.GetMetrics()
	if len(metrics) != 0 {
		t.Errorf("expected 0 metrics after reset, got %d", len(metrics))
	}

	logs := o.GetLogs()
	if len(logs) != 0 {
		t.Errorf("expected 0 logs after reset, got %d", len(logs))
	}
}

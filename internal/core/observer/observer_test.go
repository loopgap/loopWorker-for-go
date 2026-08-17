package observer

import (
	"context"
	"fmt"
	"sync"
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

// 并发压力测试

func TestConcurrentIncrementCounter(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	o := NewObserver(bus)
	var wg sync.WaitGroup
	n := 1000

	// 并发递增计数器
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			o.IncrementCounter("concurrent.counter", nil)
		}()
	}
	wg.Wait()

	metrics := o.GetMetrics()
	if len(metrics) != 1 {
		t.Errorf("expected 1 metric, got %d", len(metrics))
	}

	// 验证计数器值
	for _, m := range metrics {
		if m.Name == "concurrent.counter" && m.Value != float64(n) {
			t.Errorf("expected counter value %d, got %f", n, m.Value)
		}
	}
}

func TestConcurrentSetGauge(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	o := NewObserver(bus)
	var wg sync.WaitGroup
	n := 100

	// 并发设置gauge
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			o.SetGauge("concurrent.gauge", float64(idx), nil)
		}(i)
	}
	wg.Wait()

	metrics := o.GetMetrics()
	if len(metrics) != 1 {
		t.Errorf("expected 1 metric, got %d", len(metrics))
	}
}

func TestConcurrentObserveHistogram(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	o := NewObserver(bus)
	var wg sync.WaitGroup
	n := 100

	// 并发观察histogram
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			o.ObserveHistogram("concurrent.histogram", float64(idx), nil)
		}(i)
	}
	wg.Wait()

	metrics := o.GetMetrics()
	if len(metrics) != 1 {
		t.Errorf("expected 1 metric, got %d", len(metrics))
	}
}

func TestConcurrentLog(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	o := NewObserver(bus)
	var wg sync.WaitGroup
	n := 100

	// 并发记录日志
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			o.Log("info", fmt.Sprintf("log-%d", idx), map[string]interface{}{"index": idx})
		}(i)
	}
	wg.Wait()

	logs := o.GetLogs()
	if len(logs) != n {
		t.Errorf("expected %d logs, got %d", n, len(logs))
	}
}

func TestConcurrentTracing(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	o := NewObserver(bus)
	var wg sync.WaitGroup
	n := 50

	// 并发创建和结束trace
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			span := o.StartTrace(fmt.Sprintf("operation-%d", idx))
			time.Sleep(time.Millisecond)
			o.EndTrace(span, "completed")
		}(i)
	}
	wg.Wait()

	traces := o.GetTraces()
	if len(traces) != n {
		t.Errorf("expected %d traces, got %d", n, len(traces))
	}
}

func TestConcurrentGetMetrics(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	o := NewObserver(bus)

	// 先添加一些指标
	for i := 0; i < 10; i++ {
		o.IncrementCounter(fmt.Sprintf("metric-%d", i), nil)
	}

	var wg sync.WaitGroup
	n := 100

	// 并发读取指标
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			metrics := o.GetMetrics()
			if len(metrics) != 10 {
				t.Errorf("expected 10 metrics, got %d", len(metrics))
			}
		}()
	}
	wg.Wait()
}

func TestConcurrentGetLogs(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	o := NewObserver(bus)

	// 先添加一些日志
	for i := 0; i < 10; i++ {
		o.Log("info", fmt.Sprintf("log-%d", i), nil)
	}

	var wg sync.WaitGroup
	n := 100

	// 并发读取日志
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			logs := o.GetLogs()
			if len(logs) != 10 {
				t.Errorf("expected 10 logs, got %d", len(logs))
			}
		}()
	}
	wg.Wait()
}

func TestConcurrentGetTraces(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	o := NewObserver(bus)

	// 先添加一些trace
	for i := 0; i < 10; i++ {
		span := o.StartTrace(fmt.Sprintf("operation-%d", i))
		o.EndTrace(span, "completed")
	}

	var wg sync.WaitGroup
	n := 100

	// 并发读取traces
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			traces := o.GetTraces()
			if len(traces) != 10 {
				t.Errorf("expected 10 traces, got %d", len(traces))
			}
		}()
	}
	wg.Wait()
}

func TestConcurrentMixedOperations(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	o := NewObserver(bus)
	ctx := context.Background()
	_ = o.Start(ctx)

	var wg sync.WaitGroup
	n := 100

	// 并发混合操作
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			// 混合各种操作
			o.IncrementCounter("mixed.counter", nil)
			o.SetGauge("mixed.gauge", float64(idx), nil)
			o.Log("info", fmt.Sprintf("log-%d", idx), nil)
			span := o.StartTrace(fmt.Sprintf("op-%d", idx))
			o.EndTrace(span, "completed")
		}(i)
	}
	wg.Wait()

	// 验证所有操作都完成了
	metrics := o.GetMetrics()
	logs := o.GetLogs()
	traces := o.GetTraces()

	if len(metrics) < 2 { // counter和gauge
		t.Errorf("expected at least 2 metrics, got %d", len(metrics))
	}
	if len(logs) != n {
		t.Errorf("expected %d logs, got %d", n, len(logs))
	}
	if len(traces) != n {
		t.Errorf("expected %d traces, got %d", n, len(traces))
	}
}

package research

import (
	"context"
	"testing"
)

func TestNewResearchEngine(t *testing.T) {
	engine := NewResearchEngine()
	if engine == nil {
		t.Fatal("research engine should not be nil")
	}
}

func TestAnomalyDetector(t *testing.T) {
	detector := NewAnomalyDetector(2.0)
	if detector.Name() != "anomaly" {
		t.Errorf("expected name 'anomaly', got '%s'", detector.Name())
	}

	data := []interface{}{1.0, 2.0, 3.0, 100.0, 4.0, 5.0}
	findings, err := detector.Detect(context.Background(), data)
	if err != nil {
		t.Fatalf("detect failed: %v", err)
	}

	if len(findings) == 0 {
		t.Error("expected findings for anomalous data")
	}
}

func TestTrendDetector(t *testing.T) {
	detector := NewTrendDetector(3)
	if detector.Name() != "trend" {
		t.Errorf("expected name 'trend', got '%s'", detector.Name())
	}

	data := []interface{}{1.0, 2.0, 3.0, 4.0, 5.0}
	findings, err := detector.Detect(context.Background(), data)
	if err != nil {
		t.Fatalf("detect failed: %v", err)
	}

	if len(findings) == 0 {
		t.Error("expected trend findings")
	}
}

func TestResearchEngineAnalyze(t *testing.T) {
	engine := NewResearchEngine()
	engine.RegisterDetector(NewAnomalyDetector(2.0))
	engine.RegisterDetector(NewTrendDetector(3))

	data := []interface{}{1.0, 2.0, 3.0, 100.0, 4.0, 5.0}
	findings, err := engine.Analyze(context.Background(), data)
	if err != nil {
		t.Fatalf("analyze failed: %v", err)
	}

	if len(findings) == 0 {
		t.Error("expected findings from analysis")
	}
}

func TestResearchEngineGetResults(t *testing.T) {
	engine := NewResearchEngine()
	engine.RegisterDetector(NewAnomalyDetector(2.0))

	data := []interface{}{1.0, 2.0, 3.0, 4.0, 5.0, 100.0}
	_, _ = engine.Analyze(context.Background(), data)

	results := engine.GetResults()
	if len(results) == 0 {
		t.Error("expected results to be stored")
	}
}

func TestCalculateMean(t *testing.T) {
	values := []float64{1.0, 2.0, 3.0, 4.0, 5.0}
	mean := calculateMean(values)
	if mean != 3.0 {
		t.Errorf("expected mean 3.0, got %f", mean)
	}
}

func TestCalculateSlope(t *testing.T) {
	values := []float64{1.0, 2.0, 3.0, 4.0, 5.0}
	slope := calculateSlope(values)
	if slope < 0.9 || slope > 1.1 {
		t.Errorf("expected slope ~1.0, got %f", slope)
	}
}

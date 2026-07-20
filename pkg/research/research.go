package research

import (
	"context"
	"fmt"
	"sync"
	"time"

	"loopworker/pkg/event"
)

type PatternType string

const (
	PatternAnomaly     PatternType = "anomaly"
	PatternTrend       PatternType = "trend"
	PatternCorrelation PatternType = "correlation"
)

type Finding struct {
	ID             string
	Type           PatternType
	Confidence     float64
	Description    string
	Evidence       []string
	Recommendation string
	Timestamp      time.Time
}

type Detector interface {
	Name() string
	Detect(ctx context.Context, data []interface{}) ([]Finding, error)
}

type AnomalyDetector struct {
	threshold float64
	baseline  []float64
}

func NewAnomalyDetector(threshold float64) *AnomalyDetector {
	return &AnomalyDetector{
		threshold: threshold,
		baseline:  make([]float64, 0),
	}
}

func (d *AnomalyDetector) Name() string { return "anomaly" }

func (d *AnomalyDetector) Detect(ctx context.Context, data []interface{}) ([]Finding, error) {
	var findings []Finding

	values := make([]float64, 0)
	for _, item := range data {
		if v, ok := item.(float64); ok {
			values = append(values, v)
		}
	}

	if len(values) == 0 {
		return findings, nil
	}

	mean := calculateMean(values)
	variance := calculateStdDev(values, mean)
	stddev := sqrt(variance)
	if stddev == 0 {
		stddev = 1
	}

	for i, v := range values {
		zScore := (v - mean) / stddev
		if zScore > d.threshold || zScore < -d.threshold {
			findings = append(findings, Finding{
				ID:             fmt.Sprintf("anomaly-%d", i),
				Type:           PatternAnomaly,
				Confidence:     0.8,
				Description:    fmt.Sprintf("Anomalous value detected: %.2f (z-score: %.2f)", v, zScore),
				Evidence:       []string{fmt.Sprintf("Value: %.2f, Mean: %.2f, StdDev: %.2f", v, mean, stddev)},
				Recommendation: "Investigate the source of this anomaly",
				Timestamp:      time.Now(),
			})
		}
	}

	return findings, nil
}

type TrendDetector struct {
	windowSize int
}

func NewTrendDetector(windowSize int) *TrendDetector {
	return &TrendDetector{windowSize: windowSize}
}

func (d *TrendDetector) Name() string { return "trend" }

func (d *TrendDetector) Detect(ctx context.Context, data []interface{}) ([]Finding, error) {
	var findings []Finding

	values := make([]float64, 0)
	for _, item := range data {
		if v, ok := item.(float64); ok {
			values = append(values, v)
		}
	}

	if len(values) < d.windowSize {
		return findings, nil
	}

	recent := values[len(values)-d.windowSize:]
	slope := calculateSlope(recent)

	if slope > 0.1 {
		findings = append(findings, Finding{
			ID:             "trend-increasing",
			Type:           PatternTrend,
			Confidence:     0.7,
			Description:    "Upward trend detected",
			Evidence:       []string{fmt.Sprintf("Slope: %.4f over %d data points", slope, d.windowSize)},
			Recommendation: "Monitor for continued increase",
			Timestamp:      time.Now(),
		})
	} else if slope < -0.1 {
		findings = append(findings, Finding{
			ID:             "trend-decreasing",
			Type:           PatternTrend,
			Confidence:     0.7,
			Description:    "Downward trend detected",
			Evidence:       []string{fmt.Sprintf("Slope: %.4f over %d data points", slope, d.windowSize)},
			Recommendation: "Investigate cause of decrease",
			Timestamp:      time.Now(),
		})
	}

	return findings, nil
}

type ResearchEngine struct {
	detectors []Detector
	results   map[string][]Finding
	eventBus  *event.EventBus
	mu        sync.RWMutex
}

func NewResearchEngine() *ResearchEngine {
	return &ResearchEngine{
		detectors: make([]Detector, 0),
		results:   make(map[string][]Finding),
	}
}

func (e *ResearchEngine) RegisterDetector(detector Detector) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.detectors = append(e.detectors, detector)
}

// SetEventBus sets the event bus for publishing research findings.
func (e *ResearchEngine) SetEventBus(bus *event.EventBus) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.eventBus = bus
}

func (e *ResearchEngine) Analyze(ctx context.Context, data []interface{}) ([]Finding, error) {
	e.mu.RLock()
	detectors := make([]Detector, len(e.detectors))
	copy(detectors, e.detectors)
	e.mu.RUnlock()

	var allFindings []Finding

	for _, detector := range detectors {
		findings, err := detector.Detect(ctx, data)
		if err != nil {
			return nil, fmt.Errorf("detector %s failed: %w", detector.Name(), err)
		}
		allFindings = append(allFindings, findings...)
	}

	e.mu.Lock()
	e.results["latest"] = allFindings
	e.mu.Unlock()

	// Publish findings to event bus
	if e.eventBus != nil {
		for _, finding := range allFindings {
			_ = e.eventBus.Publish(ctx, event.NewEvent(event.EventResearchFinding, event.ResearchFindingPayload{
				FindingID:  finding.ID,
				Type:       string(finding.Type),
				Confidence: finding.Confidence,
				DataPoints: len(finding.Evidence),
			}, nil))
		}
	}

	return allFindings, nil
}

func (e *ResearchEngine) GetResults() []Finding {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.results["latest"]
}

func calculateMean(values []float64) float64 {
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

func calculateStdDev(values []float64, mean float64) float64 {
	sum := 0.0
	for _, v := range values {
		sum += (v - mean) * (v - mean)
	}
	variance := sum / float64(len(values))
	return variance
}

func sqrt(x float64) float64 {
	if x <= 0 {
		return 0
	}
	z := x
	for i := 0; i < 10; i++ {
		z = (z + x/z) / 2
	}
	return z
}

func calculateSlope(values []float64) float64 {
	n := float64(len(values))
	if n < 2 {
		return 0
	}

	sumX := 0.0
	sumY := 0.0
	sumXY := 0.0
	sumX2 := 0.0

	for i, y := range values {
		x := float64(i)
		sumX += x
		sumY += y
		sumXY += x * y
		sumX2 += x * x
	}

	return (n*sumXY - sumX*sumY) / (n*sumX2 - sumX*sumX)
}

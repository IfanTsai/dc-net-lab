package traffic

import (
	"testing"

	"github.com/ifantsai/dcnetlab/internal/model"
)

func TestEvaluateAssertions(t *testing.T) {
	point := model.TrafficPoint{Rate: 10, SuccessRate: 99.5, P50Us: 100, P95Us: 200, P99Us: 300}

	if got := evaluateAssertions(nil, point); got != nil {
		t.Errorf("no assertions should yield nil, got %v", got)
	}

	assertions := []model.TrafficAssertion{
		{Metric: model.TrafficMetricSuccessRate, Comparator: model.TrafficComparatorGTE, Threshold: 99},
		{Metric: model.TrafficMetricP99, Comparator: model.TrafficComparatorLTE, Threshold: 250},
		{Metric: model.TrafficMetricRate, Comparator: model.TrafficComparatorGTE, Threshold: 10},
	}

	results := evaluateAssertions(assertions, point)
	if len(results) != 3 {
		t.Fatalf("results = %d, want 3", len(results))
	}

	if !results[0].Pass || results[0].Value != 99.5 {
		t.Errorf("successRate gte 99: %+v", results[0])
	}

	if results[1].Pass || results[1].Value != 300 {
		t.Errorf("p99 lte 250 should fail at 300: %+v", results[1])
	}

	if !results[2].Pass {
		t.Errorf("rate gte 10 at rate=10 should pass: %+v", results[2])
	}
}

func TestMetricValueUnknown(t *testing.T) {
	if v := metricValue("bogus", model.TrafficPoint{Rate: 5}); v != 0 {
		t.Errorf("unknown metric = %v, want 0", v)
	}
}

func TestComparePassUnknownComparator(t *testing.T) {
	if comparePass("eq", 5, 5) {
		t.Error("unknown comparator should never pass")
	}
}

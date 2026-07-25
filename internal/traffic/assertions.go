package traffic

import "github.com/ifantsai/dcnetlab/internal/model"

// evaluateAssertions checks every assertion against the latest point,
// returning nil when the scenario declares none.
func evaluateAssertions(assertions []model.TrafficAssertion, p model.TrafficPoint) []model.TrafficAssertionResult {
	if len(assertions) == 0 {
		return nil
	}

	results := make([]model.TrafficAssertionResult, 0, len(assertions))
	for _, a := range assertions {
		value := metricValue(a.Metric, p)
		results = append(results, model.TrafficAssertionResult{
			Metric:     a.Metric,
			Comparator: a.Comparator,
			Threshold:  a.Threshold,
			Value:      value,
			Pass:       comparePass(a.Comparator, value, a.Threshold),
		})
	}

	return results
}

// metricValue extracts one named metric from a point.
func metricValue(metric string, p model.TrafficPoint) float64 {
	switch metric {
	case model.TrafficMetricRate:
		return p.Rate
	case model.TrafficMetricSuccessRate:
		return p.SuccessRate
	case model.TrafficMetricP50:
		return float64(p.P50Us)
	case model.TrafficMetricP95:
		return float64(p.P95Us)
	case model.TrafficMetricP99:
		return float64(p.P99Us)
	default:
		return 0
	}
}

// comparePass applies a comparator (gte/lte) to value against
// threshold.
func comparePass(comparator string, value, threshold float64) bool {
	switch comparator {
	case model.TrafficComparatorGTE:
		return value >= threshold
	case model.TrafficComparatorLTE:
		return value <= threshold
	default:
		return false
	}
}

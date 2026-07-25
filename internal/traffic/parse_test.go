package traffic

import "testing"

func TestLatestStatsLine(t *testing.T) {
	t.Run("empty text", func(t *testing.T) {
		if _, ok := latestStatsLine(""); ok {
			t.Error("expected ok=false")
		}
	})

	t.Run("no stats lines", func(t *testing.T) {
		text := `{"time":"t","level":"INFO","msg":"listening","addr":":8080"}` + "\n"
		if _, ok := latestStatsLine(text); ok {
			t.Error("expected ok=false")
		}
	})

	t.Run("picks the last stats line", func(t *testing.T) {
		text := `{"msg":"stats","total":5,"failed":0,"rate":1,"lat_p50_us":100,"lat_p95_us":200,"lat_p99_us":300}
{"msg":"stats","total":10,"failed":1,"rate":1,"lat_p50_us":110,"lat_p95_us":210,"lat_p99_us":310}
`
		sl, ok := latestStatsLine(text)
		if !ok {
			t.Fatal("expected ok=true")
		}

		if sl.Total != 10 || sl.Failed != 1 || sl.P50Us != 110 || sl.P95Us != 210 || sl.P99Us != 310 {
			t.Errorf("sl = %+v", sl)
		}
	})

	t.Run("skips torn trailing line", func(t *testing.T) {
		text := `{"msg":"stats","total":5,"failed":0,"rate":1}
{"msg":"stats","total":10,"fail`
		sl, ok := latestStatsLine(text)
		if !ok || sl.Total != 5 {
			t.Errorf("sl = %+v ok=%v, want the complete earlier line", sl, ok)
		}
	})

	t.Run("ignores non-stats messages mixed in", func(t *testing.T) {
		text := `{"msg":"stats","total":5,"failed":0,"rate":1}
{"msg":"request failed","status":500}
`
		sl, ok := latestStatsLine(text)
		if !ok || sl.Total != 5 {
			t.Errorf("sl = %+v ok=%v, want the stats line despite trailing noise", sl, ok)
		}
	})
}

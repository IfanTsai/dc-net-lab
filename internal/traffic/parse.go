package traffic

import (
	"encoding/json"
	"strings"
)

// statsLine is one JSON line trafficgen's stats reporter emits via
// slog.JSONHandler; only the fields the collector needs are decoded.
type statsLine struct {
	Msg    string  `json:"msg"`
	Total  int64   `json:"total"`
	Failed int64   `json:"failed"`
	Rate   float64 `json:"rate"`
	P50Us  int64   `json:"lat_p50_us"`
	P95Us  int64   `json:"lat_p95_us"`
	P99Us  int64   `json:"lat_p99_us"`
}

// latestStatsLine scans tail text (as returned by the agent's
// TailLogs, oldest to newest) for the most recent trafficgen "stats"
// line.
func latestStatsLine(text string) (statsLine, bool) {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}

		var sl statsLine
		if err := json.Unmarshal([]byte(line), &sl); err != nil {
			continue
		}

		if sl.Msg == "stats" {
			return sl, true
		}
	}

	return statsLine{}, false
}

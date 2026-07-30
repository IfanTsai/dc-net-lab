package data

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ifantsai/dcnetlab/internal/model"
	"github.com/ifantsai/dcnetlab/internal/nodeagentapi"
)

// metricsBodyLimit caps a scrape response; the agent exporter emits a
// few KiB.
const metricsBodyLimit = 1 << 20

// Metrics scrapes the node-agent's Prometheus endpoint
// (http://addr:9100/metrics) and decodes the dcnetlab_node_* families
// into the shared model: cumulative counters and instantaneous
// gauges. Rates are the caller's job (collector and realtime view
// diff against their baselines).
func (a *programAgent) Metrics(ctx context.Context, addr string) (*model.NodeMetrics, error) {
	callCtx, cancel := context.WithTimeout(ctx, agentCallTimeout)
	defer cancel()

	url := fmt.Sprintf("http://%s:%d/metrics", addr, nodeagentapi.DefaultMetricsPort)
	req, err := http.NewRequestWithContext(callCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build scrape request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("scrape agent metrics: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("scrape agent metrics: status %s", resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, metricsBodyLimit))
	if err != nil {
		return nil, fmt.Errorf("read agent metrics: %w", err)
	}

	m := parseNodeMetrics(string(body))
	m.SampledAt = time.Now().UTC()

	return m, nil
}

// parseNodeMetrics decodes the agent exporter's text format. Unknown
// families are ignored so agent and controller can evolve
// independently; a malformed line is skipped rather than failing the
// scrape.
func parseNodeMetrics(body string) *model.NodeMetrics {
	m := &model.NodeMetrics{Filesystem: model.MetricsFilesystem{Mount: "/"}}
	ifaces := make(map[string]*model.MetricsInterface)
	var order []string

	iface := func(labels map[string]string) *model.MetricsInterface {
		name := labels["iface"]
		entry, ok := ifaces[name]
		if !ok {
			entry = &model.MetricsInterface{Name: name}
			ifaces[name] = entry
			order = append(order, name)
		}

		return entry
	}

	for _, line := range strings.Split(body, "\n") {
		name, labels, value, ok := parseSample(line)
		if !ok {
			continue
		}

		switch name {
		case "dcnetlab_node_cpu_seconds_total":
			switch labels["mode"] {
			case "user":
				m.CPU.UserSecondsTotal = value
			case "system":
				m.CPU.SystemSecondsTotal = value
			}
		case "dcnetlab_node_cpu_usage_seconds_total":
			m.CPU.UsageSecondsTotal = value
		case "dcnetlab_node_cpu_limit_cores":
			m.CPU.LimitCores = value
		case "dcnetlab_node_memory_used_bytes":
			m.Memory.UsedBytes = int64(value)
		case "dcnetlab_node_memory_cache_bytes":
			m.Memory.CacheBytes = int64(value)
		case "dcnetlab_node_memory_limit_bytes":
			m.Memory.LimitBytes = int64(value)
		case "dcnetlab_node_memory_swap_used_bytes":
			m.Memory.SwapUsedBytes = int64(value)
		case "dcnetlab_node_load1":
			m.Load.Load1 = value
		case "dcnetlab_node_load5":
			m.Load.Load5 = value
		case "dcnetlab_node_load15":
			m.Load.Load15 = value
		case "dcnetlab_node_filesystem_size_bytes":
			m.Filesystem.SizeBytes = int64(value)
		case "dcnetlab_node_filesystem_used_bytes":
			m.Filesystem.UsedBytes = int64(value)
		case "dcnetlab_node_filesystem_avail_bytes":
			m.Filesystem.AvailBytes = int64(value)
		case "dcnetlab_node_procs":
			m.Procs = int(value)
		case "dcnetlab_node_uptime_seconds":
			m.Uptime = time.Duration(value * float64(time.Second))
		case "dcnetlab_node_disk_read_bytes_total":
			m.Disk.ReadBytesTotal = int64(value)
		case "dcnetlab_node_disk_written_bytes_total":
			m.Disk.WriteBytesTotal = int64(value)
		case "dcnetlab_node_disk_reads_total":
			m.Disk.ReadOpsTotal = int64(value)
		case "dcnetlab_node_disk_writes_total":
			m.Disk.WriteOpsTotal = int64(value)
		case "dcnetlab_node_network_receive_bytes_total":
			iface(labels).RxBytesTotal = int64(value)
		case "dcnetlab_node_network_transmit_bytes_total":
			iface(labels).TxBytesTotal = int64(value)
		case "dcnetlab_node_network_receive_packets_total":
			iface(labels).RxPacketsTotal = int64(value)
		case "dcnetlab_node_network_transmit_packets_total":
			iface(labels).TxPacketsTotal = int64(value)
		case "dcnetlab_node_network_receive_errors_total":
			iface(labels).RxErrors = int64(value)
		case "dcnetlab_node_network_transmit_errors_total":
			iface(labels).TxErrors = int64(value)
		case "dcnetlab_node_network_receive_drop_total":
			iface(labels).RxDropped = int64(value)
		case "dcnetlab_node_network_transmit_drop_total":
			iface(labels).TxDropped = int64(value)
		}
	}

	for _, name := range order {
		m.Interfaces = append(m.Interfaces, *ifaces[name])
	}

	return m
}

// parseSample splits one text-format sample line into name, labels
// and value; comments, blanks and malformed lines report !ok.
func parseSample(line string) (name string, labels map[string]string, value float64, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", nil, 0, false
	}

	idx := strings.LastIndexByte(line, ' ')
	if idx < 0 {
		return "", nil, 0, false
	}

	value, err := strconv.ParseFloat(line[idx+1:], 64)
	if err != nil {
		return "", nil, 0, false
	}

	metric := line[:idx]
	name, labelPart, found := strings.Cut(metric, "{")
	if !found {
		return name, nil, value, true
	}

	labelPart, found = strings.CutSuffix(labelPart, "}")
	if !found {
		return "", nil, 0, false
	}

	labels = make(map[string]string)
	for _, pair := range strings.Split(labelPart, ",") {
		key, quoted, found := strings.Cut(pair, "=")
		if !found {
			continue
		}

		// Label values are %q-quoted by the exporter; Unquote undoes
		// the escaping.
		v, err := strconv.Unquote(quoted)
		if err != nil {
			continue
		}

		labels[key] = v
	}

	return name, labels, value, true
}

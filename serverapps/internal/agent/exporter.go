// exporter.go serves the agent's resource sample as a Prometheus
// text-format (version 0.0.4) endpoint: GET /metrics on the
// management network, node-exporter style under the dcnetlab_node_
// prefix. The controller's collector scrapes it and diffs counters
// into rates; an external Prometheus can scrape it directly.

package agent

import (
	"fmt"
	"net/http"
	"strings"
)

// MetricsHandler returns the /metrics handler over one collector.
func MetricsHandler(c *MetricsCollector) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte(encodeMetrics(c.Collect())))
	}
}

// nodeMetric describes one exported family drawn from the sample.
type nodeMetric struct {
	name    string
	kind    string // gauge or counter
	help    string
	value   func(Metrics) float64
	byIface func(InterfaceMetrics) float64 // set for per-interface series
}

// nodeMetrics is the export catalogue. CPU seconds are written by
// hand ahead of it (mode label).
var nodeMetrics = []nodeMetric{
	{name: "dcnetlab_node_cpu_usage_seconds_total", kind: "counter",
		help:  "Total CPU time consumed by the container since boot.",
		value: func(m Metrics) float64 { return m.CPU.UsageSecondsTotal }},
	{name: "dcnetlab_node_cpu_limit_cores", kind: "gauge",
		help:  "CPU limit of the container in cores (host cores when unlimited).",
		value: func(m Metrics) float64 { return m.CPU.LimitCores }},
	{name: "dcnetlab_node_memory_used_bytes", kind: "gauge",
		help:  "Memory in use, excluding the reclaimable page cache.",
		value: func(m Metrics) float64 { return float64(m.Memory.UsedBytes) }},
	{name: "dcnetlab_node_memory_cache_bytes", kind: "gauge",
		help:  "Page cache of the container.",
		value: func(m Metrics) float64 { return float64(m.Memory.CacheBytes) }},
	{name: "dcnetlab_node_memory_limit_bytes", kind: "gauge",
		help:  "Memory limit (host total when unlimited).",
		value: func(m Metrics) float64 { return float64(m.Memory.LimitBytes) }},
	{name: "dcnetlab_node_memory_swap_used_bytes", kind: "gauge",
		help:  "Swap in use.",
		value: func(m Metrics) float64 { return float64(m.Memory.SwapUsedBytes) }},
	{name: "dcnetlab_node_load1", kind: "gauge",
		help:  "Host 1m load average (shared kernel).",
		value: func(m Metrics) float64 { return m.Load.Load1 }},
	{name: "dcnetlab_node_load5", kind: "gauge",
		help:  "Host 5m load average.",
		value: func(m Metrics) float64 { return m.Load.Load5 }},
	{name: "dcnetlab_node_load15", kind: "gauge",
		help:  "Host 15m load average.",
		value: func(m Metrics) float64 { return m.Load.Load15 }},
	{name: "dcnetlab_node_filesystem_size_bytes", kind: "gauge",
		help:  "Size of the root filesystem.",
		value: func(m Metrics) float64 { return float64(m.Filesystem.SizeBytes) }},
	{name: "dcnetlab_node_filesystem_used_bytes", kind: "gauge",
		help:  "Used space on the root filesystem.",
		value: func(m Metrics) float64 { return float64(m.Filesystem.UsedBytes) }},
	{name: "dcnetlab_node_filesystem_avail_bytes", kind: "gauge",
		help:  "Available space on the root filesystem.",
		value: func(m Metrics) float64 { return float64(m.Filesystem.AvailBytes) }},
	{name: "dcnetlab_node_procs", kind: "gauge",
		help:  "Number of processes in the container.",
		value: func(m Metrics) float64 { return float64(m.Procs) }},
	{name: "dcnetlab_node_uptime_seconds", kind: "gauge",
		help:  "Container uptime (age of PID 1).",
		value: func(m Metrics) float64 { return m.Uptime.Seconds() }},
	{name: "dcnetlab_node_disk_read_bytes_total", kind: "counter",
		help:  "Bytes read since boot.",
		value: func(m Metrics) float64 { return float64(m.Disk.ReadBytesTotal) }},
	{name: "dcnetlab_node_disk_written_bytes_total", kind: "counter",
		help:  "Bytes written since boot.",
		value: func(m Metrics) float64 { return float64(m.Disk.WriteBytesTotal) }},
	{name: "dcnetlab_node_disk_reads_total", kind: "counter",
		help:  "Read operations since boot.",
		value: func(m Metrics) float64 { return float64(m.Disk.ReadOpsTotal) }},
	{name: "dcnetlab_node_disk_writes_total", kind: "counter",
		help:  "Write operations since boot.",
		value: func(m Metrics) float64 { return float64(m.Disk.WriteOpsTotal) }},
	{name: "dcnetlab_node_network_receive_bytes_total", kind: "counter",
		help:    "Bytes received per interface since boot.",
		byIface: func(i InterfaceMetrics) float64 { return float64(i.RxBytesTotal) }},
	{name: "dcnetlab_node_network_transmit_bytes_total", kind: "counter",
		help:    "Bytes transmitted per interface since boot.",
		byIface: func(i InterfaceMetrics) float64 { return float64(i.TxBytesTotal) }},
	{name: "dcnetlab_node_network_receive_packets_total", kind: "counter",
		help:    "Packets received per interface since boot.",
		byIface: func(i InterfaceMetrics) float64 { return float64(i.RxPacketsTotal) }},
	{name: "dcnetlab_node_network_transmit_packets_total", kind: "counter",
		help:    "Packets transmitted per interface since boot.",
		byIface: func(i InterfaceMetrics) float64 { return float64(i.TxPacketsTotal) }},
	{name: "dcnetlab_node_network_receive_errors_total", kind: "counter",
		help:    "Receive errors per interface since boot.",
		byIface: func(i InterfaceMetrics) float64 { return float64(i.RxErrors) }},
	{name: "dcnetlab_node_network_transmit_errors_total", kind: "counter",
		help:    "Transmit errors per interface since boot.",
		byIface: func(i InterfaceMetrics) float64 { return float64(i.TxErrors) }},
	{name: "dcnetlab_node_network_receive_drop_total", kind: "counter",
		help:    "Received packets dropped per interface since boot.",
		byIface: func(i InterfaceMetrics) float64 { return float64(i.RxDropped) }},
	{name: "dcnetlab_node_network_transmit_drop_total", kind: "counter",
		help:    "Transmitted packets dropped per interface since boot.",
		byIface: func(i InterfaceMetrics) float64 { return float64(i.TxDropped) }},
}

// encodeMetrics renders one sample as Prometheus text format.
func encodeMetrics(m Metrics) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# HELP dcnetlab_node_cpu_seconds_total CPU time spent since boot, split by mode.\n")
	fmt.Fprintf(&b, "# TYPE dcnetlab_node_cpu_seconds_total counter\n")
	fmt.Fprintf(&b, "dcnetlab_node_cpu_seconds_total{mode=\"user\"} %g\n", m.CPU.UserSecondsTotal)
	fmt.Fprintf(&b, "dcnetlab_node_cpu_seconds_total{mode=\"system\"} %g\n", m.CPU.SystemSecondsTotal)

	for _, f := range nodeMetrics {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", f.name, f.help, f.name, f.kind)
		if f.byIface == nil {
			fmt.Fprintf(&b, "%s %g\n", f.name, f.value(m))

			continue
		}

		// %q escapes quote, backslash and newline exactly as the
		// Prometheus text format requires.
		for _, iface := range m.Interfaces {
			fmt.Fprintf(&b, "%s{iface=%q} %g\n", f.name, iface.Name, f.byIface(iface))
		}
	}

	return b.String()
}

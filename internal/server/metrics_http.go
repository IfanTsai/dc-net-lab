package server

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ifantsai/dcnetlab/internal/metrics"
	"github.com/ifantsai/dcnetlab/internal/model"
)

// MetricsSource is the slice of the metrics history the Prometheus
// endpoint needs: the freshest point of every server.
type MetricsSource interface {
	Latest() []metrics.LabLatest
}

// metricsStaleAfter drops servers whose last sample is too old to
// export: a stopped lab must not keep reporting frozen values.
const metricsStaleAfter = time.Minute

// metricsHandler serves GET /metrics in the Prometheus text format
// (version 0.0.4), exposing the latest collector sweep. Counters are
// exported raw (node-exporter semantics); an external Prometheus
// computes its own rates.
func metricsHandler(src MetricsSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var fresh []metrics.LabLatest
		cutoff := time.Now().UTC().Add(-metricsStaleAfter)
		for _, l := range src.Latest() {
			if l.Point.Ts.After(cutoff) {
				fresh = append(fresh, l)
			}
		}

		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

		var b strings.Builder
		writeServerMetrics(&b, fresh)
		_, _ = w.Write([]byte(b.String()))
	}
}

// serverGauge and serverCounter describe one exported series drawn
// from every fresh server point.
type serverMetric struct {
	name    string
	kind    string // gauge or counter
	help    string
	value   func(model.MetricsPoint) float64
	byIface func(model.MetricsInterface) float64 // set for per-interface series
}

// serverMetrics is the export catalogue, node-exporter naming
// conventions under the dcnetlab_server_ prefix.
var serverMetrics = []serverMetric{
	{name: "dcnetlab_server_cpu_usage_percent", kind: "gauge",
		help:  "CPU usage in percent of the server's CPU limit, averaged over the collector interval.",
		value: func(p model.MetricsPoint) float64 { return p.CPU.UsagePercent }},
	{name: "dcnetlab_server_cpu_limit_cores", kind: "gauge",
		help:  "CPU limit of the server in cores.",
		value: func(p model.MetricsPoint) float64 { return p.CPU.LimitCores }},
	{name: "dcnetlab_server_memory_used_bytes", kind: "gauge",
		help:  "Memory in use, excluding the reclaimable page cache.",
		value: func(p model.MetricsPoint) float64 { return float64(p.Memory.UsedBytes) }},
	{name: "dcnetlab_server_memory_limit_bytes", kind: "gauge",
		help:  "Memory limit of the server.",
		value: func(p model.MetricsPoint) float64 { return float64(p.Memory.LimitBytes) }},
	{name: "dcnetlab_server_memory_cache_bytes", kind: "gauge",
		help:  "Page cache of the server.",
		value: func(p model.MetricsPoint) float64 { return float64(p.Memory.CacheBytes) }},
	{name: "dcnetlab_server_load1", kind: "gauge",
		help:  "Host 1m load average (shared kernel: identical on every server).",
		value: func(p model.MetricsPoint) float64 { return p.Load.Load1 }},
	{name: "dcnetlab_server_load5", kind: "gauge",
		help:  "Host 5m load average.",
		value: func(p model.MetricsPoint) float64 { return p.Load.Load5 }},
	{name: "dcnetlab_server_load15", kind: "gauge",
		help:  "Host 15m load average.",
		value: func(p model.MetricsPoint) float64 { return p.Load.Load15 }},
	{name: "dcnetlab_server_filesystem_size_bytes", kind: "gauge",
		help:  "Size of the server root filesystem.",
		value: func(p model.MetricsPoint) float64 { return float64(p.Filesystem.SizeBytes) }},
	{name: "dcnetlab_server_filesystem_avail_bytes", kind: "gauge",
		help:  "Available space on the server root filesystem.",
		value: func(p model.MetricsPoint) float64 { return float64(p.Filesystem.AvailBytes) }},
	{name: "dcnetlab_server_procs", kind: "gauge",
		help:  "Number of processes in the server.",
		value: func(p model.MetricsPoint) float64 { return float64(p.Procs) }},
	{name: "dcnetlab_server_disk_read_bytes_total", kind: "counter",
		help:  "Bytes read by the server since boot.",
		value: func(p model.MetricsPoint) float64 { return float64(p.Disk.ReadBytesTotal) }},
	{name: "dcnetlab_server_disk_written_bytes_total", kind: "counter",
		help:  "Bytes written by the server since boot.",
		value: func(p model.MetricsPoint) float64 { return float64(p.Disk.WriteBytesTotal) }},
	{name: "dcnetlab_server_disk_reads_total", kind: "counter",
		help:  "Read operations by the server since boot.",
		value: func(p model.MetricsPoint) float64 { return float64(p.Disk.ReadOpsTotal) }},
	{name: "dcnetlab_server_disk_writes_total", kind: "counter",
		help:  "Write operations by the server since boot.",
		value: func(p model.MetricsPoint) float64 { return float64(p.Disk.WriteOpsTotal) }},
	{name: "dcnetlab_server_network_receive_bytes_total", kind: "counter",
		help:    "Bytes received per interface since boot.",
		byIface: func(i model.MetricsInterface) float64 { return float64(i.RxBytesTotal) }},
	{name: "dcnetlab_server_network_transmit_bytes_total", kind: "counter",
		help:    "Bytes transmitted per interface since boot.",
		byIface: func(i model.MetricsInterface) float64 { return float64(i.TxBytesTotal) }},
	{name: "dcnetlab_server_network_receive_packets_total", kind: "counter",
		help:    "Packets received per interface since boot.",
		byIface: func(i model.MetricsInterface) float64 { return float64(i.RxPacketsTotal) }},
	{name: "dcnetlab_server_network_transmit_packets_total", kind: "counter",
		help:    "Packets transmitted per interface since boot.",
		byIface: func(i model.MetricsInterface) float64 { return float64(i.TxPacketsTotal) }},
	{name: "dcnetlab_server_network_receive_errors_total", kind: "counter",
		help:    "Receive errors per interface since boot.",
		byIface: func(i model.MetricsInterface) float64 { return float64(i.RxErrors) }},
	{name: "dcnetlab_server_network_transmit_errors_total", kind: "counter",
		help:    "Transmit errors per interface since boot.",
		byIface: func(i model.MetricsInterface) float64 { return float64(i.TxErrors) }},
	{name: "dcnetlab_server_network_receive_drop_total", kind: "counter",
		help:    "Received packets dropped per interface since boot.",
		byIface: func(i model.MetricsInterface) float64 { return float64(i.RxDropped) }},
	{name: "dcnetlab_server_network_transmit_drop_total", kind: "counter",
		help:    "Transmitted packets dropped per interface since boot.",
		byIface: func(i model.MetricsInterface) float64 { return float64(i.TxDropped) }},
}

func writeServerMetrics(b *strings.Builder, latest []metrics.LabLatest) {
	// CPU seconds get the node-exporter mode label treatment and are
	// written by hand ahead of the catalogue.
	fmt.Fprintf(b, "# HELP dcnetlab_server_cpu_seconds_total CPU time spent by the server since boot.\n")
	fmt.Fprintf(b, "# TYPE dcnetlab_server_cpu_seconds_total counter\n")
	for _, l := range latest {
		base := labelPair(l)
		fmt.Fprintf(b, "dcnetlab_server_cpu_seconds_total{%s,mode=\"user\"} %g\n", base, l.Point.CPU.UserSecondsTotal)
		fmt.Fprintf(b, "dcnetlab_server_cpu_seconds_total{%s,mode=\"system\"} %g\n", base, l.Point.CPU.SystemSecondsTotal)
	}

	for _, m := range serverMetrics {
		fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s %s\n", m.name, m.help, m.name, m.kind)
		for _, l := range latest {
			base := labelPair(l)
			if m.byIface == nil {
				fmt.Fprintf(b, "%s{%s} %g\n", m.name, base, m.value(l.Point))

				continue
			}

			for _, iface := range l.Point.Interfaces {
				fmt.Fprintf(b, "%s{%s,iface=%q} %g\n", m.name, base, iface.Name, m.byIface(iface))
			}
		}
	}
}

// labelPair renders the shared label set; %q escapes quote, backslash
// and newline exactly as the Prometheus text format requires.
func labelPair(l metrics.LabLatest) string {
	return fmt.Sprintf("lab=%q,server=%q", l.Lab, l.Server)
}

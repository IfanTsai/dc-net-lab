package model

import "time"

// NodeMetrics is one resource-usage sample of a server, live from its
// agent. It mirrors the node-exporter collectors that stay meaningful
// inside a shared-kernel container: CPU, memory, disk I/O and process
// count are scoped to the container's cgroup / PID namespace, network
// to its netns; the load average is the host kernel's and thus
// identical on every server.
type NodeMetrics struct {
	SampledAt  time.Time          `json:"sampledAt"`
	Uptime     time.Duration      `json:"uptime"`
	Procs      int                `json:"procs"`
	CPU        MetricsCPU         `json:"cpu"`
	Memory     MetricsMemory      `json:"memory"`
	Load       MetricsLoad        `json:"load"`
	Filesystem MetricsFilesystem  `json:"filesystem"`
	Disk       MetricsDisk        `json:"disk"`
	Interfaces []MetricsInterface `json:"interfaces"`
}

// MetricsCPU is CPU usage normalised against the server's CPU limit:
// UsagePercent is 0-100 of LimitCores. The *SecondsTotal fields are
// cumulative counters (node-exporter semantics) for rate computation
// by the metrics collector.
type MetricsCPU struct {
	UsagePercent  float64 `json:"usagePercent"`
	UserPercent   float64 `json:"userPercent"`
	SystemPercent float64 `json:"systemPercent"`
	LimitCores    float64 `json:"limitCores"`

	UsageSecondsTotal  float64 `json:"usageSecondsTotal,omitempty"`
	UserSecondsTotal   float64 `json:"userSecondsTotal,omitempty"`
	SystemSecondsTotal float64 `json:"systemSecondsTotal,omitempty"`
}

// MetricsMemory reports server memory; Used excludes the reclaimable
// page cache.
type MetricsMemory struct {
	UsedBytes     int64 `json:"usedBytes"`
	CacheBytes    int64 `json:"cacheBytes"`
	LimitBytes    int64 `json:"limitBytes"`
	SwapUsedBytes int64 `json:"swapUsedBytes"`
}

// MetricsLoad is the host load average.
type MetricsLoad struct {
	Load1  float64 `json:"load1"`
	Load5  float64 `json:"load5"`
	Load15 float64 `json:"load15"`
}

// MetricsFilesystem is the usage of the server root filesystem.
type MetricsFilesystem struct {
	Mount      string `json:"mount"`
	SizeBytes  int64  `json:"sizeBytes"`
	UsedBytes  int64  `json:"usedBytes"`
	AvailBytes int64  `json:"availBytes"`
}

// MetricsDisk is block I/O attributed to the server, aggregated over
// all devices.
type MetricsDisk struct {
	ReadBytesPerSec  float64 `json:"readBytesPerSec"`
	WriteBytesPerSec float64 `json:"writeBytesPerSec"`
	ReadOpsPerSec    float64 `json:"readOpsPerSec"`
	WriteOpsPerSec   float64 `json:"writeOpsPerSec"`
	ReadBytesTotal   int64   `json:"readBytesTotal"`
	WriteBytesTotal  int64   `json:"writeBytesTotal"`
	ReadOpsTotal     int64   `json:"readOpsTotal,omitempty"`
	WriteOpsTotal    int64   `json:"writeOpsTotal,omitempty"`
}

// MetricsInterface is the traffic of one interface of the server;
// error and drop counts are totals since boot.
type MetricsInterface struct {
	Name            string  `json:"name"`
	RxBytesPerSec   float64 `json:"rxBytesPerSec"`
	TxBytesPerSec   float64 `json:"txBytesPerSec"`
	RxPacketsPerSec float64 `json:"rxPacketsPerSec"`
	TxPacketsPerSec float64 `json:"txPacketsPerSec"`
	RxBytesTotal    int64   `json:"rxBytesTotal"`
	TxBytesTotal    int64   `json:"txBytesTotal"`
	RxPacketsTotal  int64   `json:"rxPacketsTotal,omitempty"`
	TxPacketsTotal  int64   `json:"txPacketsTotal,omitempty"`
	RxErrors        int64   `json:"rxErrors"`
	TxErrors        int64   `json:"txErrors"`
	RxDropped       int64   `json:"rxDropped"`
	TxDropped       int64   `json:"txDropped"`
}

// MetricsPoint is one collected sample of a server's resource-usage
// time series: the sweep time plus the NodeMetrics gauge groups.
// Rate values are averages over the collector's sweep interval.
type MetricsPoint struct {
	Ts         time.Time          `json:"ts"`
	Procs      int                `json:"procs"`
	CPU        MetricsCPU         `json:"cpu"`
	Memory     MetricsMemory      `json:"memory"`
	Load       MetricsLoad        `json:"load"`
	Filesystem MetricsFilesystem  `json:"filesystem"`
	Disk       MetricsDisk        `json:"disk"`
	Interfaces []MetricsInterface `json:"interfaces,omitempty"`
}

package data

import (
	"testing"
	"time"
)

// sampleExport mirrors the agent exporter's output shape.
const sampleExport = `# HELP dcnetlab_node_cpu_seconds_total CPU time spent since boot, split by mode.
# TYPE dcnetlab_node_cpu_seconds_total counter
dcnetlab_node_cpu_seconds_total{mode="user"} 2
dcnetlab_node_cpu_seconds_total{mode="system"} 1
# TYPE dcnetlab_node_cpu_usage_seconds_total counter
dcnetlab_node_cpu_usage_seconds_total 3.5
# TYPE dcnetlab_node_cpu_limit_cores gauge
dcnetlab_node_cpu_limit_cores 2
dcnetlab_node_memory_used_bytes 9.437184e+07
dcnetlab_node_memory_limit_bytes 209715200
dcnetlab_node_load1 0.5
dcnetlab_node_load15 0.3
dcnetlab_node_filesystem_size_bytes 1000
dcnetlab_node_filesystem_avail_bytes 400
dcnetlab_node_procs 2
dcnetlab_node_uptime_seconds 1000
dcnetlab_node_disk_read_bytes_total 1000
dcnetlab_node_disk_writes_total 20
dcnetlab_node_network_receive_bytes_total{iface="lo"} 10
dcnetlab_node_network_receive_bytes_total{iface="eth0"} 1000
dcnetlab_node_network_transmit_packets_total{iface="eth0"} 20
dcnetlab_node_network_transmit_drop_total{iface="eth0"} 2
dcnetlab_some_future_metric 42
not a metric line
`

func TestParseNodeMetrics(t *testing.T) {
	m := parseNodeMetrics(sampleExport)

	if m.CPU.UsageSecondsTotal != 3.5 || m.CPU.UserSecondsTotal != 2 ||
		m.CPU.SystemSecondsTotal != 1 || m.CPU.LimitCores != 2 {
		t.Errorf("cpu = %+v", m.CPU)
	}

	if m.Memory.UsedBytes != 94371840 || m.Memory.LimitBytes != 209715200 {
		t.Errorf("memory = %+v", m.Memory)
	}

	if m.Load.Load1 != 0.5 || m.Load.Load15 != 0.3 {
		t.Errorf("load = %+v", m.Load)
	}

	if m.Filesystem.Mount != "/" || m.Filesystem.SizeBytes != 1000 || m.Filesystem.AvailBytes != 400 {
		t.Errorf("filesystem = %+v", m.Filesystem)
	}

	if m.Procs != 2 || m.Uptime != 1000*time.Second {
		t.Errorf("procs = %d uptime = %v", m.Procs, m.Uptime)
	}

	if m.Disk.ReadBytesTotal != 1000 || m.Disk.WriteOpsTotal != 20 {
		t.Errorf("disk = %+v", m.Disk)
	}

	if len(m.Interfaces) != 2 || m.Interfaces[0].Name != "lo" {
		t.Fatalf("interfaces = %+v", m.Interfaces)
	}

	eth0 := m.Interfaces[1]
	if eth0.RxBytesTotal != 1000 || eth0.TxPacketsTotal != 20 || eth0.TxDropped != 2 {
		t.Errorf("eth0 = %+v", eth0)
	}
}

func TestParseSample(t *testing.T) {
	tests := []struct {
		name   string
		line   string
		want   string
		labels map[string]string
		value  float64
		ok     bool
	}{
		{name: "bare", line: "dcnetlab_node_procs 2", want: "dcnetlab_node_procs", value: 2, ok: true},
		{name: "labelled", line: `m{iface="eth0",mode="user"} 1.5`, want: "m",
			labels: map[string]string{"iface": "eth0", "mode": "user"}, value: 1.5, ok: true},
		{name: "escaped label", line: `m{iface="a\"b"} 1`, want: "m",
			labels: map[string]string{"iface": `a"b`}, value: 1, ok: true},
		{name: "comment", line: "# TYPE m counter", ok: false},
		{name: "blank", line: "   ", ok: false},
		{name: "no value", line: "dcnetlab_node_procs", ok: false},
		{name: "bad value", line: "m eleven", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, labels, value, ok := parseSample(tt.line)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}

			if !ok {
				return
			}

			if name != tt.want || value != tt.value {
				t.Errorf("got %s=%v, want %s=%v", name, value, tt.want, tt.value)
			}

			for k, v := range tt.labels {
				if labels[k] != v {
					t.Errorf("label %s = %q, want %q", k, labels[k], v)
				}
			}
		})
	}
}

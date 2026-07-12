package agent

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseCPUStat(t *testing.T) {
	tests := []struct {
		name string
		data string
		want cpuCounters
		ok   bool
	}{
		{
			name: "regular",
			data: "usage_usec 2500000\nuser_usec 1500000\nsystem_usec 1000000\nnr_periods 0\n",
			want: cpuCounters{usage: 2.5, user: 1.5, system: 1.0, ok: true},
			ok:   true,
		},
		{
			name: "missing usage",
			data: "user_usec 1500000\n",
			ok:   false,
		},
		{
			name: "garbage",
			data: "not a cpu stat",
			ok:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseCPUStat(tt.data)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}

			if ok && got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseProcStat(t *testing.T) {
	// 100 jiffies = 1s per column: user nice system idle iowait irq softirq steal.
	data := "cpu  100 200 300 4000 500 10 20 30\ncpu0 50 100 150 2000 250 5 10 15\n"

	got := parseProcStat(data)
	if !got.ok {
		t.Fatal("expected ok")
	}

	if !almostEqual(got.usage, 6.6) || !almostEqual(got.user, 3.0) || !almostEqual(got.system, 3.3) {
		t.Errorf("got usage=%v user=%v system=%v", got.usage, got.user, got.system)
	}
}

func TestParseCPUMax(t *testing.T) {
	tests := []struct {
		name string
		data string
		want float64
	}{
		{name: "half core", data: "50000 100000\n", want: 0.5},
		{name: "two cores", data: "200000 100000\n", want: 2},
		{name: "unlimited", data: "max 100000\n", want: 8},
		{name: "garbage", data: "what\n", want: 8},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseCPUMax(tt.data, 8); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseIOStat(t *testing.T) {
	data := "254:0 rbytes=1000 wbytes=2000 rios=10 wios=20 dbytes=0 dios=0\n" +
		"254:1 rbytes=500 wbytes=1000 rios=5 wios=10 dbytes=0 dios=0\n"

	got := parseIOStat(data)
	want := DiskMetrics{ReadBytesTotal: 1500, WriteBytesTotal: 3000, ReadOpsTotal: 15, WriteOpsTotal: 30}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestParseNetDev(t *testing.T) {
	data := `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo:    1000      10    0    0    0     0          0         0     1000      10    0    0    0     0       0          0
  eth0:  500000    4000    1    2    0     0          0         0   250000    2000    3    4    0     0       0          0
`

	got := parseNetDev(data)
	if len(got) != 2 {
		t.Fatalf("got %d interfaces, want 2", len(got))
	}

	eth0 := got[1]
	want := netCounters{
		name: "eth0", rxBytes: 500000, rxPackets: 4000, rxErrors: 1, rxDropped: 2,
		txBytes: 250000, txPackets: 2000, txErrors: 3, txDropped: 4,
	}
	if eth0 != want {
		t.Errorf("got %+v, want %+v", eth0, want)
	}
}

func TestParseMemoryStat(t *testing.T) {
	data := "anon 1000\nfile 5000\ninactive_file 3000\nactive_file 2000\n"

	cache, inactive := parseMemoryStat(data)
	if cache != 5000 || inactive != 3000 {
		t.Errorf("got cache=%d inactive=%d, want 5000/3000", cache, inactive)
	}
}

// writeMetricsFixture lays out a fake /proc + cgroup tree.
func writeMetricsFixture(t *testing.T, dir string) (procRoot, cgroupRoot string) {
	t.Helper()

	procRoot = filepath.Join(dir, "proc")
	cgroupRoot = filepath.Join(dir, "cgroup")

	writeFile(t, filepath.Join(procRoot, "loadavg"), "0.50 0.40 0.30 2/345 9999\n")
	writeFile(t, filepath.Join(procRoot, "uptime"), "5000.00 39000.00\n")
	writeFile(t, filepath.Join(procRoot, "1/stat"),
		"1 (node (agent)) S 0 1 1 0 -1 4194560 100 0 0 0 1 2 0 0 20 0 1 0 400000 1000000 100 18446744073709551615 0 0 0 0 0 0 0 0 0 0 0 0 17 0 0 0 0 0 0\n")
	writeFile(t, filepath.Join(procRoot, "42/stat"), "unused")
	writeFile(t, filepath.Join(procRoot, "net/dev"),
		"h1\nh2\n eth0: 1000 10 1 0 0 0 0 0 2000 20 0 2 0 0 0 0\n")

	writeFile(t, filepath.Join(cgroupRoot, "cpu.stat"), "usage_usec 3000000\nuser_usec 2000000\nsystem_usec 1000000\n")
	writeFile(t, filepath.Join(cgroupRoot, "cpu.max"), "200000 100000\n")
	writeFile(t, filepath.Join(cgroupRoot, "io.stat"), "254:0 rbytes=1000 wbytes=2000 rios=10 wios=20\n")
	writeFile(t, filepath.Join(cgroupRoot, "memory.current"), "104857600\n")
	writeFile(t, filepath.Join(cgroupRoot, "memory.max"), "209715200\n")
	writeFile(t, filepath.Join(cgroupRoot, "memory.stat"), "file 20971520\ninactive_file 10485760\n")
	writeFile(t, filepath.Join(cgroupRoot, "memory.swap.current"), "0\n")

	return procRoot, cgroupRoot
}

func TestCollect(t *testing.T) {
	dir := t.TempDir()
	procRoot, cgroupRoot := writeMetricsFixture(t, dir)

	c := &MetricsCollector{procRoot: procRoot, cgroupRoot: cgroupRoot, rootfs: dir}
	m := c.Collect()

	if m.Procs != 2 {
		t.Errorf("procs = %d, want 2", m.Procs)
	}

	// Container uptime: 5000s host uptime - 400000 ticks / 100 = 1000s.
	if m.Uptime != 1000*time.Second {
		t.Errorf("uptime = %v, want 1000s", m.Uptime)
	}

	if !almostEqual(m.Load.Load1, 0.5) || !almostEqual(m.Load.Load15, 0.3) {
		t.Errorf("load = %+v", m.Load)
	}

	if m.CPU.LimitCores != 2 || !almostEqual(m.CPU.UsageSecondsTotal, 3) ||
		!almostEqual(m.CPU.UserSecondsTotal, 2) || !almostEqual(m.CPU.SystemSecondsTotal, 1) {
		t.Errorf("cpu = %+v", m.CPU)
	}

	// Working set: 100 MB current - 10 MB inactive file.
	if m.Memory.UsedBytes != 94371840 || m.Memory.LimitBytes != 209715200 || m.Memory.CacheBytes != 20971520 {
		t.Errorf("memory = %+v", m.Memory)
	}

	if m.Disk != (DiskMetrics{ReadBytesTotal: 1000, WriteBytesTotal: 2000, ReadOpsTotal: 10, WriteOpsTotal: 20}) {
		t.Errorf("disk = %+v", m.Disk)
	}

	if len(m.Interfaces) != 1 {
		t.Fatalf("interfaces = %+v", m.Interfaces)
	}

	eth0 := m.Interfaces[0]
	if eth0.Name != "eth0" || eth0.RxBytesTotal != 1000 || eth0.RxPacketsTotal != 10 ||
		eth0.TxPacketsTotal != 20 || eth0.RxErrors != 1 || eth0.TxDropped != 2 {
		t.Errorf("eth0 = %+v", eth0)
	}

	if m.Filesystem.SizeBytes <= 0 || m.Filesystem.Mount != dir {
		t.Errorf("filesystem = %+v", m.Filesystem)
	}

	if m.SampledAt.IsZero() {
		t.Error("sampled at is zero")
	}
}

// TestCollectFallbacks removes every cgroup file: CPU falls back to
// /proc/stat, memory to /proc/meminfo, disk to zeros.
func TestCollectFallbacks(t *testing.T) {
	dir := t.TempDir()
	procRoot := filepath.Join(dir, "proc")

	writeFile(t, filepath.Join(procRoot, "loadavg"), "0.10 0.20 0.30 1/100 500\n")
	writeFile(t, filepath.Join(procRoot, "uptime"), "1000.00 8000.00\n")
	writeFile(t, filepath.Join(procRoot, "1/stat"),
		"1 (init) S 0 1 1 0 -1 4194560 100 0 0 0 1 2 0 0 20 0 1 0 50000 1000000 100 0 0 0 0 0 0 0 0 0 0 0 0 0 17 0 0 0 0 0 0\n")
	writeFile(t, filepath.Join(procRoot, "stat"), "cpu  100 0 100 1000 0 0 0 0\n")
	writeFile(t, filepath.Join(procRoot, "meminfo"),
		"MemTotal: 1000 kB\nMemAvailable: 600 kB\nCached: 300 kB\nSwapTotal: 100 kB\nSwapFree: 90 kB\n")
	writeFile(t, filepath.Join(procRoot, "net/dev"), "h1\nh2\n")

	c := &MetricsCollector{procRoot: procRoot, cgroupRoot: filepath.Join(dir, "no-cgroup"), rootfs: dir}
	m := c.Collect()

	if !almostEqual(m.CPU.UsageSecondsTotal, 2) || !almostEqual(m.CPU.UserSecondsTotal, 1) {
		t.Errorf("cpu = %+v, want host-wide jiffies", m.CPU)
	}

	if m.Memory.UsedBytes != 400<<10 || m.Memory.LimitBytes != 1000<<10 ||
		m.Memory.CacheBytes != 300<<10 || m.Memory.SwapUsedBytes != 10<<10 {
		t.Errorf("memory = %+v", m.Memory)
	}

	if m.Disk != (DiskMetrics{}) {
		t.Errorf("disk = %+v, want zeros", m.Disk)
	}
}

func TestEncodeMetrics(t *testing.T) {
	dir := t.TempDir()
	procRoot, cgroupRoot := writeMetricsFixture(t, dir)

	c := &MetricsCollector{procRoot: procRoot, cgroupRoot: cgroupRoot, rootfs: dir}
	body := encodeMetrics(c.Collect())

	for _, want := range []string{
		"# TYPE dcnetlab_node_cpu_seconds_total counter",
		`dcnetlab_node_cpu_seconds_total{mode="user"} 2`,
		`dcnetlab_node_cpu_seconds_total{mode="system"} 1`,
		"dcnetlab_node_cpu_usage_seconds_total 3",
		"dcnetlab_node_cpu_limit_cores 2",
		"# TYPE dcnetlab_node_memory_used_bytes gauge",
		"dcnetlab_node_memory_used_bytes 9.437184e+07",
		"dcnetlab_node_load1 0.5",
		"dcnetlab_node_procs 2",
		"dcnetlab_node_uptime_seconds 1000",
		"dcnetlab_node_disk_read_bytes_total 1000",
		`dcnetlab_node_network_receive_bytes_total{iface="eth0"} 1000`,
		`dcnetlab_node_network_transmit_drop_total{iface="eth0"} 2`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
}

func TestPID1StartTicks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stat")

	writeFile(t, path,
		"1 (a b) c) S 0 1 1 0 -1 4194560 100 0 0 0 1 2 0 0 20 0 1 0 12345 1000000 100 0 0 0 0 0 0 0 0 0 0 0 0 0 17 0 0 0 0 0 0\n")

	ticks, err := pid1StartTicks(path)
	if err != nil {
		t.Fatal(err)
	}

	if ticks != 12345 {
		t.Errorf("ticks = %d, want 12345", ticks)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func almostEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

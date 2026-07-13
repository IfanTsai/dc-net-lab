// metrics.go samples the server's resource usage, node-exporter
// style, adapted to a shared-kernel container: CPU, memory, disk I/O
// and process count come from the container's own cgroup v2 subtree
// and PID namespace, network from its own netns, load average from
// the host kernel (shared by design). Following the Prometheus data
// model the sample carries cumulative counters and instantaneous
// gauges only — rates are the scraper's job (the controller's
// collector diffs consecutive scrapes; an external Prometheus runs
// rate()).

package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// userHZ is the kernel's USER_HZ: the unit of the jiffies counters in
// /proc/stat and /proc/<pid>/stat, fixed at 100 on Linux.
const userHZ = 100

// Metrics is one resource-usage sample of the server.
type Metrics struct {
	SampledAt  time.Time
	Uptime     time.Duration
	Procs      int
	CPU        CPUMetrics
	Memory     MemoryMetrics
	Load       LoadMetrics
	Filesystem FilesystemMetrics
	Disk       DiskMetrics
	Interfaces []InterfaceMetrics
}

// CPUMetrics is cumulative CPU time in seconds since boot plus the
// container's CPU limit.
type CPUMetrics struct {
	LimitCores         float64
	UsageSecondsTotal  float64
	UserSecondsTotal   float64
	SystemSecondsTotal float64
}

// MemoryMetrics reports container memory; Used excludes the
// reclaimable page cache (node-exporter's MemAvailable convention).
type MemoryMetrics struct {
	UsedBytes     int64
	CacheBytes    int64
	LimitBytes    int64
	SwapUsedBytes int64
}

// LoadMetrics is the host load average.
type LoadMetrics struct {
	Load1  float64
	Load5  float64
	Load15 float64
}

// FilesystemMetrics is the usage of the container root filesystem.
type FilesystemMetrics struct {
	Mount      string
	SizeBytes  int64
	UsedBytes  int64
	AvailBytes int64
}

// DiskMetrics is cumulative block I/O attributed to the container by
// the io cgroup, aggregated over all devices.
type DiskMetrics struct {
	ReadBytesTotal  int64
	WriteBytesTotal int64
	ReadOpsTotal    int64
	WriteOpsTotal   int64
}

// InterfaceMetrics is the cumulative traffic of one interface of the
// container's network namespace.
type InterfaceMetrics struct {
	Name           string
	RxBytesTotal   int64
	TxBytesTotal   int64
	RxPacketsTotal int64
	TxPacketsTotal int64
	RxErrors       int64
	TxErrors       int64
	RxDropped      int64
	TxDropped      int64
}

// MetricsCollector samples resource usage from /proc, the cgroup v2
// subtree and the root filesystem. The roots are injectable for
// tests.
type MetricsCollector struct {
	procRoot   string
	cgroupRoot string
	rootfs     string
}

// NewMetricsCollector returns a collector reading the real system
// trees.
func NewMetricsCollector() *MetricsCollector {
	return &MetricsCollector{
		procRoot:   "/proc",
		cgroupRoot: "/sys/fs/cgroup",
		rootfs:     "/",
	}
}

// Collect assembles one sample. Every collector degrades
// independently: a missing source (cgroup controller not delegated,
// file gone) zeroes its section instead of failing the sample.
func (c *MetricsCollector) Collect() Metrics {
	m := Metrics{
		SampledAt:  time.Now(),
		Uptime:     c.uptime(),
		Procs:      c.procs(),
		Memory:     c.memory(),
		Load:       c.load(),
		Filesystem: c.filesystem(),
		Disk:       c.diskCounters(),
	}

	m.CPU.LimitCores = c.cpuLimit()
	if counters := c.cpuCounters(); counters.ok {
		m.CPU.UsageSecondsTotal = counters.usage
		m.CPU.UserSecondsTotal = counters.user
		m.CPU.SystemSecondsTotal = counters.system
	}

	for _, n := range c.netCounters() {
		m.Interfaces = append(m.Interfaces, InterfaceMetrics{
			Name:           n.name,
			RxBytesTotal:   n.rxBytes,
			TxBytesTotal:   n.txBytes,
			RxPacketsTotal: n.rxPackets,
			TxPacketsTotal: n.txPackets,
			RxErrors:       n.rxErrors,
			TxErrors:       n.txErrors,
			RxDropped:      n.rxDropped,
			TxDropped:      n.txDropped,
		})
	}

	return m
}

// cpuCounters is cumulative CPU time in seconds; ok marks a
// successful reading.
type cpuCounters struct {
	usage  float64
	user   float64
	system float64
	ok     bool
}

// netCounters is the cumulative traffic of one interface.
type netCounters struct {
	name      string
	rxBytes   int64
	txBytes   int64
	rxPackets int64
	txPackets int64
	rxErrors  int64
	txErrors  int64
	rxDropped int64
	txDropped int64
}

// cpuCounters reads the container's cgroup cpu.stat, falling back to
// the host-wide /proc/stat aggregate when the cpu controller is not
// delegated (cgroup v1 hosts).
func (c *MetricsCollector) cpuCounters() cpuCounters {
	if data, err := os.ReadFile(filepath.Join(c.cgroupRoot, "cpu.stat")); err == nil {
		if counters, ok := parseCPUStat(string(data)); ok {
			return counters
		}
	}

	data, err := os.ReadFile(filepath.Join(c.procRoot, "stat"))
	if err != nil {
		return cpuCounters{}
	}

	return parseProcStat(string(data))
}

// parseCPUStat reads cgroup v2 cpu.stat (usage_usec / user_usec /
// system_usec) into seconds.
func parseCPUStat(data string) (cpuCounters, bool) {
	var counters cpuCounters
	for _, line := range strings.Split(data, "\n") {
		key, value, found := strings.Cut(line, " ")
		if !found {
			continue
		}

		usec, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			continue
		}

		switch key {
		case "usage_usec":
			counters.usage = float64(usec) / 1e6
			counters.ok = true
		case "user_usec":
			counters.user = float64(usec) / 1e6
		case "system_usec":
			counters.system = float64(usec) / 1e6
		}
	}

	return counters, counters.ok
}

// parseProcStat reads the aggregate "cpu" line of /proc/stat: user,
// nice, system, idle, iowait, irq, softirq, steal in USER_HZ jiffies.
// Usage is everything but idle and iowait.
func parseProcStat(data string) cpuCounters {
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 8 || fields[0] != "cpu" {
			continue
		}

		jiffies := make([]float64, 8)
		for i := range jiffies {
			v, err := strconv.ParseFloat(fields[i+1], 64)
			if err != nil {
				return cpuCounters{}
			}

			jiffies[i] = v / userHZ
		}

		// jiffies[3] and jiffies[4] are idle and iowait: not busy time.
		user, nice, system := jiffies[0], jiffies[1], jiffies[2]
		irq, softirq, steal := jiffies[5], jiffies[6], jiffies[7]

		return cpuCounters{
			usage:  user + nice + system + irq + softirq + steal,
			user:   user + nice,
			system: system + irq + softirq,
			ok:     true,
		}
	}

	return cpuCounters{}
}

// cpuLimit is the cgroup CPU quota in cores; the host core count when
// unlimited or unreadable.
func (c *MetricsCollector) cpuLimit() float64 {
	hostCores := float64(runtime.NumCPU())

	data, err := os.ReadFile(filepath.Join(c.cgroupRoot, "cpu.max"))
	if err != nil {
		return hostCores
	}

	return parseCPUMax(string(data), hostCores)
}

// parseCPUMax reads cgroup v2 cpu.max: "<quota> <period>" in usec, or
// "max <period>" when unlimited.
func parseCPUMax(data string, hostCores float64) float64 {
	fields := strings.Fields(data)
	if len(fields) != 2 || fields[0] == "max" {
		return hostCores
	}

	quota, errQ := strconv.ParseFloat(fields[0], 64)
	period, errP := strconv.ParseFloat(fields[1], 64)
	if errQ != nil || errP != nil || quota <= 0 || period <= 0 {
		return hostCores
	}

	return quota / period
}

// diskCounters sums the container's cgroup io.stat over all devices;
// zeros when the io controller is not delegated.
func (c *MetricsCollector) diskCounters() DiskMetrics {
	data, err := os.ReadFile(filepath.Join(c.cgroupRoot, "io.stat"))
	if err != nil {
		return DiskMetrics{}
	}

	return parseIOStat(string(data))
}

// parseIOStat reads cgroup v2 io.stat lines:
// "254:0 rbytes=x wbytes=y rios=z wios=w ...", summed over devices.
func parseIOStat(data string) DiskMetrics {
	var counters DiskMetrics
	for _, line := range strings.Split(data, "\n") {
		for _, field := range strings.Fields(line) {
			key, value, found := strings.Cut(field, "=")
			if !found {
				continue
			}

			v, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				continue
			}

			switch key {
			case "rbytes":
				counters.ReadBytesTotal += v
			case "wbytes":
				counters.WriteBytesTotal += v
			case "rios":
				counters.ReadOpsTotal += v
			case "wios":
				counters.WriteOpsTotal += v
			}
		}
	}

	return counters
}

// netCounters reads /proc/net/dev, which is scoped to the container's
// network namespace.
func (c *MetricsCollector) netCounters() []netCounters {
	data, err := os.ReadFile(filepath.Join(c.procRoot, "net/dev"))
	if err != nil {
		return nil
	}

	return parseNetDev(string(data))
}

// parseNetDev reads /proc/net/dev lines: "eth0: rx_bytes rx_packets
// rx_errs rx_drop ... tx_bytes tx_packets tx_errs tx_drop ...", with
// two header lines.
func parseNetDev(data string) []netCounters {
	var counters []netCounters
	for _, line := range strings.Split(data, "\n") {
		name, rest, found := strings.Cut(line, ":")
		if !found {
			continue
		}

		fields := strings.Fields(rest)
		if len(fields) < 12 {
			continue
		}

		values := make([]int64, 12)
		valid := true
		for i := range values {
			v, err := strconv.ParseInt(fields[i], 10, 64)
			if err != nil {
				valid = false

				break
			}

			values[i] = v
		}

		if !valid {
			continue
		}

		counters = append(counters, netCounters{
			name:      strings.TrimSpace(name),
			rxBytes:   values[0],
			rxPackets: values[1],
			rxErrors:  values[2],
			rxDropped: values[3],
			txBytes:   values[8],
			txPackets: values[9],
			txErrors:  values[10],
			txDropped: values[11],
		})
	}

	return counters
}

// memory reads the container's cgroup memory files, falling back to
// host-wide /proc/meminfo when the memory controller is not
// delegated. Used follows the working-set convention: current usage
// minus the inactive (readily reclaimable) page cache.
func (c *MetricsCollector) memory() MemoryMetrics {
	current, err := readInt(filepath.Join(c.cgroupRoot, "memory.current"))
	if err != nil {
		return c.memoryFromMeminfo()
	}

	var cache, inactiveFile int64
	if data, err := os.ReadFile(filepath.Join(c.cgroupRoot, "memory.stat")); err == nil {
		cache, inactiveFile = parseMemoryStat(string(data))
	}

	m := MemoryMetrics{
		UsedBytes:  positiveInt(current - inactiveFile),
		CacheBytes: cache,
		LimitBytes: c.memoryLimit(),
	}

	if swap, err := readInt(filepath.Join(c.cgroupRoot, "memory.swap.current")); err == nil {
		m.SwapUsedBytes = swap
	}

	return m
}

// memoryLimit is memory.max, or the host MemTotal when unlimited.
func (c *MetricsCollector) memoryLimit() int64 {
	data, err := os.ReadFile(filepath.Join(c.cgroupRoot, "memory.max"))
	if err != nil {
		return c.hostMemTotal()
	}

	limit, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return c.hostMemTotal()
	}

	return limit
}

// memoryFromMeminfo is the host-wide fallback: used is MemTotal -
// MemAvailable, node-exporter's headline pair.
func (c *MetricsCollector) memoryFromMeminfo() MemoryMetrics {
	info := c.meminfo()

	m := MemoryMetrics{
		UsedBytes:     positiveInt(info["MemTotal"] - info["MemAvailable"]),
		CacheBytes:    info["Cached"],
		LimitBytes:    info["MemTotal"],
		SwapUsedBytes: positiveInt(info["SwapTotal"] - info["SwapFree"]),
	}

	return m
}

func (c *MetricsCollector) hostMemTotal() int64 {
	return c.meminfo()["MemTotal"]
}

// meminfo parses /proc/meminfo ("MemTotal:  16384 kB") into bytes.
func (c *MetricsCollector) meminfo() map[string]int64 {
	info := make(map[string]int64)

	data, err := os.ReadFile(filepath.Join(c.procRoot, "meminfo"))
	if err != nil {
		return info
	}

	for _, line := range strings.Split(string(data), "\n") {
		key, rest, found := strings.Cut(line, ":")
		if !found {
			continue
		}

		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}

		v, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			continue
		}

		info[key] = v << 10 // kB to bytes
	}

	return info
}

// parseMemoryStat extracts the page cache ("file") and its inactive
// part from cgroup v2 memory.stat.
func parseMemoryStat(data string) (cache, inactiveFile int64) {
	for _, line := range strings.Split(data, "\n") {
		key, value, found := strings.Cut(line, " ")
		if !found {
			continue
		}

		v, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			continue
		}

		switch key {
		case "file":
			cache = v
		case "inactive_file":
			inactiveFile = v
		}
	}

	return cache, inactiveFile
}

// load reads the host load average from /proc/loadavg.
func (c *MetricsCollector) load() LoadMetrics {
	data, err := os.ReadFile(filepath.Join(c.procRoot, "loadavg"))
	if err != nil {
		return LoadMetrics{}
	}

	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return LoadMetrics{}
	}

	var load LoadMetrics
	load.Load1, _ = strconv.ParseFloat(fields[0], 64)
	load.Load5, _ = strconv.ParseFloat(fields[1], 64)
	load.Load15, _ = strconv.ParseFloat(fields[2], 64)

	return load
}

// filesystem reports the container root filesystem via statfs.
func (c *MetricsCollector) filesystem() FilesystemMetrics {
	var st syscall.Statfs_t
	if err := syscall.Statfs(c.rootfs, &st); err != nil {
		return FilesystemMetrics{Mount: c.rootfs}
	}

	// Statfs_t.Bsize is int64 on linux but uint32 on darwin. The hop
	// through uint64 keeps the widening conversion non-redundant on
	// both, so unconvert stays quiet whichever GOOS lints this file
	// (a plain int64(st.Bsize) is flagged on linux, and a
	// //nolint:unconvert directive is flagged as unused on darwin).
	bsize := int64(uint64(st.Bsize))

	return FilesystemMetrics{
		Mount:      c.rootfs,
		SizeBytes:  int64(st.Blocks) * bsize,
		UsedBytes:  int64(st.Blocks-st.Bfree) * bsize,
		AvailBytes: int64(st.Bavail) * bsize,
	}
}

// uptime is the container uptime: the age of PID 1, derived from its
// starttime (jiffies since host boot) and the host uptime.
func (c *MetricsCollector) uptime() time.Duration {
	hostUptime, err := c.hostUptime()
	if err != nil {
		return 0
	}

	startTicks, err := pid1StartTicks(filepath.Join(c.procRoot, "1/stat"))
	if err != nil {
		return 0
	}

	uptime := hostUptime - time.Duration(float64(startTicks)/userHZ*float64(time.Second))
	if uptime < 0 {
		return 0
	}

	return uptime
}

func (c *MetricsCollector) hostUptime() (time.Duration, error) {
	data, err := os.ReadFile(filepath.Join(c.procRoot, "uptime"))
	if err != nil {
		return 0, fmt.Errorf("read uptime: %w", err)
	}

	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, fmt.Errorf("empty uptime")
	}

	seconds, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, fmt.Errorf("parse uptime: %w", err)
	}

	return time.Duration(seconds * float64(time.Second)), nil
}

// pid1StartTicks reads field 22 (starttime) of /proc/1/stat; fields
// are counted after the parenthesised comm, which may contain spaces.
func pid1StartTicks(path string) (int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read pid 1 stat: %w", err)
	}

	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return 0, fmt.Errorf("malformed pid 1 stat")
	}

	// The remainder starts at field 3 (state); starttime is field 22.
	fields := strings.Fields(string(data)[end+1:])
	if len(fields) < 20 {
		return 0, fmt.Errorf("malformed pid 1 stat")
	}

	ticks, err := strconv.ParseInt(fields[19], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse starttime: %w", err)
	}

	return ticks, nil
}

// procs counts the processes visible in the container's PID
// namespace: the numeric entries of /proc.
func (c *MetricsCollector) procs() int {
	entries, err := os.ReadDir(c.procRoot)
	if err != nil {
		return 0
	}

	count := 0
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err == nil {
			count++
		}
	}

	return count
}

// readInt reads one file holding a single integer (cgroup v2 style).
func readInt(path string) (int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}

	v, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}

	return v, nil
}

func positiveInt(v int64) int64 {
	if v < 0 {
		return 0
	}

	return v
}

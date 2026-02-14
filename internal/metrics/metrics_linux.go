package metrics

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Snapshot struct {
	TS int64 `json:"ts"`

	CPU float64 `json:"cpu"` // %
	Mem float64 `json:"mem"` // %

	MemTotalBytes uint64 `json:"mem_total_bytes,omitempty"`
	MemUsedBytes  uint64 `json:"mem_used_bytes,omitempty"`

	Disk float64 `json:"disk"` // %
	DiskTotalBytes uint64 `json:"disk_total_bytes,omitempty"`
	DiskUsedBytes  uint64 `json:"disk_used_bytes,omitempty"`

	Swap float64 `json:"swap"` // %
	SwapTotalBytes uint64 `json:"swap_total_bytes,omitempty"`
	SwapUsedBytes  uint64 `json:"swap_used_bytes,omitempty"`

	BytesUpTotal   uint64 `json:"bytes_up_total"`
	BytesDownTotal uint64 `json:"bytes_down_total"`
	NetUpBPS       uint64 `json:"net_up_bps"`
	NetDownBPS     uint64 `json:"net_down_bps"`
}

type Collector struct {
	iface string

	prevCPU *cpuTimes
	prevNet *netCounters
	prevTS  time.Time
}

func NewCollector(netIface string) *Collector {
	return &Collector{
		iface: netIface,
	}
}

func (c *Collector) Collect() (Snapshot, error) {
	now := time.Now()
	s := Snapshot{TS: now.Unix()}

	// CPU
	ct, err := readCPUTimes()
	if err == nil {
		if c.prevCPU != nil {
			s.CPU = cpuPercent(*c.prevCPU, ct)
		}
		c.prevCPU = &ct
	}

	// Mem + Swap
	if mt, mu, st, su, err := readMemSwap(); err == nil {
		s.MemTotalBytes = mt
		s.MemUsedBytes = mu
		if mt > 0 {
			s.Mem = float64(mu) * 100.0 / float64(mt)
		}
		s.SwapTotalBytes = st
		s.SwapUsedBytes = su
		if st > 0 {
			s.Swap = float64(su) * 100.0 / float64(st)
		}
	}

	// Disk
	if dt, du, err := readDisk("/"); err == nil {
		s.DiskTotalBytes = dt
		s.DiskUsedBytes = du
		if dt > 0 {
			s.Disk = float64(du) * 100.0 / float64(dt)
		}
	}

	// Net
	iface := c.iface
	if iface == "" || iface == "auto" {
		iface = pickIface()
	}
	nc, err := readNet(iface)
	if err == nil {
		s.BytesUpTotal = nc.txBytes
		s.BytesDownTotal = nc.rxBytes
		if c.prevNet != nil && !c.prevTS.IsZero() {
			dt := now.Sub(c.prevTS).Seconds()
			if dt > 0 {
				// bytes per second
				s.NetUpBPS = uint64(float64(diffU64(c.prevNet.txBytes, nc.txBytes)) / dt)
				s.NetDownBPS = uint64(float64(diffU64(c.prevNet.rxBytes, nc.rxBytes)) / dt)
			}
		}
		c.prevNet = &nc
		c.prevTS = now
	}

	// If we can't read anything meaningful, return error
	if c.prevCPU == nil && c.prevNet == nil && s.MemTotalBytes == 0 && s.DiskTotalBytes == 0 {
		return s, errors.New("no metrics available (unsupported platform?)")
	}
	return s, nil
}

type cpuTimes struct {
	user, nice, system, idle, iowait, irq, softirq, steal uint64
}

func readCPUTimes() (cpuTimes, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return cpuTimes{}, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "cpu ") {
			parts := strings.Fields(line)
			// cpu user nice system idle iowait irq softirq steal ...
			if len(parts) < 9 {
				return cpuTimes{}, fmt.Errorf("bad /proc/stat cpu line")
			}
			val := make([]uint64, 8)
			for i := 0; i < 8; i++ {
				u, e := strconv.ParseUint(parts[i+1], 10, 64)
				if e != nil {
					return cpuTimes{}, e
				}
				val[i] = u
			}
			return cpuTimes{
				user: val[0], nice: val[1], system: val[2], idle: val[3],
				iowait: val[4], irq: val[5], softirq: val[6], steal: val[7],
			}, nil
		}
	}
	return cpuTimes{}, fmt.Errorf("cpu line not found in /proc/stat")
}

func cpuPercent(prev, cur cpuTimes) float64 {
	prevIdle := prev.idle + prev.iowait
	curIdle := cur.idle + cur.iowait

	prevNonIdle := prev.user + prev.nice + prev.system + prev.irq + prev.softirq + prev.steal
	curNonIdle := cur.user + cur.nice + cur.system + cur.irq + cur.softirq + cur.steal

	prevTotal := prevIdle + prevNonIdle
	curTotal := curIdle + curNonIdle

	totald := float64(curTotal - prevTotal)
	idled := float64(curIdle - prevIdle)
	if totald <= 0 {
		return 0
	}
	pct := (totald - idled) * 100.0 / totald
	if pct < 0 {
		return 0
	}
	if pct > 100 {
		return 100
	}
	return pct
}

func readMemSwap() (memTotal, memUsed, swapTotal, swapUsed uint64, err error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, 0, 0, err
	}
	defer f.Close()

	var memFree, buffers, cached, sreclaimable, shmem uint64
	var swapFree uint64

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		key := strings.TrimSuffix(parts[0], ":")
		val, e := strconv.ParseUint(parts[1], 10, 64)
		if e != nil {
			continue
		}
		// values are in kB
		val *= 1024
		switch key {
		case "MemTotal":
			memTotal = val
		case "MemFree":
			memFree = val
		case "Buffers":
			buffers = val
		case "Cached":
			cached = val
		case "SReclaimable":
			sreclaimable = val
		case "Shmem":
			shmem = val
		case "SwapTotal":
			swapTotal = val
		case "SwapFree":
			swapFree = val
		}
	}
	// Linux "available" estimation (rough): free+buffers+cached+sreclaimable-shmem
	avail := memFree + buffers + cached + sreclaimable
	if avail > shmem {
		avail -= shmem
	}
	if memTotal > avail {
		memUsed = memTotal - avail
	}
	if swapTotal > swapFree {
		swapUsed = swapTotal - swapFree
	}
	return
}

func readDisk(path string) (total, used uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	total = st.Blocks * uint64(st.Bsize)
	free := st.Bavail * uint64(st.Bsize)
	if total > free {
		used = total - free
	}
	return total, used, nil
}

type netCounters struct {
	iface   string
	rxBytes uint64
	txBytes uint64
}

func pickIface() string {
	// very small heuristic: pick first non-lo interface that appears in /proc/net/dev
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return "eth0"
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "Inter-") || strings.HasPrefix(line, "face") || line == "" {
			continue
		}
		// "<iface>: ..."
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		iface := strings.TrimSpace(parts[0])
		if iface == "lo" || strings.HasPrefix(iface, "docker") || strings.HasPrefix(iface, "veth") {
			continue
		}
		return iface
	}
	return "eth0"
}

func readNet(iface string) (netCounters, error) {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return netCounters{}, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "Inter-") || strings.HasPrefix(line, "face") || line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		if strings.TrimSpace(parts[0]) != iface {
			continue
		}
		fields := strings.Fields(parts[1])
		// rx bytes is field[0], tx bytes is field[8]
		if len(fields) < 9 {
			return netCounters{}, fmt.Errorf("bad /proc/net/dev line for %s", iface)
		}
		rx, _ := strconv.ParseUint(fields[0], 10, 64)
		tx, _ := strconv.ParseUint(fields[8], 10, 64)
		return netCounters{iface: iface, rxBytes: rx, txBytes: tx}, nil
	}
	return netCounters{}, fmt.Errorf("iface not found: %s", iface)
}

func diffU64(prev, cur uint64) uint64 {
	if cur >= prev {
		return cur - prev
	}
	// counter reset
	return cur
}

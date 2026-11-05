// Package collect 采集主机指标与内核参数体检（CacheOS 规格书 6.2、7 节 M3）。
package collect

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"

	agentv1 "github.com/xiaoguan521/cacheos/api/gen/agent/v1"
)

// Host 返回 CPU 使用率（两次调用之间）与已用内存。
type Host struct {
	mu         sync.Mutex
	idle, tot  uint64
	haveSample bool
}

func (h *Host) Sample() (cpuPercent float64, memUsed uint64) {
	if b, err := os.ReadFile("/proc/stat"); err == nil {
		f := strings.Fields(strings.SplitN(string(b), "\n", 2)[0])
		var idle, tot uint64
		for i, v := range f[1:] {
			n, _ := strconv.ParseUint(v, 10, 64)
			tot += n
			if i == 3 || i == 4 { // idle + iowait
				idle += n
			}
		}
		h.mu.Lock()
		if h.haveSample && tot > h.tot {
			cpuPercent = 100 * (1 - float64(idle-h.idle)/float64(tot-h.tot))
		}
		h.idle, h.tot, h.haveSample = idle, tot, true
		h.mu.Unlock()
	}
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		var total, avail uint64
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) < 2 {
				continue
			}
			n, _ := strconv.ParseUint(f[1], 10, 64)
			switch f[0] {
			case "MemTotal:":
				total = n * 1024
			case "MemAvailable:":
				avail = n * 1024
			}
		}
		if total > avail {
			memUsed = total - avail
		}
	}
	return
}

// MemTotal 返回物理内存总量（字节）。
func MemTotal() uint64 {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[0] == "MemTotal:" {
			n, _ := strconv.ParseUint(f[1], 10, 64)
			return n * 1024
		}
	}
	return 0
}

// OSRelease 返回 /etc/os-release 的 PRETTY_NAME。
func OSRelease() string {
	b, _ := os.ReadFile("/etc/os-release")
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
			return strings.Trim(v, `"'`)
		}
	}
	return ""
}

// Kernel 返回内核版本。
func Kernel() string {
	b, _ := os.ReadFile("/proc/sys/kernel/osrelease")
	return strings.TrimSpace(string(b))
}

// ParseTHP 从 "always madvise [never]" 中取出选中的值。
func ParseTHP(s string) string {
	if i := strings.IndexByte(s, '['); i >= 0 {
		if j := strings.IndexByte(s[i:], ']'); j > 0 {
			return s[i+1 : i+j]
		}
	}
	return strings.TrimSpace(s)
}

func readInt(path string) int64 {
	b, _ := os.ReadFile(path)
	n, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return n
}

// Tuning 读取缓存服务关心的内核参数（THP、overcommit、somaxconn、文件句柄上限）。
func Tuning() *agentv1.HostTuning {
	t := &agentv1.HostTuning{
		OvercommitMemory: readInt("/proc/sys/vm/overcommit_memory"),
		Somaxconn:        readInt("/proc/sys/net/core/somaxconn"),
	}
	if b, err := os.ReadFile("/sys/kernel/mm/transparent_hugepage/enabled"); err == nil {
		t.TransparentHugepage = ParseTHP(string(b))
	}
	var rl syscall.Rlimit
	if syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rl) == nil {
		t.NofileLimit = rl.Cur
	}
	return t
}

// DiskFree 返回 path 所在分区的可用空间。
func DiskFree(path string) uint64 {
	var st syscall.Statfs_t
	if syscall.Statfs(path, &st) != nil {
		return 0
	}
	return st.Bavail * uint64(st.Bsize)
}

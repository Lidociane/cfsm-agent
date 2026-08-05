//go:build linux

package cfprobe

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func readCPUTimes() (cpuTimes, bool) {
	line := ""
	_ = scanFile("/proc/stat", func(s string) {
		if line == "" && strings.HasPrefix(s, "cpu ") {
			line = s
		}
	})
	fields := strings.Fields(line)
	if len(fields) < 5 {
		return cpuTimes{}, false
	}
	var total uint64
	for i, f := range fields[1:] {
		if i >= 8 {
			break
		}
		n, _ := strconv.ParseUint(f, 10, 64)
		total += n
	}
	idle, _ := strconv.ParseUint(fields[4], 10, 64)
	if len(fields) > 5 {
		iowait, _ := strconv.ParseUint(fields[5], 10, 64)
		idle += iowait
	}
	return cpuTimes{Total: total, Idle: idle}, true
}

func collectBasicStats() BasicStats {
	mem := readMemInfo()
	diskTotal, diskUsed := diskUsageLinux()
	return BasicStats{
		MemTotalMB:  mem["MemTotal"] / 1024,
		MemUsedMB:   usedMemMB(mem),
		SwapTotalMB: mem["SwapTotal"] / 1024,
		SwapUsedMB:  usedSwapMB(mem),
		DiskTotalMB: diskTotal,
		DiskUsedMB:  diskUsed,
		LoadAvg:     linuxLoadAvg(),
		BootTimeMS:  linuxBootTimeMS(),
		OSName:      linuxOSName(),
		Arch:        runtime.GOARCH,
		Kernel:      commandOutput("uname", "-r"),
		CPUInfo:     linuxCPUInfo(),
		CPUCores:    runtime.NumCPU(),
		GPUInfo:     detectGPUInfo(),
		Processes:   linuxProcessCount(),
		TCPConn:     linuxConnCount("tcp") + linuxConnCount("tcp6"),
		UDPConn:     linuxConnCount("udp") + linuxConnCount("udp6"),
	}
}

func readNetBytes(ifaces string) NetBytes {
	wanted := splitInterfaceSet(ifaces)
	var total NetBytes
	_ = scanFile("/proc/net/dev", func(line string) {
		if !strings.Contains(line, ":") {
			return
		}
		name, rest, _ := strings.Cut(strings.TrimSpace(line), ":")
		name = strings.TrimSpace(name)
		fields := strings.Fields(rest)
		if len(fields) < 16 {
			return
		}
		if !shouldIncludeNetInterface(name, wanted) {
			return
		}
		rx, _ := strconv.ParseUint(fields[0], 10, 64)
		tx, _ := strconv.ParseUint(fields[8], 10, 64)
		total.RX += rx
		total.TX += tx
	})
	return total
}

func readMemInfo() map[string]uint64 {
	out := map[string]uint64{}
	_ = scanFile("/proc/meminfo", func(line string) {
		key, rest, ok := strings.Cut(line, ":")
		if !ok {
			return
		}
		out[key] = parseFirstUint(rest)
	})
	return out
}

func usedMemMB(mem map[string]uint64) uint64 {
	return memoryUsedMBFromKB(mem["MemTotal"], mem["MemAvailable"], mem["MemFree"], mem["Buffers"], mem["Cached"])
}

func usedSwapMB(mem map[string]uint64) uint64 {
	return swapUsedMBFromKB(mem["SwapTotal"], mem["SwapFree"])
}

func linuxLoadAvg() string {
	fields := strings.Fields(readSmallFile("/proc/loadavg"))
	if len(fields) >= 3 {
		return strings.Join(fields[:3], " ")
	}
	return "0 0 0"
}

func linuxBootTimeMS() int64 {
	var boot int64
	_ = scanFile("/proc/stat", func(line string) {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "btime" {
			boot, _ = strconv.ParseInt(fields[1], 10, 64)
		}
	})
	if boot > 0 {
		return boot * 1000
	}
	uptime := strings.Fields(readSmallFile("/proc/uptime"))
	if len(uptime) > 0 {
		f, _ := strconv.ParseFloat(uptime[0], 64)
		if f > 0 {
			return time.Now().Add(-time.Duration(f) * time.Second).UnixMilli()
		}
	}
	return 0
}

func linuxOSName() string {
	if isSynology() {
		if name := synologyOSName(); name != "" {
			return name
		}
	}
	values, err := parseKVFile("/etc/os-release")
	if err == nil {
		if pretty := values["PRETTY_NAME"]; pretty != "" {
			return pretty
		}
		if id := values["ID"]; id != "" {
			return id
		}
	}
	return "Linux"
}

func synologyOSName() string {
	values, err := parseKVFile("/etc.defaults/VERSION")
	if err != nil {
		values, err = parseKVFile("/etc/VERSION")
	}
	if err != nil {
		return "Synology DSM"
	}
	product := values["productversion"]
	build := values["buildnumber"]
	if product == "" {
		return "Synology DSM"
	}
	if build != "" {
		return "Synology DSM " + product + "-" + build
	}
	return "Synology DSM " + product
}

func linuxCPUInfo() string {
	info := ""
	_ = scanFile("/proc/cpuinfo", func(line string) {
		if info != "" {
			return
		}
		if strings.HasPrefix(line, "model name") || strings.HasPrefix(line, "Hardware") || strings.HasPrefix(line, "Processor") {
			_, v, ok := strings.Cut(line, ":")
			if ok {
				info = strings.TrimSpace(v)
			}
		}
	})
	if info == "" {
		info = runtime.GOARCH
	}
	return info
}

func linuxProcessCount() int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err == nil {
			count++
		}
	}
	return count
}

func linuxConnCount(name string) int {
	count := 0
	_ = scanFile("/proc/net/"+name, func(line string) {
		if strings.HasPrefix(strings.TrimSpace(line), "sl") {
			return
		}
		fields := strings.Fields(line)
		if len(fields) >= 4 {
			if strings.HasPrefix(name, "tcp") {
				if fields[3] == "01" {
					count++
				}
			} else {
				count++
			}
		}
	})
	return count
}

func shouldIncludeNetInterface(name string, wanted map[string]bool) bool {
	if isExcludedNetInterface(name) {
		return false
	}
	if len(wanted) == 0 {
		return true
	}
	for pattern := range wanted {
		if pattern == name {
			return true
		}
		if matched, err := filepath.Match(pattern, name); err == nil && matched {
			return true
		}
	}
	return false
}

func isExcludedNetInterface(name string) bool {
	for _, prefix := range []string{"br", "cni", "docker", "podman", "flannel", "lo", "veth", "virbr", "vmbr", "tap", "fwbr", "fwpr"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

type diskUsageEntry struct {
	total uint64
	used  uint64
}

func diskUsageLinux() (uint64, uint64) {
	devices := map[string]diskUsageEntry{}
	_ = scanFile("/proc/mounts", func(line string) {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			return
		}
		dev, mountPoint, fsType := fields[0], fields[1], fields[2]
		opts := ""
		if len(fields) >= 4 {
			opts = fields[3]
		}
		mountPoint = unescapeLinuxMountPoint(mountPoint)
		if !includeLinuxMount(dev, mountPoint, fsType, opts) {
			return
		}
		var st syscall.Statfs_t
		if err := syscall.Statfs(mountPoint, &st); err != nil {
			return
		}
		size, used, ok := diskUsageMBFromBlocks(st.Blocks, st.Bfree, int64(st.Bsize))
		if !ok {
			return
		}
		deviceID := dev
		if strings.ToLower(fsType) == "zfs" {
			if idx := strings.Index(deviceID, "/"); idx != -1 {
				deviceID = deviceID[:idx]
			}
		}
		entry := diskUsageEntry{total: size, used: used}
		if existing, ok := devices[deviceID]; !ok || entry.total > existing.total {
			devices[deviceID] = entry
		}
	})
	var total, used uint64
	for _, entry := range devices {
		total += entry.total
		used += entry.used
	}
	return total, used
}

func includeLinuxMount(dev, mountPoint, fsType, opts string) bool {
	if mountPoint == "/" {
		return true
	}
	mountPointLower := strings.ToLower(mountPoint)
	for _, prefix := range []string{"/tmp", "/var/tmp", "/dev", "/run", "/var/lib/containers", "/var/lib/docker", "/proc", "/sys", "/sys/fs/cgroup", "/etc/resolv.conf", "/etc/host", "/nix/store"} {
		if mountPointLower == prefix || strings.HasPrefix(mountPointLower, prefix) {
			return false
		}
	}
	fsTypeLower := strings.ToLower(fsType)
	if fsTypeLower == "autofs" && !strings.HasPrefix(dev, "/dev/") {
		return false
	}
	if fsTypeLower == "fuseblk" {
		return true
	}
	for _, excluded := range []string{"tmpfs", "devtmpfs", "udev", "nfs", "cifs", "smb", "vboxsf", "9p", "fuse", "overlay", "proc", "devpts", "sysfs", "cgroup", "mqueue", "hugetlbfs", "debugfs", "binfmt_misc", "securityfs"} {
		if fsTypeLower == excluded || strings.HasPrefix(fsTypeLower, excluded) {
			return false
		}
	}
	optsLower := strings.ToLower(opts)
	if strings.Contains(optsLower, "remote") || strings.Contains(optsLower, "network") {
		return false
	}
	if strings.HasPrefix(dev, "/dev/loop") {
		return false
	}
	return true
}

func unescapeLinuxMountPoint(path string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(path)
}

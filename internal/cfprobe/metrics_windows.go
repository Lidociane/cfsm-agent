//go:build windows

package cfprobe

import (
	"runtime"
	"strconv"
	"strings"
	"time"
)

func readCPUTimes() (cpuTimes, bool) {
	return cpuTimes{}, false
}

func collectBasicStats() BasicStats {
	total, used := windowsMemoryMB()
	diskTotal, diskUsed := windowsDiskUsage()
	return BasicStats{
		MemTotalMB:  total,
		MemUsedMB:   used,
		SwapTotalMB: 0,
		SwapUsedMB:  0,
		DiskTotalMB: diskTotal,
		DiskUsedMB:  diskUsed,
		LoadAvg:     "0 0 0",
		BootTimeMS:  windowsBootTimeMS(),
		OSName:      firstNonEmpty(commandOutput("powershell", "-NoProfile", "-Command", "(Get-CimInstance Win32_OperatingSystem).Caption"), "Windows"),
		Arch:        runtime.GOARCH,
		Kernel:      firstNonEmpty(commandOutput("cmd", "/C", "ver"), "Windows"),
		CPUInfo:     firstNonEmpty(commandOutput("powershell", "-NoProfile", "-Command", "(Get-CimInstance Win32_Processor | Select-Object -First 1 -ExpandProperty Name)"), runtime.GOARCH),
		CPUCores:    runtime.NumCPU(),
		GPUInfo:     detectGPUInfo(),
		Processes:   windowsProcessCount(),
		TCPConn:     windowsConnCount("TCP"),
		UDPConn:     windowsConnCount("UDP"),
	}
}

func readNetBytes(ifaces string) NetBytes {
	script := "Get-NetAdapterStatistics | ForEach-Object { \"$($_.Name),$($_.ReceivedBytes),$($_.SentBytes)\" }"
	out := commandOutput("powershell", "-NoProfile", "-Command", script)
	wanted := splitInterfaceSet(ifaces)
	var total NetBytes
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Split(strings.TrimSpace(line), ",")
		if len(parts) != 3 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		if len(wanted) > 0 && !wanted[name] {
			continue
		}
		rx, _ := strconv.ParseUint(strings.TrimSpace(parts[1]), 10, 64)
		tx, _ := strconv.ParseUint(strings.TrimSpace(parts[2]), 10, 64)
		total.RX += rx
		total.TX += tx
	}
	return total
}

func windowsMemoryMB() (uint64, uint64) {
	out := commandOutput("powershell", "-NoProfile", "-Command", "$o=Get-CimInstance Win32_OperatingSystem; \"$($o.TotalVisibleMemorySize),$($o.FreePhysicalMemory)\"")
	parts := strings.Split(out, ",")
	if len(parts) != 2 {
		return 0, 0
	}
	totalKB := parseFirstUint(parts[0])
	freeKB := parseFirstUint(parts[1])
	if totalKB < freeKB {
		return totalKB / 1024, 0
	}
	return totalKB / 1024, (totalKB - freeKB) / 1024
}

func windowsDiskUsage() (uint64, uint64) {
	out := commandOutput("powershell", "-NoProfile", "-Command", "Get-CimInstance Win32_LogicalDisk -Filter \"DriveType=3\" | ForEach-Object { \"$($_.Size),$($_.FreeSpace)\" }")
	var total, used uint64
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Split(strings.TrimSpace(line), ",")
		if len(parts) != 2 {
			continue
		}
		size := parseFirstUint(parts[0]) / 1024 / 1024
		free := parseFirstUint(parts[1]) / 1024 / 1024
		if size >= free {
			total += size
			used += size - free
		}
	}
	return total, used
}

func windowsBootTimeMS() int64 {
	out := commandOutput("powershell", "-NoProfile", "-Command", "([DateTimeOffset](Get-CimInstance Win32_OperatingSystem).LastBootUpTime).ToUnixTimeMilliseconds()")
	n, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil {
		return time.Now().UnixMilli()
	}
	return n
}

func windowsProcessCount() int {
	out := commandOutput("powershell", "-NoProfile", "-Command", "(Get-Process).Count")
	n, _ := strconv.Atoi(strings.TrimSpace(out))
	return n
}

func windowsConnCount(kind string) int {
	cmdlet := "Get-NetTCPConnection"
	if kind == "UDP" {
		cmdlet = "Get-NetUDPEndpoint"
	}
	out := commandOutput("powershell", "-NoProfile", "-Command", "("+cmdlet+").Count")
	n, _ := strconv.Atoi(strings.TrimSpace(out))
	return n
}

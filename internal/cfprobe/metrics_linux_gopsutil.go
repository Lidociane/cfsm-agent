//go:build linux && (amd64 || arm64 || 386 || arm || loong64)

package cfprobe

import (
	"sync"

	gopsutilCPU "github.com/shirou/gopsutil/v4/cpu"
)

var (
	linuxCPUMu    sync.Mutex
	linuxCPUTimes cpuTimes
)

func readGopsutilCPUTimes() (cpuTimes, bool) {
	percentages, err := gopsutilCPU.Percent(0, false)
	if err != nil || len(percentages) == 0 {
		return cpuTimes{}, false
	}

	linuxCPUMu.Lock()
	defer linuxCPUMu.Unlock()
	linuxCPUTimes = cpuTimesFromPercent(linuxCPUTimes, percentages[0])
	return linuxCPUTimes, true
}

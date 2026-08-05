package cfprobe

import (
	"bufio"
	"encoding/json"
	"os"
	"runtime"
	"strconv"
	"strings"
)

type cpuTimes struct {
	Total uint64
	Idle  uint64
}

type gpuMetric struct {
	Name string `json:"name"`
	Info any    `json:"info"`
	ID   string `json:"id"`
}

const bytesPerMiB = 1024 * 1024

func uintString(v uint64) string {
	return strconv.FormatUint(v, 10)
}

func intString(v int) string {
	if v < 0 {
		v = 0
	}
	return strconv.Itoa(v)
}

func floatString(v float64) string {
	if v < 0 {
		v = 0
	}
	if v > 100 {
		v = 100
	}
	return strconv.FormatFloat(v, 'f', 2, 64)
}

func cpuUsagePercent(prev, current cpuTimes) (float64, bool) {
	if current.Total < prev.Total || current.Idle < prev.Idle {
		return 0, false
	}
	totalDelta := current.Total - prev.Total
	idleDelta := current.Idle - prev.Idle
	if totalDelta == 0 || idleDelta > totalDelta {
		return 0, false
	}
	return float64(totalDelta-idleDelta) / float64(totalDelta) * 100, true
}

func cpuPercentString(v float64) string {
	return floatString(v)
}

func diskUsageMBFromBlocks(blocks, bfree uint64, bsize int64) (uint64, uint64, bool) {
	if bsize <= 0 || blocks < bfree {
		return 0, 0, false
	}
	total := blocks * uint64(bsize) / bytesPerMiB
	used := (blocks - bfree) * uint64(bsize) / bytesPerMiB
	return total, used, total > 0
}

func memoryUsedMBFromKB(total, available, free, buffers, cached uint64) uint64 {
	if available == 0 {
		available = free + buffers + cached
	}
	if total < available {
		return 0
	}
	return (total - available) / 1024
}

func swapUsedMBFromKB(total, free uint64) uint64 {
	if total < free {
		return 0
	}
	return (total - free) / 1024
}

func readSmallFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func scanFile(path string, fn func(string)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fn(scanner.Text())
	}
	return scanner.Err()
}

func parseFirstUint(raw string) uint64 {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return 0
	}
	n, _ := strconv.ParseUint(fields[0], 10, 64)
	return n
}

func detectGPUInfo() any {
	if commandExists("nvidia-smi") {
		out := commandOutput("nvidia-smi", "--query-gpu=index,name,utilization.gpu", "--format=csv,noheader,nounits")
		if info := parseNvidiaSMI(out); len(info) > 0 {
			return info
		}
	}
	if commandExists("rocm-smi") {
		out := commandOutput("rocm-smi", "--showproductname", "--showuse")
		if strings.TrimSpace(out) != "" {
			return []gpuMetric{{Name: "AMD ROCm GPU", Info: 0, ID: "0"}}
		}
	}
	return nil
}

func parseNvidiaSMI(out string) []gpuMetric {
	var gpus []gpuMetric
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) < 3 {
			continue
		}
		id := strings.TrimSpace(parts[0])
		utilRaw := strings.TrimSpace(parts[len(parts)-1])
		name := strings.TrimSpace(strings.Join(parts[1:len(parts)-1], ","))
		util, err := strconv.ParseFloat(utilRaw, 64)
		var utilAny any = nil
		if err == nil {
			utilAny = util
		}
		gpus = append(gpus, gpuMetric{Name: name, Info: utilAny, ID: id})
	}
	return gpus
}

func metricsToMap(m Metrics) map[string]any {
	return map[string]any{
		"cpu":            m.CPU,
		"ram_total":      m.RAMTotal,
		"ram_used":       m.RAMUsed,
		"swap_total":     m.SwapTotal,
		"swap_used":      m.SwapUsed,
		"disk_total":     m.DiskTotal,
		"disk_used":      m.DiskUsed,
		"load_avg":       m.LoadAvg,
		"boot_time":      m.BootTime,
		"net_rx":         m.NetRX,
		"net_tx":         m.NetTX,
		"net_rx_monthly": m.NetRXMonthly,
		"net_tx_monthly": m.NetTXMonthly,
		"net_in_speed":   m.NetInSpeed,
		"net_out_speed":  m.NetOutSpeed,
		"os":             m.OS,
		"arch":           m.Arch,
		"kernel_version": m.Kernel,
		"cpu_info":       m.CPUInfo,
		"cpu_cores":      m.CPUCores,
		"gpu_info":       m.GPUInfo,
		"processes":      m.Processes,
		"tcp_conn":       m.TCPConn,
		"udp_conn":       m.UDPConn,
		"ip_v4":          m.IPv4,
		"ip_v6":          m.IPv6,
		"ping_ct":        m.PingCT,
		"ping_cu":        m.PingCU,
		"ping_cm":        m.PingCM,
		"ping_bd":        m.PingBD,
		"loss_ct":        m.LossCT,
		"loss_cu":        m.LossCU,
		"loss_cm":        m.LossCM,
		"loss_bd":        m.LossBD,
	}
}

func sampleMetricsToMap(m Metrics) map[string]any {
	return map[string]any{
		"cpu":           m.CPU,
		"ram_total":     m.RAMTotal,
		"ram_used":      m.RAMUsed,
		"swap_total":    m.SwapTotal,
		"swap_used":     m.SwapUsed,
		"net_in_speed":  m.NetInSpeed,
		"net_out_speed": m.NetOutSpeed,
	}
}

func toJSONSize(v any) int {
	data, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len(data)
}

func fallbackArch() string {
	return runtime.GOARCH
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

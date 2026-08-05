package cfprobe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Agent struct {
	cfg     Config
	paths   Paths
	log     logger
	version string

	mu       sync.RWMutex
	probes   ProbeSnapshot
	basic    BasicStats
	basicAt  time.Time
	prevNet  NetBytes
	prevTime time.Time
	prevCPU  cpuTimes
	cpuOK    bool

	samples    []map[string]any
	lastReport time.Time
}

func Run(configFile string, debug bool, version string) error {
	paths := defaultPaths(serviceNameDefault, "")
	if configFile != "" {
		paths.ConfigFile = configFile
		paths.ConfigDir = filepath.Dir(configFile)
		paths.TrafficFile = filepath.Join(paths.ConfigDir, "traffic.dat")
	}
	cfg, err := readConfig(paths.ConfigFile)
	if err != nil {
		return fmt.Errorf("读取配置失败 %s: %w", paths.ConfigFile, err)
	}
	if cfg.ServerID == "" || cfg.Secret == "" || cfg.WorkerURL == "" {
		return errors.New("配置缺失: SERVER_ID/SECRET/WORKER_URL 不能为空")
	}
	normalizeConfigIntervals(&cfg)

	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals()...)
	defer stop()

	a := &Agent{
		cfg:      cfg,
		paths:    paths,
		log:      newLogger(debug),
		version:  version,
		prevNet:  readNetBytes(cfg.Interface),
		prevTime: time.Now(),
		cpuOK:    true,
	}
	a.basic = collectBasicStats()
	a.basicAt = time.Now()

	a.log.info("CF-Server-Monitor Go Probe started version=%s platform=%s config=%s", version, platformName(), paths.ConfigFile)
	a.log.debugf("config id=%s url=%s report_interval=%ds collect_interval=%ds reset_day=%d interface=%s auto_update=%v",
		cfg.ServerID, cfg.WorkerURL, cfg.ReportInterval, cfg.CollectInterval, cfg.ResetDay, firstNonEmpty(cfg.Interface, "auto"), cfg.AutoUpdate)

	go a.networkWorker(ctx)
	return a.loop(ctx)
}

func (a *Agent) loop(ctx context.Context) error {
	active := time.Duration(a.cfg.ReportInterval) * time.Second
	if a.cfg.CollectInterval > 0 {
		active = time.Duration(a.cfg.CollectInterval) * time.Second
	}
	if active < time.Second {
		active = time.Duration(defaultReportIntervalSec) * time.Second
	}

	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			a.log.info("probe stopped")
			return nil
		case <-timer.C:
			a.tick()
			active = time.Duration(a.cfg.ReportInterval) * time.Second
			if a.cfg.CollectInterval > 0 {
				active = time.Duration(a.cfg.CollectInterval) * time.Second
			}
			timer.Reset(active)
		}
	}
}

func (a *Agent) tick() {
	now := time.Now()
	if now.Sub(a.basicAt) >= 60*time.Second || a.basicAt.IsZero() {
		a.basic = collectBasicStats()
		a.basicAt = now
	}
	netNow := readNetBytes(a.cfg.Interface)
	dt := now.Sub(a.prevTime).Seconds()
	if dt <= 0 {
		dt = float64(a.cfg.ReportInterval)
	}
	rxDelta := uint64(0)
	txDelta := uint64(0)
	if netNow.RX >= a.prevNet.RX {
		rxDelta = netNow.RX - a.prevNet.RX
	}
	if netNow.TX >= a.prevNet.TX {
		txDelta = netNow.TX - a.prevNet.TX
	}
	rxSpeed := uint64(float64(rxDelta) / dt)
	txSpeed := uint64(float64(txDelta) / dt)
	a.prevNet = netNow
	a.prevTime = now

	cpu := "0.00"
	if current, ok := readCPUTimes(); ok && a.cpuOK {
		if usage, ok := cpuUsagePercent(a.prevCPU, current); ok {
			cpu = cpuPercentString(usage)
		}
		a.prevCPU = current
	} else {
		a.prevCPU, a.cpuOK = readCPUTimes()
	}

	rxMonthly, txMonthly := calcMonthlyTraffic(a.paths.TrafficFile, netNow, a.cfg.ResetDay, a.cfg.Interface)
	m := a.buildMetrics(cpu, netNow, rxSpeed, txSpeed, rxMonthly, txMonthly)
	if a.cfg.CollectInterval > 0 {
		a.samples = append(a.samples, map[string]any{
			"ts":      now.UnixMilli(),
			"metrics": sampleMetricsToMap(m),
		})
	}
	if a.lastReport.IsZero() || now.Sub(a.lastReport) >= time.Duration(a.cfg.ReportInterval)*time.Second {
		a.report(m)
		a.lastReport = now
		a.samples = nil
	}
}

func (a *Agent) buildMetrics(cpu string, netNow NetBytes, rxSpeed, txSpeed, rxMonthly, txMonthly uint64) Metrics {
	a.mu.RLock()
	probes := a.probes
	a.mu.RUnlock()
	b := a.basic
	return Metrics{
		CPU:          cpu,
		RAMTotal:     uintString(b.MemTotalMB),
		RAMUsed:      uintString(b.MemUsedMB),
		SwapTotal:    uintString(b.SwapTotalMB),
		SwapUsed:     uintString(b.SwapUsedMB),
		DiskTotal:    uintString(b.DiskTotalMB),
		DiskUsed:     uintString(b.DiskUsedMB),
		LoadAvg:      firstNonEmpty(b.LoadAvg, "0 0 0"),
		BootTime:     strconv.FormatInt(b.BootTimeMS, 10),
		NetRX:        uintString(netNow.RX),
		NetTX:        uintString(netNow.TX),
		NetRXMonthly: uintString(rxMonthly),
		NetTXMonthly: uintString(txMonthly),
		NetInSpeed:   uintString(rxSpeed),
		NetOutSpeed:  uintString(txSpeed),
		OS:           firstNonEmpty(b.OSName, runtime.GOOS),
		Arch:         firstNonEmpty(b.Arch, fallbackArch()),
		Kernel:       b.Kernel,
		CPUInfo:      firstNonEmpty(b.CPUInfo, fallbackArch()),
		CPUCores:     intString(b.CPUCores),
		GPUInfo:      b.GPUInfo,
		Processes:    intString(b.Processes),
		TCPConn:      intString(b.TCPConn),
		UDPConn:      intString(b.UDPConn),
		IPv4:         firstNonEmpty(probes.IPv4, "0"),
		IPv6:         firstNonEmpty(probes.IPv6, "0"),
		PingCT:       probeRTTValue(a.cfg.CTNode, probes.CT),
		PingCU:       probeRTTValue(a.cfg.CUNode, probes.CU),
		PingCM:       probeRTTValue(a.cfg.CMNode, probes.CM),
		PingBD:       probeRTTValue(a.cfg.BDNode, probes.BD),
		LossCT:       probeLossValue(a.cfg.CTNode, probes.CT),
		LossCU:       probeLossValue(a.cfg.CUNode, probes.CU),
		LossCM:       probeLossValue(a.cfg.CMNode, probes.CM),
		LossBD:       probeLossValue(a.cfg.BDNode, probes.BD),
	}
}

func probeRTTValue(node string, r ProbeResult) any {
	if strings.TrimSpace(node) == "" {
		return false
	}
	if !r.OK || r.RTTMs < 0 {
		return "null"
	}
	return strconv.Itoa(r.RTTMs)
}

func probeLossValue(node string, r ProbeResult) any {
	if strings.TrimSpace(node) == "" {
		return false
	}
	if r.Loss < 0 {
		return "100"
	}
	return strconv.Itoa(r.Loss)
}

func (a *Agent) report(m Metrics) {
	payload := map[string]any{
		"id":               a.cfg.ServerID,
		"secret":           a.cfg.Secret,
		"metrics":          metricsToMap(m),
		"collect_interval": a.cfg.CollectInterval,
		"report_interval":  a.cfg.ReportInterval,
	}
	if a.cfg.CollectInterval > 0 {
		payload["samples"] = a.samples
	}
	body, err := json.Marshal(payload)
	if err != nil {
		a.log.warnf("marshal payload failed: %v", err)
		return
	}
	gpuSummary, _ := json.Marshal(m.GPUInfo)
	a.log.debugf("metrics summary cpu=%s gpu_info=%s", m.CPU, string(gpuSummary))
	a.log.debugf("report attempt url=%s payload_bytes=%d samples=%d", a.cfg.WorkerURL, len(body), len(a.samples))
	req, err := http.NewRequest(http.MethodPost, a.cfg.WorkerURL, bytes.NewReader(body))
	if err != nil {
		a.log.warnf("create report request failed: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	a.setAgentHeaders(req)
	req.Header.Set("X-Agent-Config-Schema", configSchemaVersion)
	req.Header.Set("X-Agent-Version", a.version)
	req.Header.Set("X-Agent-Config-Md5", firstNonEmpty(a.cfg.ConfigMD5, "none"))

	client := http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		a.log.warnf("report failed: %v", err)
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	respHeaders := resp.Header
	reportHTTPCode := resp.StatusCode
	a.handleReportResponse(reportHTTPCode, respBody, respHeaders)
}

func (a *Agent) handleReportResponse(statusCode int, respBody []byte, headers http.Header) {
	a.log.debugf("report response http=%d body=%s", statusCode, strings.TrimSpace(string(respBody)))
	if statusCode < 200 || statusCode >= 300 {
		return
	}
	if strings.EqualFold(strings.TrimSpace(string(respBody)), "OK") {
		return
	}
	if statusCode == http.StatusOK {
		if err := a.applyRemoteConfig(respBody, headers); err != nil {
			a.log.warnf("dynamic configuration rejected: %v", err)
		}
	}
}

func (a *Agent) networkWorker(ctx context.Context) {
	var lastIP, lastProbe time.Time
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			snap := ProbeSnapshot{}
			needUpdate := false
			if lastIP.IsZero() || now.Sub(lastIP) >= 10*time.Minute {
				snap.IPv4 = getCFTraceIP("tcp4")
				snap.IPv6 = getCFTraceIP("tcp6")
				lastIP = now
				needUpdate = true
			}
			probeInterval := time.Duration(a.cfg.ReportInterval) * time.Second
			if probeInterval < 30*time.Second {
				probeInterval = 30 * time.Second
			}
			if probeInterval > 60*time.Second {
				probeInterval = 60 * time.Second
			}
			if lastProbe.IsZero() || now.Sub(lastProbe) >= probeInterval {
				snap.CT = measureProbe(a.cfg.CTNode, 4, defaultMetricsTCPPort, a.log)
				snap.CU = measureProbe(a.cfg.CUNode, 4, defaultMetricsTCPPort, a.log)
				snap.CM = measureProbe(a.cfg.CMNode, 4, defaultMetricsTCPPort, a.log)
				snap.BD = measureProbe(a.cfg.BDNode, 4, defaultMetricsTCPPort, a.log)
				lastProbe = now
				needUpdate = true
			}
			if needUpdate {
				a.mu.Lock()
				if snap.IPv4 == "" {
					snap.IPv4 = a.probes.IPv4
				}
				if snap.IPv6 == "" {
					snap.IPv6 = a.probes.IPv6
				}
				if snap.CT == (ProbeResult{}) {
					snap.CT = a.probes.CT
				}
				if snap.CU == (ProbeResult{}) {
					snap.CU = a.probes.CU
				}
				if snap.CM == (ProbeResult{}) {
					snap.CM = a.probes.CM
				}
				if snap.BD == (ProbeResult{}) {
					snap.BD = a.probes.BD
				}
				a.probes = snap
				a.mu.Unlock()
			}
		}
	}
}

func getCFTraceIP(network string) string {
	dialer := net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, addr)
		},
	}
	defer transport.CloseIdleConnections()
	client := http.Client{Timeout: 5 * time.Second, Transport: transport}
	resp, err := client.Get("https://cloudflare.com/cdn-cgi/trace")
	if err != nil {
		return "0"
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "ip=") {
			return strings.TrimSpace(strings.TrimPrefix(line, "ip="))
		}
	}
	return "0"
}

var remoteBodyRE = regexp.MustCompile(`^[A-Za-z0-9_=&.,:%+\-\*\?\[\]]*$`)

func (a *Agent) applyRemoteConfig(body []byte, headers http.Header) error {
	if len(body) == 0 {
		return errors.New("empty body")
	}
	if len(body) > 1024 {
		return errors.New("response too large")
	}
	raw := strings.TrimSpace(string(body))
	if raw == "" {
		return errors.New("empty body")
	}
	if !remoteBodyRE.MatchString(raw) {
		return errors.New("invalid body characters")
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return err
	}
	allowed := map[string]bool{
		"collect_interval": true,
		"report_interval":  true,
		"reset_day":        true,
		"schema_version":   true,
		"custom_ct":        true,
		"custom_cu":        true,
		"custom_cm":        true,
		"custom_bd":        true,
		"interface":        true,
		"rx_correction":    true,
		"tx_correction":    true,
		"update":           true,
	}
	for key := range values {
		if !allowed[key] {
			return fmt.Errorf("unknown field %s", key)
		}
	}
	update := values.Get("update")
	hasConfig := values.Has("collect_interval") || values.Has("report_interval") || values.Has("reset_day") || values.Has("schema_version") || values.Has("interface")
	if !hasConfig {
		if update == "1" {
			a.scheduleAgentUpdate()
			return nil
		}
		return errors.New("no config fields")
	}
	newMD5 := strings.ToLower(strings.TrimSpace(headers.Get("X-Agent-Config-Md5")))
	if len(newMD5) != 32 || strings.ContainsFunc(newMD5, func(r rune) bool {
		return !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f')
	}) {
		return errors.New("invalid remote md5")
	}
	collect := parseIntDefault(values.Get("collect_interval"), -1)
	report := parseIntDefault(values.Get("report_interval"), -1)
	reset := parseIntDefault(values.Get("reset_day"), -1)
	if !inIntSet(collect, 0, 1, 2, 5, 10) {
		return fmt.Errorf("invalid collect_interval %d", collect)
	}
	if !inIntSet(report, 30, 60, 120, 180) {
		return fmt.Errorf("invalid report_interval %d", report)
	}
	if reset < 0 || reset > 31 {
		return fmt.Errorf("invalid reset_day %d", reset)
	}
	if values.Get("schema_version") != configSchemaVersion {
		return fmt.Errorf("invalid schema_version %s", values.Get("schema_version"))
	}
	if update != "" && update != "0" && update != "1" {
		return fmt.Errorf("invalid update %s", update)
	}
	if report < collect {
		return errors.New("report_interval less than collect_interval")
	}
	iface, err := normalizeInterfaceList(values.Get("interface"))
	if err != nil {
		return err
	}
	if newMD5 != a.cfg.ConfigMD5 {
		a.cfg.CollectInterval = collect
		a.cfg.ReportInterval = report
		a.cfg.ResetDay = reset
		a.cfg.CTNode = values.Get("custom_ct")
		a.cfg.CUNode = values.Get("custom_cu")
		a.cfg.CMNode = values.Get("custom_cm")
		a.cfg.BDNode = values.Get("custom_bd")
		a.cfg.Interface = iface
		a.cfg.ConfigMD5 = newMD5
		if err := writeConfig(a.paths.ConfigFile, a.cfg); err != nil {
			return err
		}
		a.prevNet = readNetBytes(a.cfg.Interface)
		a.prevTime = time.Now()
		a.samples = nil
		a.lastReport = time.Time{}
		a.log.info("dynamic configuration applied md5=%s interface=%s", newMD5, firstNonEmpty(iface, "auto"))
	}
	if values.Has("rx_correction") || values.Has("tx_correction") {
		rx := values.Get("rx_correction")
		tx := values.Get("tx_correction")
		if err := applyTrafficCorrection(a.paths.TrafficFile, readNetBytes(a.cfg.Interface), a.cfg.Interface, rx, tx); err != nil {
			return err
		}
		_ = a.sendCorrectionConfirm(rx, tx)
	}
	if update == "1" {
		a.scheduleAgentUpdate()
	}
	return nil
}

func inIntSet(v int, allowed ...int) bool {
	for _, item := range allowed {
		if v == item {
			return true
		}
	}
	return false
}

func (a *Agent) sendCorrectionConfirm(rx, tx string) error {
	if _, err := parseTrafficCorrectionGB(rx); err != nil {
		return err
	}
	if _, err := parseTrafficCorrectionGB(tx); err != nil {
		return err
	}
	payload := map[string]any{
		"id":            a.cfg.ServerID,
		"secret":        a.cfg.Secret,
		"rx_correction": parseFloatDefault(rx, 0),
		"tx_correction": parseFloatDefault(tx, 0),
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, a.cfg.WorkerURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	a.setAgentHeaders(req)
	client := http.Client{Timeout: 4 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	statusCode := resp.StatusCode
	if statusCode < 200 || statusCode >= 300 {
		return fmt.Errorf("http %d", statusCode)
	}
	a.log.info("traffic correction confirm sent rx=%sGB tx=%sGB", firstNonEmpty(rx, "0"), firstNonEmpty(tx, "0"))
	return nil
}

func (a *Agent) setAgentHeaders(req *http.Request) {
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", "cfsm")
}

func parseFloatDefault(raw string, def float64) float64 {
	if strings.TrimSpace(raw) == "" {
		return def
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return def
	}
	return v
}

func (a *Agent) scheduleAgentUpdate() {
	if !a.cfg.AutoUpdate {
		a.log.warnf("auto update ignored: local AUTO_UPDATE=0")
		return
	}
	origin, err := workerOrigin(a.cfg.WorkerURL)
	if err != nil {
		a.log.warnf("auto update skipped: %v", err)
		return
	}
	lockFile := filepath.Join(a.paths.ConfigDir, "auto_update.lock")
	now := time.Now().Unix()
	if data, err := os.ReadFile(lockFile); err == nil {
		last := atoi64Default(string(data), 0)
		if now-last < 1800 {
			a.log.warnf("auto update already scheduled recently")
			return
		}
	}
	_ = os.MkdirAll(a.paths.ConfigDir, 0o755)
	_ = os.WriteFile(lockFile, []byte(strconv.FormatInt(now, 10)), 0o600)
	if runtime.GOOS == "windows" {
		scriptURL := origin + "/install.ps1"
		cmd := exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command",
			fmt.Sprintf("Start-Sleep -Seconds %d; iwr -UseBasicParsing %s | iex", int(autoUpdateDelay.Seconds()), quoteShell(scriptURL)))
		if err := cmd.Start(); err != nil {
			a.log.warnf("schedule update failed: %v", err)
			return
		}
		_ = cmd.Process.Release()
		a.log.info("auto update scheduled after %s", autoUpdateDelay)
		return
	}
	scriptURL := origin + "/install.sh"
	cmdLine := fmt.Sprintf("sleep %d; curl -fsSL --connect-timeout 5 -m 30 %s | sh -s install",
		int(autoUpdateDelay.Seconds()), quoteShell(scriptURL))
	cmd := exec.Command("sh", "-c", cmdLine)
	if err := cmd.Start(); err != nil {
		a.log.warnf("schedule update failed: %v", err)
		return
	}
	_ = cmd.Process.Release()
	a.log.info("auto update scheduled after %s", autoUpdateDelay)
}

func workerOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("invalid worker url")
	}
	if u.Host == "" {
		return "", errors.New("invalid worker url host")
	}
	return u.Scheme + "://" + u.Host, nil
}

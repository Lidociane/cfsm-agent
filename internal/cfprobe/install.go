package cfprobe

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

func Install(opts InstallOptions, version string) error {
	paths := defaultPaths(opts.ServiceName, opts.InstallDir)
	printBanner(version)
	if err := requireInstallPermission(); err != nil {
		return err
	}

	existing, existingErr := readConfig(paths.ConfigFile)
	usingExistingConfig := existingErr == nil && (opts.ServerID == "" || opts.Secret == "" || opts.WorkerURL == "")
	if usingExistingConfig {
		fmt.Printf("[INFO] 检测到已有配置，沿用 %s\n", paths.ConfigFile)
		opts.Config = existing
	} else if opts.ServerID == "" || opts.Secret == "" || opts.WorkerURL == "" {
		printUsage(os.Stderr)
		return errors.New("运行所需的 -id/-secret/-url 参数不完整")
	}
	if !usingExistingConfig || opts.ConfigMD5 == "" {
		opts.ConfigMD5 = "none"
	}
	normalizeConfigIntervals(&opts.Config)

	fmt.Printf("[INFO] Platform: %s/%s (%s)\n", runtime.GOOS, runtime.GOARCH, platformName())
	fmt.Printf("[INFO] Install binary: %s\n", paths.BinaryFile)
	fmt.Printf("[INFO] Config file: %s\n", paths.ConfigFile)

	stopService(paths)
	if err := copySelfTo(paths.BinaryFile); err != nil {
		return fmt.Errorf("安装二进制失败: %w", err)
	}
	if err := chmodExecutable(paths.BinaryFile); err != nil {
		return fmt.Errorf("设置可执行权限失败: %w", err)
	}
	if err := migrateTraffic(paths); err != nil {
		return err
	}
	if err := writeConfig(paths.ConfigFile, opts.Config); err != nil {
		return fmt.Errorf("写入配置失败: %w", err)
	}
	if opts.RXCorrectionGB != "" || opts.TXCorrectionGB != "" {
		if err := applyTrafficCorrection(paths.TrafficFile, readNetBytes(opts.Interface), opts.Interface, opts.RXCorrectionGB, opts.TXCorrectionGB); err != nil {
			return err
		}
	}
	if err := writeService(paths, opts.Debug); err != nil {
		return err
	}
	if !opts.NoStart {
		if err := startService(paths, opts.Debug); err != nil {
			return err
		}
	}
	printInstallSummary(paths, opts)
	return nil
}

func Uninstall(serviceName string) error {
	paths := defaultPaths(serviceName, "")
	printBanner("")
	fmt.Println("[INFO] 开始卸载 cf-probe")
	stopService(paths)
	removeService(paths)
	stopDetached(paths.PIDFile)
	_ = os.Remove(paths.BinaryFile)
	_ = os.RemoveAll(paths.ConfigDir)
	_ = os.Remove(paths.LogFile)
	fmt.Println("[INFO] 卸载完成")
	return nil
}

func printBanner(version string) {
	if version == "" {
		version = legacyAgentVersion
	}
	fmt.Println("===========================================")
	fmt.Println("    CF-Server-Monitor Go Probe")
	fmt.Printf("    Version: %s\n", version)
	fmt.Println("===========================================")
}

func migrateTraffic(paths Paths) error {
	if _, err := os.Stat(paths.TrafficFile); err == nil {
		return nil
	}
	if _, err := os.Stat(paths.OldTrafficFile); err != nil {
		if err := os.MkdirAll(paths.ConfigDir, 0o755); err != nil {
			return err
		}
		f, createErr := os.OpenFile(paths.TrafficFile, os.O_CREATE, 0o600)
		if createErr == nil {
			_ = f.Close()
		}
		return createErr
	}
	if err := os.MkdirAll(paths.ConfigDir, 0o755); err != nil {
		return err
	}
	if err := os.Rename(paths.OldTrafficFile, paths.TrafficFile); err != nil {
		return err
	}
	_ = os.RemoveAll(filepath.Dir(paths.OldTrafficFile))
	return nil
}

func printInstallSummary(paths Paths, opts InstallOptions) {
	fmt.Println("")
	fmt.Println("===========================================")
	fmt.Println("    CF-Server-Monitor Go Probe 安装成功")
	fmt.Println("===========================================")
	fmt.Printf("  Service     : %s\n", paths.ServiceName)
	fmt.Printf("  Binary      : %s\n", paths.BinaryFile)
	fmt.Printf("  Config      : %s\n", paths.ConfigFile)
	fmt.Printf("  Server ID   : %s\n", opts.ServerID)
	fmt.Printf("  Secret      : ********\n")
	fmt.Printf("  Worker URL  : %s\n", opts.WorkerURL)
	fmt.Printf("  Report      : %d秒\n", opts.ReportInterval)
	fmt.Printf("  Collect     : %d秒\n", opts.CollectInterval)
	fmt.Printf("  Auto Update : %v\n", opts.AutoUpdate)
	fmt.Printf("  Debug       : %v\n", opts.Debug)
	fmt.Printf("  Interface   : %s\n", firstNonEmpty(opts.Interface, "自动汇总"))
	if opts.ResetDay == 0 {
		fmt.Println("  Reset Day   : 不重置")
	} else {
		fmt.Printf("  Reset Day   : %d号\n", opts.ResetDay)
	}
	fmt.Println("===========================================")
}

func initSystem() string {
	if runtime.GOOS == "windows" {
		return "windows"
	}
	if runtime.GOOS == "darwin" && commandExists("launchctl") {
		return "launchd"
	}
	if isOpenWrt() {
		return "procd"
	}
	if _, err := os.Stat("/etc/alpine-release"); err == nil {
		if commandExists("rc-service") || fileExists("/sbin/openrc-run") {
			return "openrc"
		}
	}
	if commandExists("systemctl") && (fileExists("/run/systemd/system") || commandOutput("ps", "-p", "1", "-o", "comm=") == "systemd") {
		return "systemd"
	}
	if commandExists("rc-service") && fileExists("/etc/init.d") {
		return "openrc"
	}
	if isSynology() {
		return "synology-rc"
	}
	if commandExists("initctl") && fileExists("/etc/init") {
		return "upstart"
	}
	return "background"
}

func writeService(paths Paths, debug bool) error {
	switch initSystem() {
	case "systemd":
		return writeSystemdService(paths, debug)
	case "openrc":
		return writeOpenRCService(paths, debug)
	case "procd":
		return writeProcdService(paths, debug)
	case "launchd":
		return writeLaunchdService(paths, debug)
	case "upstart":
		return writeUpstartService(paths, debug)
	case "synology-rc":
		return writeSynologyRCService(paths, debug)
	case "windows":
		return writeWindowsService(paths, debug)
	case "background":
		return nil
	default:
		return nil
	}
}

func startService(paths Paths, debug bool) error {
	switch initSystem() {
	case "systemd":
		if err := runCommand("systemctl", "daemon-reload"); err != nil {
			return err
		}
		_ = runCommand("systemctl", "enable", paths.ServiceName+".service")
		return runCommand("systemctl", "restart", paths.ServiceName+".service")
	case "openrc":
		_ = runCommand("rc-update", "add", paths.ServiceName, "default")
		return runCommand("rc-service", paths.ServiceName, "restart")
	case "procd":
		_ = runCommand("/etc/init.d/"+paths.ServiceName, "enable")
		return runCommand("/etc/init.d/"+paths.ServiceName, "restart")
	case "launchd":
		plist := launchdPlist(paths)
		domain := "system"
		if runtime.GOOS == "darwin" && !isRootUser() {
			domain = "gui/" + strconv.Itoa(currentUID())
		}
		_ = runCommandQuiet("launchctl", "bootout", domain, plist)
		return runCommand("launchctl", "bootstrap", domain, plist)
	case "upstart":
		_ = runCommand("initctl", "reload-configuration")
		_ = runCommand("initctl", "stop", paths.ServiceName)
		return runCommand("initctl", "start", paths.ServiceName)
	case "synology-rc":
		return runCommand(synologyServiceFile(paths), "restart")
	case "windows":
		return runCommand("schtasks", "/Run", "/TN", paths.ServiceName)
	default:
		debugArg := "-debug=0"
		if debug {
			debugArg = "-debug=1"
		}
		return startDetached(paths.BinaryFile, []string{"run", debugArg}, paths.LogFile, paths.PIDFile)
	}
}

func stopService(paths Paths) {
	switch initSystem() {
	case "systemd":
		_ = runCommand("systemctl", "stop", paths.ServiceName+".service")
		_ = runCommand("systemctl", "disable", paths.ServiceName+".service")
	case "openrc":
		_ = runCommand("rc-service", paths.ServiceName, "stop")
		_ = runCommand("rc-update", "del", paths.ServiceName, "default")
	case "procd":
		_ = runCommand("/etc/init.d/"+paths.ServiceName, "stop")
		_ = runCommand("/etc/init.d/"+paths.ServiceName, "disable")
	case "launchd":
		plist := launchdPlist(paths)
		domain := "system"
		if runtime.GOOS == "darwin" && !isRootUser() {
			domain = "gui/" + strconv.Itoa(currentUID())
		}
		_ = runCommandQuiet("launchctl", "bootout", domain, plist)
	case "upstart":
		_ = runCommand("initctl", "stop", paths.ServiceName)
	case "synology-rc":
		_ = runCommand(synologyServiceFile(paths), "stop")
	case "windows":
		_ = runCommand("schtasks", "/End", "/TN", paths.ServiceName)
	default:
		stopDetached(paths.PIDFile)
	}
}

func removeService(paths Paths) {
	switch initSystem() {
	case "systemd":
		_ = os.Remove(paths.ServiceFile)
		_ = runCommand("systemctl", "daemon-reload")
		_ = runCommand("systemctl", "reset-failed", paths.ServiceName)
	case "openrc", "procd":
		_ = os.Remove("/etc/init.d/" + paths.ServiceName)
	case "launchd":
		_ = os.Remove(paths.LaunchdRootFile)
		_ = os.Remove(paths.LaunchdUserFile)
	case "upstart":
		_ = os.Remove("/etc/init/" + paths.ServiceName + ".conf")
	case "synology-rc":
		_ = os.Remove(synologyServiceFile(paths))
	case "windows":
		_ = runCommand("schtasks", "/Delete", "/TN", paths.ServiceName, "/F")
	}
}

func writeSystemdService(paths Paths, debug bool) error {
	debugArg := "0"
	if debug {
		debugArg = "1"
	}
	content := fmt.Sprintf(`[Unit]
Description=CF Server Monitor Probe Agent
After=network.target network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s run -debug=%s
Restart=always
RestartSec=5
User=root
Group=root
Nice=19
CPUSchedulingPolicy=other
IOSchedulingClass=idle
IOSchedulingPriority=7
StandardOutput=journal
StandardError=journal
SyslogIdentifier=%s

[Install]
WantedBy=multi-user.target
`, paths.BinaryFile, debugArg, paths.ServiceName)
	return writeFileExecutable(paths.ServiceFile, content, 0o644)
}

func writeOpenRCService(paths Paths, debug bool) error {
	return writeFileExecutable("/etc/init.d/"+paths.ServiceName, fmt.Sprintf(`#!/sbin/openrc-run

name="CF Server Monitor Probe Agent"
description="CF Server Monitor Probe Agent"
command="%s"
command_args="run -debug=%s"
pidfile="/run/%s.pid"
retry="SIGTERM/30"
supervisor=supervise-daemon

depend() {
    need net
    after network
}
`, paths.BinaryFile, boolInt(debug), paths.ServiceName), 0o755)
}

func writeProcdService(paths Paths, debug bool) error {
	return writeFileExecutable("/etc/init.d/"+paths.ServiceName, fmt.Sprintf(`#!/bin/sh /etc/rc.common

START=99
STOP=10
USE_PROCD=1

start_service() {
    procd_open_instance
    procd_set_param command %s run -debug=%s
    procd_set_param respawn
    procd_set_param stdout 1
    procd_set_param stderr 1
    procd_close_instance
}

stop_service() {
    killall %s 2>/dev/null
}
`, paths.BinaryFile, boolInt(debug), filepath.Base(paths.BinaryFile)), 0o755)
}

func writeLaunchdService(paths Paths, debug bool) error {
	plist := launchdPlist(paths)
	if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
		return err
	}
	userBlock := ""
	if runtime.GOOS == "darwin" && isRootUser() {
		userBlock = "    <key>UserName</key>\n    <string>root</string>\n"
	}
	content := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%s</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>run</string>
        <string>-debug=%s</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
%s    <key>StandardOutPath</key>
    <string>%s</string>
    <key>StandardErrorPath</key>
    <string>%s</string>
</dict>
</plist>
`, paths.LaunchdLabel, paths.BinaryFile, boolInt(debug), userBlock, paths.LogFile, paths.LogFile)
	return writeFileExecutable(plist, content, 0o644)
}

func writeUpstartService(paths Paths, debug bool) error {
	return writeFileExecutable("/etc/init/"+paths.ServiceName+".conf", fmt.Sprintf(`description "CF Server Monitor Probe Agent"

start on filesystem or runlevel [2345]
stop on runlevel [!2345]
respawn
respawn limit 10 5

script
    exec %s run -debug=%s
end script
`, paths.BinaryFile, boolInt(debug)), 0o644)
}

func writeSynologyRCService(paths Paths, debug bool) error {
	file := synologyServiceFile(paths)
	return writeFileExecutable(file, fmt.Sprintf(`#!/bin/sh

BIN=%s
PID=%s
LOG=%s
ARGS="run -debug=%s"

start() {
    if [ -f "$PID" ] && kill -0 "$(cat "$PID")" 2>/dev/null; then
        echo "%s already running"
        exit 0
    fi
    nohup "$BIN" $ARGS >> "$LOG" 2>&1 &
    echo $! > "$PID"
}

stop() {
    if [ -f "$PID" ]; then
        kill "$(cat "$PID")" 2>/dev/null || true
        rm -f "$PID"
    fi
}

case "$1" in
    start) start ;;
    stop) stop ;;
    restart) stop; sleep 1; start ;;
    *) echo "usage: $0 {start|stop|restart}"; exit 1 ;;
esac
`, paths.BinaryFile, paths.PIDFile, paths.LogFile, boolInt(debug), paths.ServiceName), 0o755)
}

func writeWindowsService(paths Paths, debug bool) error {
	_ = runCommand("schtasks", "/Delete", "/TN", paths.ServiceName, "/F")
	taskRun := strings.Join(windowsServiceArgs(paths, debug), " ")
	if err := runCommand("schtasks", "/Create", "/TN", paths.ServiceName, "/SC", "ONSTART", "/TR", taskRun, "/RL", "HIGHEST", "/RU", "SYSTEM", "/F"); err != nil {
		return fmt.Errorf("%w: %v", errWindowsService, err)
	}
	return nil
}

func writeFileExecutable(path, content string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), mode)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func launchdPlist(paths Paths) string {
	if runtime.GOOS == "darwin" && !isRootUser() {
		return paths.LaunchdUserFile
	}
	return paths.LaunchdRootFile
}

func synologyServiceFile(paths Paths) string {
	return "/usr/local/etc/rc.d/" + paths.ServiceName + ".sh"
}

func boolInt(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

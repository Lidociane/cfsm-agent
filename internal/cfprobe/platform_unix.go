//go:build !windows

package cfprobe

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

func defaultPaths() Paths {
	serviceName := serviceNameDefault
	installDir := "/usr/local/bin"
	if isOpenWrt() {
		installDir = "/usr/bin"
	}
	configDir := "/etc/config/cf-probe"
	pidFile := filepath.Join("/run", serviceName+".pid")
	logFile := filepath.Join("/var/log", serviceName+".log")
	if runtime.GOOS == "darwin" {
		configDir = "/usr/local/etc/cf-probe"
	}
	return Paths{
		ServiceName:     serviceName,
		BinaryFile:      filepath.Join(installDir, serviceName),
		ConfigDir:       configDir,
		ConfigFile:      filepath.Join(configDir, "config.conf"),
		TrafficFile:     filepath.Join(configDir, "traffic.dat"),
		OldTrafficFile:  "/var/lib/cf-probe/traffic.dat",
		PIDFile:         pidFile,
		LogFile:         logFile,
		ServiceFile:     filepath.Join("/etc/systemd/system", serviceName+".service"),
		DebugEnvFile:    filepath.Join("/run", serviceName+"-debug.env"),
		LaunchdLabel:    "com.cfsm." + serviceName,
		LaunchdUserFile: filepath.Join(userHomeDir(), "Library", "LaunchAgents", "com.cfsm."+serviceName+".plist"),
		LaunchdRootFile: filepath.Join("/Library/LaunchDaemons", "com.cfsm."+serviceName+".plist"),
	}
}

func darwinUserPaths(home string) Paths {
	serviceName := serviceNameDefault
	if home == "" {
		home = userHomeDir()
	}
	configDir := filepath.Join(home, ".cf-probe")
	return Paths{
		ServiceName:     serviceName,
		BinaryFile:      filepath.Join(home, ".cf-probe", "bin", serviceName),
		ConfigDir:       configDir,
		ConfigFile:      filepath.Join(configDir, "config.conf"),
		TrafficFile:     filepath.Join(configDir, "traffic.dat"),
		OldTrafficFile:  "/var/lib/cf-probe/traffic.dat",
		PIDFile:         filepath.Join(configDir, serviceName+".pid"),
		LogFile:         filepath.Join(home, "Library", "Logs", serviceName+".log"),
		ServiceFile:     filepath.Join("/etc/systemd/system", serviceName+".service"),
		DebugEnvFile:    filepath.Join("/run", serviceName+"-debug.env"),
		LaunchdLabel:    "com.cfsm." + serviceName,
		LaunchdUserFile: filepath.Join(home, "Library", "LaunchAgents", "com.cfsm."+serviceName+".plist"),
		LaunchdRootFile: filepath.Join("/Library/LaunchDaemons", "com.cfsm."+serviceName+".plist"),
	}
}

func sudoUserHomeDir() string {
	if user := os.Getenv("SUDO_USER"); user != "" && user != "root" {
		if home := darwinAccountHome(user); home != "" {
			return home
		}
		return filepath.Join("/Users", user)
	}
	home := os.Getenv("HOME")
	if home == "" || home == "/var/root" || home == "/" {
		return ""
	}
	return home
}

func darwinAccountHome(user string) string {
	if runtime.GOOS != "darwin" || user == "" {
		return ""
	}
	out := commandOutput("dscl", ".", "-read", "/Users/"+user, "NFSHomeDirectory")
	const prefix = "NFSHomeDirectory:"
	if strings.HasPrefix(out, prefix) {
		return strings.TrimSpace(strings.TrimPrefix(out, prefix))
	}
	return ""
}

func sudoUserUID(home string) int {
	if raw := os.Getenv("SUDO_UID"); raw != "" {
		if uid, err := strconv.Atoi(raw); err == nil && uid > 0 {
			return uid
		}
	}
	if home == "" {
		return -1
	}
	info, err := os.Stat(home)
	if err != nil {
		return -1
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return -1
	}
	return int(stat.Uid)
}

func deepUninstall(paths Paths) []string {
	stopUnixAutostart(paths)
	removeUnixAutostart(paths)
	removeInstalledFiles(paths)
	if runtime.GOOS == "darwin" {
		removeDarwinInstallVariants()
	}
	return unixUninstallResiduals(paths)
}

func stopUnixAutostart(paths Paths) {
	if runtime.GOOS == "darwin" {
		bootoutLaunchd("system", paths.LaunchdRootFile)
		bootoutLaunchdLabel("system", paths.LaunchdLabel)
		stopDetached(paths.PIDFile)
		return
	}
	if commandExists("systemctl") {
		_ = runCommandQuiet("systemctl", "stop", paths.ServiceName+".service")
		_ = runCommandQuiet("systemctl", "disable", paths.ServiceName+".service")
	}
	if commandExists("rc-service") {
		_ = runCommandQuiet("rc-service", paths.ServiceName, "stop")
	}
	if commandExists("rc-update") {
		_ = runCommandQuiet("rc-update", "del", paths.ServiceName, "default")
	}
	initScript := filepath.Join("/etc/init.d", paths.ServiceName)
	if fileExists(initScript) {
		_ = runCommandQuiet(initScript, "stop")
		_ = runCommandQuiet(initScript, "disable")
	}
	if commandExists("initctl") {
		_ = runCommandQuiet("initctl", "stop", paths.ServiceName)
	}
	if fileExists(synologyServiceFile(paths)) {
		_ = runCommandQuiet(synologyServiceFile(paths), "stop")
	}
	stopDetached(paths.PIDFile)
}

func removeUnixAutostart(paths Paths) {
	if runtime.GOOS == "darwin" {
		_ = os.Remove(paths.LaunchdRootFile)
		return
	}
	_ = os.Remove(paths.ServiceFile)
	_ = os.Remove(filepath.Join("/etc/init.d", paths.ServiceName))
	_ = os.Remove(filepath.Join("/etc/init", paths.ServiceName+".conf"))
	_ = os.Remove(synologyServiceFile(paths))
	if commandExists("systemctl") {
		_ = runCommandQuiet("systemctl", "daemon-reload")
		_ = runCommandQuiet("systemctl", "reset-failed", paths.ServiceName)
		_ = runCommandQuiet("systemctl", "reset-failed", paths.ServiceName+".service")
	}
}

func unixUninstallResiduals(paths Paths) []string {
	if runtime.GOOS == "darwin" {
		return darwinUninstallResiduals(paths)
	}
	return existingPaths(
		paths.BinaryFile,
		paths.ConfigDir,
		paths.PIDFile,
		paths.LogFile,
		paths.DebugEnvFile,
		paths.ServiceFile,
		filepath.Join("/etc/init.d", paths.ServiceName),
		filepath.Join("/etc/init", paths.ServiceName+".conf"),
		synologyServiceFile(paths),
	)
}

func removeDarwinInstallVariants() {
	if home := sudoUserHomeDir(); home != "" {
		userPaths := darwinUserPaths(home)
		removeDarwinUserInstall(userPaths, sudoUserUID(home))
	}
}

func removeDarwinUserInstall(paths Paths, uid int) {
	if uid > 0 {
		domain := "gui/" + strconv.Itoa(uid)
		bootoutLaunchd(domain, paths.LaunchdUserFile)
		bootoutLaunchdLabel(domain, paths.LaunchdLabel)
	}
	_ = os.Remove(paths.LaunchdUserFile)
	removeInstalledFiles(paths)
}

func darwinUninstallResiduals(paths Paths) []string {
	var residuals []string
	residuals = append(residuals, existingPaths(
		paths.BinaryFile,
		paths.ConfigDir,
		paths.PIDFile,
		paths.LogFile,
		paths.LaunchdRootFile,
	)...)
	if launchdLabelLoaded("system", paths.LaunchdLabel) {
		residuals = append(residuals, "launchd:"+"/system/"+paths.LaunchdLabel)
	}
	if home := sudoUserHomeDir(); home != "" {
		userPaths := darwinUserPaths(home)
		residuals = append(residuals, existingPaths(
			userPaths.BinaryFile,
			userPaths.ConfigDir,
			userPaths.PIDFile,
			userPaths.LogFile,
			userPaths.LaunchdUserFile,
		)...)
		if uid := sudoUserUID(home); uid > 0 && launchdLabelLoaded("gui/"+strconv.Itoa(uid), userPaths.LaunchdLabel) {
			residuals = append(residuals, "launchd:/gui/"+strconv.Itoa(uid)+"/"+userPaths.LaunchdLabel)
		}
	}
	return uniqueStrings(residuals)
}

func bootoutLaunchd(domain, plist string) {
	if runtime.GOOS != "darwin" || domain == "" || plist == "" || !commandExists("launchctl") {
		return
	}
	_ = runCommandQuiet("launchctl", "bootout", domain, plist)
}

func bootoutLaunchdLabel(domain, label string) {
	if runtime.GOOS != "darwin" || domain == "" || label == "" || !commandExists("launchctl") {
		return
	}
	_ = runCommandQuiet("launchctl", "bootout", domain+"/"+label)
}

func launchdLabelLoaded(domain, label string) bool {
	if runtime.GOOS != "darwin" || domain == "" || label == "" || !commandExists("launchctl") {
		return false
	}
	return runCommandQuiet("launchctl", "print", domain+"/"+label) == nil
}

func requireInstallPermission() error {
	if !isRootUser() {
		return errors.New("请使用 root 权限运行安装: sudo ./cf-probe install ...")
	}
	return nil
}

func requireUninstallPermission() error {
	if !isRootUser() {
		return errors.New("请使用 root 权限运行卸载: sudo ./cf-probe uninstall")
	}
	return nil
}

func copySelfTo(dst string) error {
	src, err := os.Executable()
	if err != nil {
		return err
	}
	src, _ = filepath.EvalSymlinks(src)
	dst, _ = filepath.Abs(dst)
	if src == dst {
		return os.Chmod(dst, 0o755)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	if err = os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func startDetached(binary string, args []string, logFile, pidFile string) error {
	if err := os.MkdirAll(filepath.Dir(logFile), 0o755); err != nil {
		return err
	}
	log, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer log.Close()
	cmd := exec.Command(binary, args...)
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(pidFile), 0o755); err == nil {
		_ = os.WriteFile(pidFile, []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o644)
	}
	return cmd.Process.Release()
}

func stopDetached(pidFile string) {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		_ = os.Remove(pidFile)
		return
	}
	proc, err := os.FindProcess(pid)
	if err == nil {
		_ = proc.Signal(syscall.SIGTERM)
	}
	_ = os.Remove(pidFile)
}

func userHomeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/tmp"
	}
	return home
}

func runCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func runCommandQuiet(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	return cmd.Run()
}

func commandOutput(name string, args ...string) string {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func quoteShell(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func windowsServiceArgs(_ Paths, _ bool) []string {
	return nil
}

var errWindowsService = errors.New("Windows 服务管理仅在 Windows 平台可用")

func chmodExecutable(path string) error {
	return os.Chmod(path, 0o755)
}

func platformName() string {
	if runtime.GOOS == "darwin" {
		return "macOS"
	}
	if isSynology() {
		return "Synology DSM"
	}
	if isOpenWrt() {
		return "OpenWrt"
	}
	if _, err := os.Stat("/etc/alpine-release"); err == nil {
		return "Alpine Linux"
	}
	return runtime.GOOS
}

func isOpenWrt() bool {
	if _, err := os.Stat("/etc/openwrt_release"); err == nil {
		return true
	}
	if _, err := os.Stat("/etc/rc.common"); err == nil && commandExists("uci") {
		return true
	}
	return false
}

func isSynology() bool {
	for _, p := range []string{"/etc.defaults/VERSION", "/etc/VERSION", "/etc.defaults/synoinfo.conf"} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

func executableForRelease(goos, goarch string) string {
	name := fmt.Sprintf("cf-probe-%s-%s", goos, goarch)
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

func isRootUser() bool {
	return os.Geteuid() == 0
}

func currentUID() int {
	return os.Getuid()
}

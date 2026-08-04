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

func defaultPaths(serviceName, installDir string) Paths {
	if serviceName == "" {
		serviceName = serviceNameDefault
	}
	if installDir == "" {
		switch {
		case runtime.GOOS == "darwin" && !isRootUser():
			home, _ := os.UserHomeDir()
			installDir = filepath.Join(home, ".cf-probe", "bin")
		case isOpenWrt():
			installDir = "/usr/bin"
		default:
			installDir = "/usr/local/bin"
		}
	}
	configDir := "/etc/config/cf-probe"
	pidFile := filepath.Join("/run", serviceName+".pid")
	logFile := filepath.Join("/var/log", serviceName+".log")
	if runtime.GOOS == "darwin" {
		if isRootUser() {
			configDir = "/usr/local/etc/cf-probe"
		} else {
			home, _ := os.UserHomeDir()
			configDir = filepath.Join(home, ".cf-probe")
			pidFile = filepath.Join(home, ".cf-probe", serviceName+".pid")
			logFile = filepath.Join(home, "Library", "Logs", serviceName+".log")
		}
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

func requireInstallPermission() error {
	if runtime.GOOS == "darwin" && !isRootUser() {
		return nil
	}
	if !isRootUser() {
		return errors.New("请使用 root 权限运行安装: sudo ./cf-probe install ...")
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

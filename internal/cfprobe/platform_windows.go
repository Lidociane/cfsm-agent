//go:build windows

package cfprobe

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func defaultPaths(serviceName, installDir string) Paths {
	if serviceName == "" {
		serviceName = serviceNameDefault
	}
	programData := os.Getenv("ProgramData")
	if programData == "" {
		programData = `C:\ProgramData`
	}
	if installDir == "" {
		installDir = filepath.Join(os.Getenv("ProgramFiles"), "cf-probe")
		if strings.TrimSpace(installDir) == "cf-probe" {
			installDir = `C:\Program Files\cf-probe`
		}
	}
	binary := filepath.Join(installDir, serviceName+".exe")
	configDir := filepath.Join(programData, "cf-probe")
	return Paths{
		ServiceName:    serviceName,
		BinaryFile:     binary,
		ConfigDir:      configDir,
		ConfigFile:     filepath.Join(configDir, "config.conf"),
		TrafficFile:    filepath.Join(configDir, "traffic.dat"),
		OldTrafficFile: filepath.Join(programData, "cf-probe", "traffic.dat"),
		PIDFile:        filepath.Join(programData, "cf-probe", serviceName+".pid"),
		LogFile:        filepath.Join(programData, "cf-probe", serviceName+".log"),
		ServiceFile:    serviceName,
		DebugEnvFile:   filepath.Join(configDir, "debug.env"),
		LaunchdLabel:   "",
	}
}

func requireInstallPermission() error {
	return nil
}

func copySelfTo(dst string) error {
	src, err := os.Executable()
	if err != nil {
		return err
	}
	src, _ = filepath.EvalSymlinks(src)
	dst, _ = filepath.Abs(dst)
	if strings.EqualFold(src, dst) {
		return nil
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
	return os.Rename(tmp, dst)
}

func startDetached(binary string, args []string, logFile, pidFile string) error {
	if err := os.MkdirAll(filepath.Dir(logFile), 0o755); err != nil {
		return err
	}
	cmd := exec.Command(binary, args...)
	log, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer log.Close()
	cmd.Stdout = log
	cmd.Stderr = log
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = os.WriteFile(pidFile, []byte(fmt.Sprintf("%d\n", cmd.Process.Pid)), 0o644)
	return cmd.Process.Release()
}

func stopDetached(pidFile string) {
	_ = os.Remove(pidFile)
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
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

func windowsServiceArgs(paths Paths, debug bool) []string {
	debugArg := "-debug=0"
	if debug {
		debugArg = "-debug=1"
	}
	return []string{`"` + paths.BinaryFile + `"`, "run", debugArg}
}

func chmodExecutable(_ string) error {
	return nil
}

func platformName() string {
	return "Windows"
}

func isOpenWrt() bool {
	return false
}

func isSynology() bool {
	return false
}

func executableForRelease(goos, goarch string) string {
	name := fmt.Sprintf("cf-probe-%s-%s", goos, goarch)
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

var errWindowsService = errors.New("Windows 计划任务创建失败，请确认以管理员身份运行 PowerShell")

func runtimeGOOS() string {
	return runtime.GOOS
}

func isRootUser() bool {
	return false
}

func currentUID() int {
	return 0
}

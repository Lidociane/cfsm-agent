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

func defaultPaths() Paths {
	serviceName := serviceNameDefault
	programData := os.Getenv("ProgramData")
	if programData == "" {
		programData = `C:\ProgramData`
	}
	installDir := filepath.Join(os.Getenv("ProgramFiles"), "cf-probe")
	if strings.TrimSpace(installDir) == "cf-probe" {
		installDir = `C:\Program Files\cf-probe`
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
		RunUser:        "SYSTEM",
	}
}

func requireInstallPermission(_ Paths) error {
	return nil
}

func requireUninstallPermission(_ Paths) error {
	return nil
}

func checkInstallConflicts(_ Paths) error {
	return nil
}

func stopCurrentUserProbeInstances() {
}

func acquireInstanceLock(_ Paths) (func(), error) {
	return func() {}, nil
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

func deepUninstall(paths Paths) []string {
	_ = runCommandQuiet("schtasks", "/End", "/TN", paths.ServiceName)
	_ = runCommandQuiet("schtasks", "/Delete", "/TN", paths.ServiceName, "/F")
	removeInstalledFiles(paths)
	delayed := removeWindowsBinary(paths)
	return windowsUninstallResiduals(paths, delayed)
}

func removeWindowsBinary(paths Paths) []string {
	var delayed []string
	if removeWindowsFile(paths.BinaryFile) {
		delayed = append(delayed, paths.BinaryFile)
	}
	if removeWindowsFile(paths.BinaryFile + ".tmp") {
		delayed = append(delayed, paths.BinaryFile+".tmp")
	}
	_ = os.Remove(filepath.Dir(paths.BinaryFile))
	return delayed
}

func removeWindowsFile(path string) bool {
	if path == "" {
		return false
	}
	if err := os.Remove(path); err == nil || os.IsNotExist(err) {
		return false
	}
	return scheduleWindowsFileRemoval(path)
}

func scheduleWindowsFileRemoval(path string) bool {
	dir := filepath.Dir(path)
	script := strings.Join([]string{
		"Start-Sleep -Seconds 2",
		"Remove-Item -LiteralPath " + quotePowerShellLiteral(path) + " -Force -ErrorAction SilentlyContinue",
		"Remove-Item -LiteralPath " + quotePowerShellLiteral(dir) + " -Force -ErrorAction SilentlyContinue",
	}, "; ")
	cmd := exec.Command("powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", script)
	if err := cmd.Start(); err == nil {
		_ = cmd.Process.Release()
		return true
	}
	return false
}

func windowsUninstallResiduals(paths Paths, delayed []string) []string {
	delayedSet := map[string]bool{}
	for _, path := range delayed {
		delayedSet[path] = true
	}
	checks := []string{
		paths.BinaryFile,
		paths.ConfigDir,
		paths.PIDFile,
		paths.LogFile,
		paths.DebugEnvFile,
	}
	var residuals []string
	for _, path := range existingPaths(checks...) {
		if !delayedSet[path] && !delayedPathInDir(delayed, path) {
			residuals = append(residuals, path)
		}
	}
	if runCommandQuiet("schtasks", "/Query", "/TN", paths.ServiceName) == nil {
		residuals = append(residuals, "scheduled-task:"+paths.ServiceName)
	}
	return uniqueStrings(residuals)
}

func delayedPathInDir(delayed []string, dir string) bool {
	if dir == "" {
		return false
	}
	cleanDir := filepath.Clean(dir)
	prefix := cleanDir + string(os.PathSeparator)
	for _, path := range delayed {
		cleanPath := filepath.Clean(path)
		if strings.EqualFold(cleanPath, cleanDir) || strings.HasPrefix(strings.ToLower(cleanPath), strings.ToLower(prefix)) {
			return true
		}
	}
	return false
}

func quotePowerShellLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
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
	return []string{"cmd.exe", "/D", "/C", quoteWindowsCmdArg(windowsTaskWrapperFile(paths))}
}

func writeWindowsTaskWrapper(paths Paths, debug bool) error {
	if err := ensureLogFile(paths.LogFile); err != nil {
		return err
	}
	debugArg := "-debug=0"
	if debug {
		debugArg = "-debug=1"
	}
	content := strings.Join([]string{
		"@echo off",
		"setlocal",
		"if not exist " + quoteWindowsCmdArg(paths.ConfigDir) + " mkdir " + quoteWindowsCmdArg(paths.ConfigDir),
		"echo [%DATE% %TIME%] cf-probe task starting >> " + quoteWindowsCmdArg(paths.LogFile),
		quoteWindowsCmdArg(paths.BinaryFile) + " run " + debugArg + " >> " + quoteWindowsCmdArg(paths.LogFile) + " 2>&1",
		"exit /b %ERRORLEVEL%",
		"",
	}, "\r\n")
	return writeFileExecutable(windowsTaskWrapperFile(paths), content, 0o644)
}

func windowsTaskWrapperFile(paths Paths) string {
	return filepath.Join(paths.ConfigDir, paths.ServiceName+".cmd")
}

func quoteWindowsCmdArg(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
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

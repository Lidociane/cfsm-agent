//go:build windows

package cfprobe

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteWindowsTaskWrapperCreatesLogAndRedirectsOutput(t *testing.T) {
	tmp := t.TempDir()
	paths := Paths{
		ServiceName: "cf-probe",
		BinaryFile:  filepath.Join(tmp, "Program Files", "cf-probe", "cf-probe.exe"),
		ConfigDir:   filepath.Join(tmp, "ProgramData", "cf-probe"),
		LogFile:     filepath.Join(tmp, "ProgramData", "cf-probe", "cf-probe.log"),
	}

	if err := writeWindowsTaskWrapper(paths, true); err != nil {
		t.Fatalf("writeWindowsTaskWrapper returned error: %v", err)
	}
	if _, err := os.Stat(paths.LogFile); err != nil {
		t.Fatalf("log file was not created: %v", err)
	}
	data, err := os.ReadFile(windowsTaskWrapperFile(paths))
	if err != nil {
		t.Fatalf("read wrapper: %v", err)
	}
	content := string(data)
	wantRun := quoteWindowsCmdArg(paths.BinaryFile) + " run -debug=1 >> " + quoteWindowsCmdArg(paths.LogFile) + " 2>&1"
	if !strings.Contains(content, wantRun) {
		t.Fatalf("wrapper missing redirected run command %q:\n%s", wantRun, content)
	}
}

func TestWindowsServiceArgsRunsTaskWrapper(t *testing.T) {
	paths := Paths{
		ServiceName: "cf-probe",
		ConfigDir:   `C:\ProgramData\cf-probe`,
	}

	got := strings.Join(windowsServiceArgs(paths, false), " ")
	want := `cmd.exe /D /C "C:\ProgramData\cf-probe\cf-probe.cmd"`
	if got != want {
		t.Fatalf("windowsServiceArgs = %q, want %q", got, want)
	}
}

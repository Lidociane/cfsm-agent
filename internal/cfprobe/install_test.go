package cfprobe

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMergeExplicitInstallConfig(t *testing.T) {
	existing := Config{
		ServerID:       "sid",
		Secret:         "secret",
		WorkerURL:      "https://worker.example.com/report",
		ReportInterval: 120,
		ResetDay:       5,
		AutoUpdate:     false,
	}
	flagConfig := Config{
		ReportInterval: defaultReportIntervalSec,
		ResetDay:       1,
		AutoUpdate:     true,
		UpdateProxy:    "https://gh-proxy.example.com",
	}

	merged := existing
	mergeExplicitInstallConfig(&merged, flagConfig, map[string]bool{"auto_update": true})
	if !merged.AutoUpdate {
		t.Fatal("AutoUpdate = false, want true when -auto_update=1 is explicit")
	}
	if merged.ReportInterval != existing.ReportInterval || merged.ResetDay != existing.ResetDay {
		t.Fatalf("non-explicit fields changed: %+v", merged)
	}
	if merged.UpdateProxy != "" {
		t.Fatalf("UpdateProxy = %q, want preserved empty", merged.UpdateProxy)
	}

	merged = existing
	mergeExplicitInstallConfig(&merged, flagConfig, map[string]bool{"install_ghproxy": true})
	if merged.UpdateProxy != flagConfig.UpdateProxy {
		t.Fatalf("UpdateProxy = %q, want %q", merged.UpdateProxy, flagConfig.UpdateProxy)
	}

	existingAuto := existing
	existingAuto.AutoUpdate = true
	merged = existingAuto
	off := flagConfig
	off.AutoUpdate = false
	mergeExplicitInstallConfig(&merged, off, map[string]bool{"auto_update": true})
	if merged.AutoUpdate {
		t.Fatal("AutoUpdate = true, want false when -auto_update=0 is explicit")
	}
}

func TestWriteSystemdUserServiceUsesUserSafeSettings(t *testing.T) {
	tmp := t.TempDir()
	paths := Paths{
		ServiceName: "cf-probe",
		BinaryFile:  filepath.Join(tmp, "bin", "cf-probe"),
		ConfigDir:   filepath.Join(tmp, ".cf-probe"),
		ConfigFile:  filepath.Join(tmp, ".cf-probe", "config.conf"),
		ServiceFile: filepath.Join(tmp, "cf-probe.service"),
	}

	if err := writeSystemdUserService(paths, false); err != nil {
		t.Fatalf("writeSystemdUserService returned error: %v", err)
	}
	data, err := os.ReadFile(paths.ServiceFile)
	if err != nil {
		t.Fatalf("read service file: %v", err)
	}
	content := string(data)
	wantExec := "ExecStart=" + quoteSystemdExecArg(paths.BinaryFile) + " run -config=" + quoteSystemdExecArg(paths.ConfigFile) + " -debug=0"
	if !strings.Contains(content, wantExec) {
		t.Fatalf("service content missing ExecStart %q:\n%s", wantExec, content)
	}
	for _, disallowed := range []string{
		"WorkingDirectory=",
		"CPUSchedulingPolicy=",
		"IOSchedulingClass=",
		"IOSchedulingPriority=",
	} {
		if strings.Contains(content, disallowed) {
			t.Fatalf("user service should not contain %s:\n%s", disallowed, content)
		}
	}
}

func TestMigrateTrafficMovesLegacyTraffic(t *testing.T) {
	tmp := t.TempDir()
	oldDir := filepath.Join(tmp, "old")
	newDir := filepath.Join(tmp, "new")
	oldTraffic := filepath.Join(oldDir, "traffic.dat")
	newTraffic := filepath.Join(newDir, "traffic.dat")
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldTraffic, []byte("RX_PREV=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := migrateTraffic(Paths{
		ConfigDir:      newDir,
		TrafficFile:    newTraffic,
		OldTrafficFile: oldTraffic,
	})
	if err != nil {
		t.Fatalf("migrateTraffic returned error: %v", err)
	}
	got, err := os.ReadFile(newTraffic)
	if err != nil {
		t.Fatalf("new traffic missing: %v", err)
	}
	if string(got) != "RX_PREV=1\n" {
		t.Fatalf("traffic = %q", got)
	}
	if _, err := os.Stat(oldTraffic); !os.IsNotExist(err) {
		t.Fatalf("old traffic still exists or stat failed: %v", err)
	}
}

func TestMigrateTrafficKeepsExistingTraffic(t *testing.T) {
	tmp := t.TempDir()
	oldDir := filepath.Join(tmp, "old")
	newDir := filepath.Join(tmp, "new")
	oldTraffic := filepath.Join(oldDir, "traffic.dat")
	newTraffic := filepath.Join(newDir, "traffic.dat")
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldTraffic, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newTraffic, []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := migrateTraffic(Paths{ConfigDir: newDir, TrafficFile: newTraffic, OldTrafficFile: oldTraffic}); err != nil {
		t.Fatalf("migrateTraffic returned error: %v", err)
	}
	got, err := os.ReadFile(newTraffic)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new\n" {
		t.Fatalf("traffic = %q", got)
	}
	if _, err := os.Stat(oldTraffic); err != nil {
		t.Fatalf("old traffic should be preserved when current exists: %v", err)
	}
}

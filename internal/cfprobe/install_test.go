package cfprobe

import (
	"os"
	"path/filepath"
	"testing"
)

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

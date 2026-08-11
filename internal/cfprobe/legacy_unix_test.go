//go:build !windows

package cfprobe

import "testing"

func TestParseLegacyShellProcessIDsFiltersAndDeduplicates(t *testing.T) {
	include := func(pid int) bool { return pid == 101 || pid == 103 }

	got := parseLegacyShellProcessIDs("101\nbad\n0\n102\n101\n103\n", include)
	want := []int{101, 103}
	if len(got) != len(want) {
		t.Fatalf("pids = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pids = %#v, want %#v", got, want)
		}
	}
}

func TestParseLegacyShellProcessIDsSkipsOtherNamespaces(t *testing.T) {
	include := func(pid int) bool { return pid != 202 }

	got := parseLegacyShellProcessIDs("201\n202\n203\n", include)
	want := []int{201, 203}
	if len(got) != len(want) {
		t.Fatalf("pids = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pids = %#v, want %#v", got, want)
		}
	}
}

func TestIsProbeRunCommandDetectsLegacyShell(t *testing.T) {
	cmdline := []string{"/bin/sh", "/usr/local/bin/" + legacyShellScriptName}
	if !isProbeRunCommand("/bin/sh", cmdline) {
		t.Fatal("legacy shell command was not detected as a probe instance")
	}
}

func TestIsProbeRunCommandIgnoresInstallerCommand(t *testing.T) {
	cmdline := []string{"/tmp/cf-probe-linux-amd64", "install"}
	if isProbeRunCommand("/tmp/cf-probe-linux-amd64", cmdline) {
		t.Fatal("installer command should not be treated as a running probe instance")
	}
}

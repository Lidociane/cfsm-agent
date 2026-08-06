package cfprobe

import "testing"

func TestManagementCommandsSystemd(t *testing.T) {
	paths := Paths{ServiceName: "cf-probe"}
	cmds := managementCommands(paths, "systemd")
	want := []managementCommand{
		{labelRealtimeLog, "journalctl -u cf-probe -f"},
		{labelStatus, "systemctl status cf-probe"},
		{labelStop, "systemctl stop cf-probe"},
	}
	assertManagementCommands(t, cmds, want)
}

func TestManagementCommandsOpenRC(t *testing.T) {
	paths := Paths{ServiceName: "cf-probe", LogFile: "/var/log/cf-probe.log"}
	cmds := managementCommands(paths, "openrc")
	want := []managementCommand{
		{labelRealtimeLog, "tail -f " + quoteShell("/var/log/cf-probe.log")},
		{labelStatus, "rc-service cf-probe status"},
		{labelStop, "rc-service cf-probe stop"},
	}
	assertManagementCommands(t, cmds, want)
}

func TestManagementCommandsProcd(t *testing.T) {
	paths := Paths{ServiceName: "cf-probe"}
	cmds := managementCommands(paths, "procd")
	want := []managementCommand{
		{labelRealtimeLog, "logread -f -e cf-probe"},
		{labelStatus, "/etc/init.d/cf-probe status"},
		{labelStop, "/etc/init.d/cf-probe stop"},
	}
	assertManagementCommands(t, cmds, want)
}

func assertManagementCommands(t *testing.T, got, want []managementCommand) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("cmd[%d] = %#v, want %#v", i, got[i], want[i])
		}
	}
}

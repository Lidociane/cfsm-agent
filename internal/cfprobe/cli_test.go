package cfprobe

import "testing"

func TestParseUninstallArgsAllowsNoArgs(t *testing.T) {
	if err := parseUninstallArgs(nil); err != nil {
		t.Fatalf("parseUninstallArgs(nil) error = %v", err)
	}
}

func TestParseInstallOptionsRejectsCustomPathAndService(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "install dir underscore", args: []string{"-install_dir=/tmp/cf-probe"}},
		{name: "install dir hyphen", args: []string{"-install-dir=/tmp/cf-probe"}},
		{name: "service name underscore", args: []string{"-service_name=probe-a"}},
		{name: "service name hyphen", args: []string{"-service-name=probe-a"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseInstallOptions(tt.args); err == nil {
				t.Fatalf("parseInstallOptions(%v) expected error", tt.args)
			}
		})
	}
}

func TestParseUninstallArgsRejectsExtraArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "service name underscore", args: []string{"-service_name=probe-a"}},
		{name: "service name hyphen", args: []string{"-service-name=probe-a"}},
		{name: "positional arg", args: []string{"probe-a"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := parseUninstallArgs(tt.args); err == nil {
				t.Fatalf("parseUninstallArgs(%v) expected error", tt.args)
			}
		})
	}
}

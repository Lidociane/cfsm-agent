package cfprobe

import "testing"

func TestCPUUsagePercentFromZeroPrevious(t *testing.T) {
	got, ok := cpuUsagePercent(cpuTimes{}, cpuTimes{Total: 100, Idle: 75})
	if !ok {
		t.Fatal("expected cpuUsagePercent to calculate from zero previous sample")
	}
	if got != 25 {
		t.Fatalf("got %.2f, want 25", got)
	}
}

func TestCPUPercentStringKeepsTinyUsageVisible(t *testing.T) {
	if got := cpuPercentString(0); got != "0.01" {
		t.Fatalf("got %q, want 0.01", got)
	}
}

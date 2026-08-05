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

func TestCPUPercentStringReportsZeroAsZero(t *testing.T) {
	if got := cpuPercentString(0); got != "0.00" {
		t.Fatalf("got %q, want 0.00", got)
	}
}

func TestCPUPercentStringReportsPercentUnits(t *testing.T) {
	if got := cpuPercentString(5); got != "5.00" {
		t.Fatalf("got %q, want 5.00", got)
	}
}

func TestDiskUsageMBFromBlocksUsesFreeBlocksForUsedValue(t *testing.T) {
	total, used, ok := diskUsageMBFromBlocks(100, 65, int64(bytesPerMiB))
	if !ok {
		t.Fatal("expected disk usage calculation to succeed")
	}
	if total != 100 {
		t.Fatalf("total = %d, want 100", total)
	}
	if used != 35 {
		t.Fatalf("used = %d, want 35", used)
	}
}

func TestMemoryUsedMBFromKBUsesMemAvailable(t *testing.T) {
	if got := memoryUsedMBFromKB(8*1024, 3*1024, 0, 0, 0); got != 5 {
		t.Fatalf("got %d, want 5", got)
	}
}

func TestMemoryUsedMBFromKBFallsBackToFreeBuffersCached(t *testing.T) {
	if got := memoryUsedMBFromKB(8*1024, 0, 1*1024, 2*1024, 3*1024); got != 2 {
		t.Fatalf("got %d, want 2", got)
	}
}

func TestMemoryUsedMBFromKBClampsNegativeUsage(t *testing.T) {
	if got := memoryUsedMBFromKB(3*1024, 8*1024, 0, 0, 0); got != 0 {
		t.Fatalf("got %d, want 0", got)
	}
}

func TestSwapUsedMBFromKB(t *testing.T) {
	if got := swapUsedMBFromKB(4*1024, 1*1024); got != 3 {
		t.Fatalf("got %d, want 3", got)
	}
}

func TestSwapUsedMBFromKBClampsNegativeUsage(t *testing.T) {
	if got := swapUsedMBFromKB(1*1024, 4*1024); got != 0 {
		t.Fatalf("got %d, want 0", got)
	}
}

package cfprobe

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSplitProbeTarget(t *testing.T) {
	tests := []struct {
		name     string
		target   string
		wantHost string
		wantPort int
	}{
		{name: "host default", target: "example.com", wantHost: "example.com", wantPort: defaultMetricsTCPPort},
		{name: "host port", target: "example.com:8443", wantHost: "example.com", wantPort: 8443},
		{name: "ipv6 bracket", target: "[2001:db8::1]:443", wantHost: "2001:db8::1", wantPort: 443},
		{name: "ipv6 default", target: "2001:db8::1", wantHost: "2001:db8::1", wantPort: defaultMetricsTCPPort},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, port, err := splitProbeTarget(tt.target, defaultMetricsTCPPort)
			if err != nil {
				t.Fatalf("splitProbeTarget returned error: %v", err)
			}
			if host != tt.wantHost || port != tt.wantPort {
				t.Fatalf("got %s:%d, want %s:%d", host, port, tt.wantHost, tt.wantPort)
			}
		})
	}
}

func TestWorkerOrigin(t *testing.T) {
	got, err := workerOrigin("https://worker.example.com/report?id=1")
	if err != nil {
		t.Fatalf("workerOrigin returned error: %v", err)
	}
	if got != "https://worker.example.com" {
		t.Fatalf("unexpected origin: %s", got)
	}
}

func TestRetryMeasuredPingAcceptsLowLatency(t *testing.T) {
	calls := 0
	got, err := retryMeasuredPing("tcp", func() (int, error) {
		calls++
		return 42, nil
	})
	if err != nil {
		t.Fatalf("retryMeasuredPing returned error: %v", err)
	}
	if got != 42 || calls != 1 {
		t.Fatalf("got latency=%d calls=%d, want latency=42 calls=1", got, calls)
	}
}

func TestRetryMeasuredPingRejectsSuspiciousTCPRetransmission(t *testing.T) {
	values := []int{1200, 100}
	calls := 0
	got, err := retryMeasuredPing("tcp", func() (int, error) {
		v := values[calls]
		calls++
		return v, nil
	})
	if err == nil || !strings.Contains(err.Error(), "suspicious retransmission") {
		t.Fatalf("error = %v, want suspicious retransmission", err)
	}
	if got != -1 || calls != 2 {
		t.Fatalf("got latency=%d calls=%d, want latency=-1 calls=2", got, calls)
	}
}

func TestRetryMeasuredPingAcceptsRecoveredHTTP(t *testing.T) {
	values := []int{1200, 100}
	calls := 0
	got, err := retryMeasuredPing("http", func() (int, error) {
		v := values[calls]
		calls++
		return v, nil
	})
	if err != nil {
		t.Fatalf("retryMeasuredPing returned error: %v", err)
	}
	if got != 100 || calls != 2 {
		t.Fatalf("got latency=%d calls=%d, want latency=100 calls=2", got, calls)
	}
}

func TestRetryMeasuredPingRejectsPersistentHighLatency(t *testing.T) {
	calls := 0
	got, err := retryMeasuredPing("icmp", func() (int, error) {
		calls++
		return 1200, nil
	})
	if err == nil || !strings.Contains(err.Error(), "latency remains high") {
		t.Fatalf("error = %v, want latency remains high", err)
	}
	if got != -1 || calls != 4 {
		t.Fatalf("got latency=%d calls=%d, want latency=-1 calls=4", got, calls)
	}
}

func TestRetryMeasuredPingStopsOnRetryError(t *testing.T) {
	calls := 0
	got, err := retryMeasuredPing("tcp", func() (int, error) {
		calls++
		if calls == 2 {
			return -1, errors.New("dial failed")
		}
		return 1200, nil
	})
	if err == nil || err.Error() != "dial failed" {
		t.Fatalf("error = %v, want dial failed", err)
	}
	if got != -1 || calls != 2 {
		t.Fatalf("got latency=%d calls=%d, want latency=-1 calls=2", got, calls)
	}
}

func TestBuildProbeResultCalculatesLossFromFailedSamples(t *testing.T) {
	got := buildProbeResult(4, []int{40, 10, 20})
	if !got.OK {
		t.Fatal("expected probe result to be OK")
	}
	if got.RTTMs != 20 {
		t.Fatalf("RTTMs = %d, want 20", got.RTTMs)
	}
	if got.Loss != 25 {
		t.Fatalf("Loss = %d, want 25", got.Loss)
	}
}

func TestBuildProbeResultAllSamplesLost(t *testing.T) {
	got := buildProbeResult(4, nil)
	if got.OK {
		t.Fatal("expected probe result to be failed")
	}
	if got.RTTMs != -1 {
		t.Fatalf("RTTMs = %d, want -1", got.RTTMs)
	}
	if got.Loss != 100 {
		t.Fatalf("Loss = %d, want 100", got.Loss)
	}
}

func TestRollingProbeHistoryAggregatesOneMinuteWindow(t *testing.T) {
	now := time.Unix(1000, 0)
	history := rollingProbeHistory{}
	results := []ProbeResult{
		{RTTMs: 10, Loss: 0, OK: true},
		{RTTMs: 20, Loss: 0, OK: true},
		{RTTMs: -1, Loss: 100, OK: false},
		{RTTMs: 30, Loss: 0, OK: true},
		{RTTMs: 100, Loss: 0, OK: true},
		{RTTMs: -1, Loss: 100, OK: false},
	}
	for i, result := range results {
		history.add(now.Add(time.Duration(i)*metricsProbeInterval), "example.com", result)
	}

	got := history.snapshot(now.Add(5 * metricsProbeInterval))
	if !got.OK {
		t.Fatal("expected rolling probe result to be OK")
	}
	if got.RTTMs != 25 {
		t.Fatalf("RTTMs = %d, want 25", got.RTTMs)
	}
	if got.Loss != 33 {
		t.Fatalf("Loss = %d, want 33", got.Loss)
	}
}

func TestRollingProbeHistoryKeepsSixSamples(t *testing.T) {
	now := time.Unix(1000, 0)
	history := rollingProbeHistory{}
	history.add(now, "example.com", ProbeResult{RTTMs: -1, Loss: 100, OK: false})
	for i := 1; i <= 6; i++ {
		history.add(now.Add(time.Duration(i)*metricsProbeInterval), "example.com", ProbeResult{RTTMs: i * 10, OK: true})
	}

	got := history.snapshot(now.Add(6 * metricsProbeInterval))
	if got.Loss != 0 {
		t.Fatalf("Loss = %d, want 0", got.Loss)
	}
	if len(history.samples) != metricsProbeWindowSampleCount {
		t.Fatalf("samples = %d, want %d", len(history.samples), metricsProbeWindowSampleCount)
	}
}

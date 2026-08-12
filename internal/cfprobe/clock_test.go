package cfprobe

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestServerCalibrationUsesRTTMidpointAndMonotonicAge(t *testing.T) {
	anchor := time.Now()
	serverTime := anchor.UnixMilli() + 500
	calibration := newServerCalibration(serverTime, 80*time.Millisecond, anchor)
	snapshot := calibration.snapshot(anchor.Add(20 * time.Millisecond))

	if snapshot.AccurateTS == nil || *snapshot.AccurateTS != serverTime+60 {
		t.Fatalf("AccurateTS = %v, want %d", snapshot.AccurateTS, serverTime+60)
	}
	if snapshot.OffsetMS == nil || *snapshot.OffsetMS != 540 {
		t.Fatalf("OffsetMS = %v, want 540", snapshot.OffsetMS)
	}
	if snapshot.Source == nil || *snapshot.Source != "server" {
		t.Fatalf("Source = %v, want server", snapshot.Source)
	}
	if snapshot.RoundTripMS == nil || *snapshot.RoundTripMS != 80 {
		t.Fatalf("RoundTripMS = %v, want 80", snapshot.RoundTripMS)
	}
	if snapshot.SampleAgeMS == nil || *snapshot.SampleAgeMS != 20 {
		t.Fatalf("SampleAgeMS = %v, want 20", snapshot.SampleAgeMS)
	}
}

func TestNTPTimestampRoundTripPreservesUnixMilliseconds(t *testing.T) {
	const unixMilliseconds = int64(1_754_300_060_123)
	encoded := unixMillisecondsToNTPTimestamp(unixMilliseconds)
	decoded, err := ntpTimestampToUnixNanoseconds(encoded[:])
	if err != nil {
		t.Fatalf("ntpTimestampToUnixNanoseconds() error = %v", err)
	}
	difference := decoded/nanosecondsPerMilli - unixMilliseconds
	if difference < -1 || difference > 1 {
		t.Fatalf("decoded difference = %dms, want within 1ms", difference)
	}
}

func TestNTPClientUsesServerTimestamps(t *testing.T) {
	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP() error = %v", err)
	}
	defer server.Close()

	serverErr := make(chan error, 1)
	go func() {
		request := make([]byte, 48)
		_, peer, err := server.ReadFromUDP(request)
		if err != nil {
			serverErr <- err
			return
		}
		accurate := time.Now().UnixMilli() + 250
		stamp := unixMillisecondsToNTPTimestamp(accurate)
		response := make([]byte, 48)
		response[0] = 0x24 // leap=0, version=4, mode=4 (server)
		response[1] = 1
		copy(response[24:32], request[40:48])
		copy(response[32:40], stamp[:])
		copy(response[40:48], stamp[:])
		_, err = server.WriteToUDP(response, peer)
		serverErr <- err
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	sample, err := queryNTPAddress(ctx, "local-test", server.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatalf("queryNTPAddress() error = %v", err)
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("test NTP server error = %v", err)
	}
	offsetMilliseconds := sample.offsetNanos / nanosecondsPerMilli
	if offsetMilliseconds < 200 || offsetMilliseconds > 300 {
		t.Fatalf("offset = %dms, want 200..300ms", offsetMilliseconds)
	}
	if sample.server != "local-test" {
		t.Fatalf("server = %q, want local-test", sample.server)
	}
}

func TestSelectMedianNTPSampleRejectsOutlierAndBreaksTieByRTT(t *testing.T) {
	samples := []ntpSample{
		{server: "low-outlier", offsetNanos: -1_000 * nanosecondsPerMilli, roundTripMS: 1},
		{server: "near-slow", offsetNanos: 10 * nanosecondsPerMilli, roundTripMS: 20},
		{server: "near-fast", offsetNanos: 12 * nanosecondsPerMilli, roundTripMS: 5},
		{server: "high-outlier", offsetNanos: 1_000 * nanosecondsPerMilli, roundTripMS: 1},
	}
	selected := selectMedianNTPSample(samples)
	if selected.server != "near-fast" {
		t.Fatalf("selected server = %q, want near-fast", selected.server)
	}
}

func TestCalibratedClockPrefersFreshNTPAndFallsBackToServer(t *testing.T) {
	now := time.Now()
	clock := calibratedClock{
		ntp: &clockCalibration{
			source:           "ntp:test",
			anchor:           now,
			accurateAtAnchor: now.UnixMilli() + 200,
		},
		server: &clockCalibration{
			source:           "server",
			anchor:           now,
			accurateAtAnchor: now.UnixMilli() + 100,
		},
	}
	snapshot := clock.snapshot(now.Add(time.Second))
	if snapshot.Source == nil || *snapshot.Source != "ntp:test" {
		t.Fatalf("fresh Source = %v, want ntp:test", snapshot.Source)
	}

	clock.ntp.anchor = now.Add(-maxCalibrationAge - time.Second)
	snapshot = clock.snapshot(now.Add(time.Second))
	if snapshot.Source == nil || *snapshot.Source != "server" {
		t.Fatalf("fallback Source = %v, want server", snapshot.Source)
	}
}

func TestCalibratedClockCorrectsSamplesCollectedBeforeCalibration(t *testing.T) {
	anchor := time.Now()
	clock := calibratedClock{
		ntp: &clockCalibration{
			source:           "ntp:test",
			anchor:           anchor,
			accurateAtAnchor: 1_000_000,
		},
	}

	timestamp, ok := clock.timestamp(anchor.Add(-3 * time.Second))
	if !ok {
		t.Fatal("retroactive timestamp was not calibrated")
	}
	if timestamp != 997_000 {
		t.Fatalf("timestamp = %d, want 997000", timestamp)
	}

	if timestamp, ok := clock.timestamp(anchor.Add(-maxCalibrationAge - time.Millisecond)); ok {
		t.Fatalf("expired retroactive timestamp was calibrated as %d", timestamp)
	}
}

func TestSamplesAndBootTimeUseCalibratedTimeline(t *testing.T) {
	anchor := time.Now()
	offset := int64(500)
	agent := Agent{
		clock: calibratedClock{
			ntp: &clockCalibration{
				source:           "ntp:test",
				anchor:           anchor,
				accurateAtAnchor: anchor.UnixMilli() + offset,
			},
		},
		samples: []metricSample{{
			at:      anchor.Add(-3 * time.Second),
			metrics: map[string]any{"cpu": "1.00"},
		}},
	}

	samples := agent.samplesForReport()
	wantSampleTime := anchor.UnixMilli() + offset - 3_000
	if got := samples[0]["ts"]; got != wantSampleTime {
		t.Fatalf("samples[0].ts = %v, want %d", got, wantSampleTime)
	}

	localBootTime := anchor.UnixMilli() - int64(time.Hour/time.Millisecond)
	metrics := agent.metricsForReport(Metrics{BootTime: strconv.FormatInt(localBootTime, 10)}, anchor)
	wantBootTime := strconv.FormatInt(localBootTime+offset, 10)
	if got := metrics["boot_time"]; got != wantBootTime {
		t.Fatalf("metrics.boot_time = %v, want %s", got, wantBootTime)
	}
}

func TestNTPRefreshIsRateLimited(t *testing.T) {
	now := time.Now()
	clock := calibratedClock{}
	if !clock.beginNTPRefresh(now) {
		t.Fatal("first NTP refresh was not allowed")
	}
	if clock.beginNTPRefresh(now.Add(ntpRefreshInterval - time.Millisecond)) {
		t.Fatal("NTP refresh inside interval was allowed")
	}
	if !clock.beginNTPRefresh(now.Add(ntpRefreshInterval)) {
		t.Fatal("NTP refresh at interval boundary was not allowed")
	}
}

func TestUncalibratedClockSerializesNullCalibrationFields(t *testing.T) {
	snapshot := (&calibratedClock{}).snapshot(time.UnixMilli(1234))
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	want := `{"local_ts":1234,"accurate_ts":null,"offset_ms":null,"source":null,"round_trip_ms":null,"sample_age_ms":null}`
	if string(encoded) != want {
		t.Fatalf("encoded snapshot = %s, want %s", encoded, want)
	}
}

func TestResponseServerTimeFormats(t *testing.T) {
	const timestamp = int64(1_754_300_060_123)
	tests := []struct {
		name    string
		body    string
		headers http.Header
	}{
		{name: "form body", body: "server_time=1754300060123"},
		{name: "form with config", body: "server_time=1754300060123&report_interval=60"},
		{name: "json body", body: `{"server_time":1754300060123}`},
		{name: "header", body: "OK", headers: http.Header{serverTimeHeader: []string{"1754300060123"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := responseServerTime([]byte(test.body), test.headers)
			if !ok || got != timestamp {
				t.Fatalf("responseServerTime() = (%d, %v), want (%d, true)", got, ok, timestamp)
			}
		})
	}
}

func TestHandleReportResponseCalibratesServerTimeWithoutConfig(t *testing.T) {
	agent := Agent{log: newLogger(false)}
	started := time.Now()
	completed := started.Add(80 * time.Millisecond)
	agent.handleTimedReportResponse(
		http.StatusOK,
		[]byte("server_time=1754300060123"),
		http.Header{},
		started,
		completed,
	)
	snapshot := agent.clock.snapshot(completed)
	if snapshot.Source == nil || *snapshot.Source != "server" {
		t.Fatalf("Source = %v, want server", snapshot.Source)
	}
	if snapshot.AccurateTS == nil || *snapshot.AccurateTS != 1_754_300_060_163 {
		t.Fatalf("AccurateTS = %v, want 1754300060163", snapshot.AccurateTS)
	}
}

func TestServerTimeCoexistsWithURLConfigResponse(t *testing.T) {
	tmp := t.TempDir()
	configFile := tmp + "/config.conf"
	agent := Agent{
		cfg: Config{
			ServerID:       "sid",
			Secret:         "secret",
			WorkerURL:      "https://worker.example.com/report",
			ReportInterval: 60,
			ResetDay:       1,
			ConfigMD5:      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		paths: Paths{ConfigFile: configFile, TrafficFile: tmp + "/traffic.dat"},
		log:   newLogger(false),
	}
	headers := http.Header{"X-Agent-Config-Md5": []string{"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}
	body := []byte("server_time=1754300060123&collect_interval=0&report_interval=60&reset_day=1&schema_version=3&interface=")
	started := time.Now()
	agent.handleTimedReportResponse(http.StatusOK, body, headers, started, started.Add(20*time.Millisecond))

	if agent.cfg.ConfigMD5 != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("ConfigMD5 = %q", agent.cfg.ConfigMD5)
	}
	snapshot := agent.clock.snapshot(started.Add(20 * time.Millisecond))
	if snapshot.Source == nil || *snapshot.Source != "server" {
		t.Fatalf("Source = %v, want server", snapshot.Source)
	}
}

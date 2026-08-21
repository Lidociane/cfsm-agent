package cfprobe

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestReportWebSocketURLConvertsHTTPUpdateURL(t *testing.T) {
	tests := map[string]string{
		"https://example.com/update":        "wss://example.com/update",
		"http://example.com/update?token=1": "ws://example.com/update?token=1",
		"wss://example.com/update":          "wss://example.com/update",
	}
	for raw, want := range tests {
		got, err := reportWebSocketURL(raw)
		if err != nil {
			t.Fatalf("reportWebSocketURL(%q) returned error: %v", raw, err)
		}
		if got != want {
			t.Fatalf("reportWebSocketURL(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestReportWebSocketURLWithConfigAddsQueryParameters(t *testing.T) {
	got, err := reportWebSocketURLWithConfig(
		"https://example.com/update?token=1",
		configSchemaVersion,
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	)
	if err != nil {
		t.Fatalf("reportWebSocketURLWithConfig returned error: %v", err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("result is not a URL: %v", err)
	}
	if u.Scheme != "wss" {
		t.Fatalf("scheme = %q, want wss", u.Scheme)
	}
	values := u.Query()
	if values.Get("token") != "1" {
		t.Fatalf("token query = %q, want 1", values.Get("token"))
	}
	if values.Get("config_schema") != configSchemaVersion {
		t.Fatalf("config_schema = %q, want %q", values.Get("config_schema"), configSchemaVersion)
	}
	if values.Get("config_md5") != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("config_md5 = %q", values.Get("config_md5"))
	}
}

func TestWSSEffectiveCollectIntervalUsesNormalizedConfig(t *testing.T) {
	agent := Agent{
		cfg: Config{
			ReportInterval:  30,
			CollectInterval: 2,
		},
	}
	if got := agent.effectiveCollectInterval(); got != 2*time.Second {
		t.Fatalf("effectiveCollectInterval collect=2 = %s, want 2s", got)
	}
	if got := agent.tickInterval(); got != 2*time.Second {
		t.Fatalf("tickInterval collect=2 = %s, want 2s", got)
	}

	agent.cfg.CollectInterval = 10
	if got := agent.effectiveCollectInterval(); got != 10*time.Second {
		t.Fatalf("effectiveCollectInterval collect=10 = %s, want 10s", got)
	}

	agent.cfg.CollectInterval = 1
	if got := agent.effectiveCollectInterval(); got != time.Second {
		t.Fatalf("effectiveCollectInterval collect=1 = %s, want 1s", got)
	}
	if got := agent.tickInterval(); got != time.Second {
		t.Fatalf("tickInterval collect=1 = %s, want 1s", got)
	}
}

func TestWSSReportIntervalDefaultsToTwoSecondsBeforeAck(t *testing.T) {
	agent := Agent{cfg: Config{ReportInterval: 60}, reporter: &reportTransport{}}
	if got := agent.currentWSSReportInterval(); got != 2*time.Second {
		t.Fatalf("currentWSSReportInterval() = %s, want 2s", got)
	}
}

func TestHTTPCollectIntervalZeroDoesNotEnableSamples(t *testing.T) {
	agent := Agent{
		cfg: Config{
			ReportInterval:  30,
			CollectInterval: 0,
			ConnectionMode:  connectionModeHTTP,
		},
	}
	if got := agent.effectiveCollectInterval(); got != 0 {
		t.Fatalf("effectiveCollectInterval http collect=0 = %s, want 0", got)
	}
	if got := agent.tickInterval(); got != 30*time.Second {
		t.Fatalf("tickInterval http collect=0 = %s, want 30s", got)
	}
}

func TestBuildReportBodyKeepsLegacyPOSTShape(t *testing.T) {
	agent := Agent{
		cfg: Config{
			ServerID:       "sid",
			Secret:         "secret",
			WorkerURL:      "https://worker.example.com/update",
			ReportInterval: 60,
			ResetDay:       1,
			ConfigMD5:      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		log: newLogger(false),
	}
	reportAt := time.Now()
	body, _, err := agent.buildReportBody(Metrics{CPU: "1.23", BootTime: "0"}, reportAt)
	if err != nil {
		t.Fatalf("buildReportBody returned error: %v", err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	for _, key := range []string{"id", "secret", "metrics", "collect_interval", "report_interval"} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("payload missing legacy key %q: %s", key, body)
		}
	}
	if _, ok := payload["samples"]; ok {
		t.Fatalf("payload unexpectedly includes samples without buffered samples: %s", body)
	}
	var configSchema string
	if err := json.Unmarshal(payload["config_schema"], &configSchema); err != nil {
		t.Fatalf("config_schema is not a string: %v", err)
	}
	if configSchema != configSchemaVersion {
		t.Fatalf("config_schema = %q, want %q", configSchema, configSchemaVersion)
	}
	var configMD5 string
	if err := json.Unmarshal(payload["config_md5"], &configMD5); err != nil {
		t.Fatalf("config_md5 is not a string: %v", err)
	}
	if configMD5 != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("config_md5 = %q", configMD5)
	}
	body, _, err = agent.buildReportBody(Metrics{CPU: "1.23", BootTime: "0"}, reportAt.Add(10*time.Second))
	if err != nil {
		t.Fatalf("second buildReportBody returned error: %v", err)
	}
	var secondPayload map[string]json.RawMessage
	if err := json.Unmarshal(body, &secondPayload); err != nil {
		t.Fatalf("second payload is not JSON: %v", err)
	}
	if _, ok := secondPayload["config_schema"]; ok {
		t.Fatalf("config_schema should be omitted before config report interval: %s", body)
	}
	if _, ok := secondPayload["config_md5"]; ok {
		t.Fatalf("config_md5 should be omitted before config report interval: %s", body)
	}
	body, _, err = agent.buildReportBody(Metrics{CPU: "1.23", BootTime: "0"}, reportAt.Add(configStateReportInterval))
	if err != nil {
		t.Fatalf("third buildReportBody returned error: %v", err)
	}
	var thirdPayload map[string]json.RawMessage
	if err := json.Unmarshal(body, &thirdPayload); err != nil {
		t.Fatalf("third payload is not JSON: %v", err)
	}
	if _, ok := thirdPayload["config_schema"]; !ok {
		t.Fatalf("config_schema missing after config report interval: %s", body)
	}
	agent.cfg.ConfigMD5 = "cccccccccccccccccccccccccccccccc"
	body, _, err = agent.buildReportBody(Metrics{CPU: "1.23", BootTime: "0"}, reportAt.Add(configStateReportInterval+10*time.Second))
	if err != nil {
		t.Fatalf("fourth buildReportBody returned error: %v", err)
	}
	var fourthPayload map[string]json.RawMessage
	if err := json.Unmarshal(body, &fourthPayload); err != nil {
		t.Fatalf("fourth payload is not JSON: %v", err)
	}
	if _, ok := fourthPayload["config_md5"]; !ok {
		t.Fatalf("config_md5 missing after local md5 changed: %s", body)
	}

	if _, ok := payload["type"]; ok {
		t.Fatalf("payload unexpectedly uses wrapper type field: %s", body)
	}
	if _, ok := payload["payload"]; ok {
		t.Fatalf("payload unexpectedly uses wrapper payload field: %s", body)
	}
}

func TestBuildReportBodyIncludesBufferedSamplesWhenCollectIntervalZero(t *testing.T) {
	reportAt := time.Now()
	agent := Agent{
		cfg: Config{
			ServerID:        "sid",
			Secret:          "secret",
			WorkerURL:       "https://worker.example.com/update",
			ReportInterval:  30,
			CollectInterval: 0,
			ResetDay:        1,
			ConfigMD5:       "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		log: newLogger(false),
		samples: []metricSample{{
			at:      reportAt,
			metrics: map[string]any{"cpu": "1.23"},
		}},
	}
	body, sampleCount, err := agent.buildReportBody(Metrics{CPU: "1.23", BootTime: "0"}, reportAt)
	if err != nil {
		t.Fatalf("buildReportBody returned error: %v", err)
	}
	if sampleCount != 1 {
		t.Fatalf("sampleCount = %d, want 1", sampleCount)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	var collectInterval int
	if err := json.Unmarshal(payload["collect_interval"], &collectInterval); err != nil {
		t.Fatalf("collect_interval is not a number: %v", err)
	}
	if collectInterval != 0 {
		t.Fatalf("collect_interval = %d, want 0", collectInterval)
	}
	var samples []map[string]any
	if err := json.Unmarshal(payload["samples"], &samples); err != nil {
		t.Fatalf("samples is not an array: %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("samples length = %d, want 1", len(samples))
	}
}

func TestDialReportWebSocketUsesGETUpgradeAndSendsLegacyJSON(t *testing.T) {
	serverErr := make(chan error, 1)
	received := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			serverErr <- fmt.Errorf("method = %s, want GET", r.Method)
			return
		}
		if !headerHasToken(r.Header, "Connection", "Upgrade") || !headerHasToken(r.Header, "Upgrade", "websocket") {
			serverErr <- fmt.Errorf("missing websocket upgrade headers")
			return
		}
		key := r.Header.Get("Sec-WebSocket-Key")
		if key == "" {
			serverErr <- fmt.Errorf("missing Sec-WebSocket-Key")
			return
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			serverErr <- fmt.Errorf("response writer cannot hijack")
			return
		}
		conn, rw, err := hijacker.Hijack()
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()
		_, _ = fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", expectedWebSocketAccept(key))
		if err := rw.Flush(); err != nil {
			serverErr <- err
			return
		}
		ws := &webSocketConn{conn: conn, reader: rw.Reader}
		if err := ws.writeFrame(wsOpcodeText, []byte(`{"type":"hello","ts":1,"protocol":"update"}`), time.Second); err != nil {
			serverErr <- err
			return
		}
		payload, opcode, err := ws.ReadDataMessage()
		if err != nil {
			serverErr <- err
			return
		}
		if opcode != wsOpcodeText {
			serverErr <- fmt.Errorf("opcode = %d, want text", opcode)
			return
		}
		received <- payload
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, _, _, err := dialReportWebSocket(
		t.Context(),
		wsURL,
		http.Header{"X-Agent-Version": []string{"test"}},
		false,
		2*time.Second,
	)
	if err != nil {
		t.Fatalf("dialReportWebSocket returned error: %v", err)
	}
	defer conn.Close()
	transport := &reportTransport{agent: &Agent{log: newLogger(false)}, wsURL: wsURL}
	if _, err := transport.waitHello(conn); err != nil {
		t.Fatalf("waitHello returned error: %v", err)
	}
	body := []byte(`{"id":"sid","secret":"secret","metrics":{"cpu":"1.00"}}`)
	if err := conn.WriteText(body, time.Second); err != nil {
		t.Fatalf("WriteText returned error: %v", err)
	}

	select {
	case err := <-serverErr:
		t.Fatal(err)
	case got := <-received:
		if string(got) != string(body) {
			t.Fatalf("server received %s, want %s", got, body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for server to receive websocket payload")
	}
}

func TestReportTransportProtocolDelayPausesPostFallback(t *testing.T) {
	transport := &reportTransport{agent: &Agent{log: newLogger(false)}}
	transport.delayProtocol("server error code=401")
	if transport.postFallbackAllowed() {
		t.Fatal("POST fallback allowed during protocol delay")
	}
	delay, reason := transport.pauseDelay()
	if reason != "server error code=401" {
		t.Fatalf("pause reason = %q", reason)
	}
	if delay < 119*time.Second {
		t.Fatalf("pause delay = %s, want at least 119s", delay)
	}
}

func TestAgentWSSRuntimeHeadersTemporarilyDisableAndRestoreWSS(t *testing.T) {
	agent := Agent{
		cfg: Config{
			ReportInterval:  60,
			ConnectionMode:  connectionModeAuto,
			CollectInterval: 2,
		},
		log:  newLogger(false),
		wake: make(chan struct{}, 1),
	}

	agent.handleWSSRuntimeHeaders(http.Header{
		"X-Agent-Wss-Mode":   []string{"inactive"},
		"X-Agent-Wss-Reason": []string{"wss_schedule_inactive"},
	})
	if agent.usesWSS() {
		t.Fatal("usesWSS() = true after inactive schedule header")
	}
	if agent.cfg.ConnectionMode != connectionModeAuto {
		t.Fatalf("ConnectionMode = %q, want auto", agent.cfg.ConnectionMode)
	}
	if got := agent.currentWSSReportInterval(); got != time.Minute {
		t.Fatalf("currentWSSReportInterval() = %s, want 1m while runtime disabled", got)
	}
	select {
	case <-agent.wake:
	default:
		t.Fatal("inactive schedule header did not wake agent loop")
	}

	agent.handleWSSRuntimeHeaders(http.Header{
		"X-Agent-Wss-Mode":   []string{"inactive"},
		"X-Agent-Wss-Reason": []string{"wss_schedule_inactive"},
	})
	select {
	case <-agent.wake:
		t.Fatal("duplicate inactive schedule header woke agent loop")
	default:
	}

	agent.handleWSSRuntimeHeaders(http.Header{
		"X-Agent-Wss-Mode":   []string{"active"},
		"X-Agent-Wss-Reason": []string{"wss_schedule_active"},
	})
	if !agent.usesWSS() {
		t.Fatal("usesWSS() = false after active schedule header")
	}
}

func TestWSSScheduleInactiveHandshakeIsSoftReject(t *testing.T) {
	reason, ok := wssScheduleInactiveFromHandshake(&wsHandshakeError{
		StatusCode: http.StatusConflict,
		Body:       `{"text":"wss_schedule_inactive","connection_mode":"http"}`,
	})
	if !ok || reason != agentWSSScheduleInactive {
		t.Fatalf("wssScheduleInactiveFromHandshake() = %q, %v", reason, ok)
	}

	_, ok = wssScheduleInactiveFromHandshake(&wsHandshakeError{
		StatusCode: http.StatusForbidden,
		Body:       `{"error":"Forbidden","code":403}`,
	})
	if ok {
		t.Fatal("403 must not be treated as schedule inactive")
	}
}

func TestReportTransportUsesServerSuggestedWSSReportInterval(t *testing.T) {
	agent := Agent{
		cfg: Config{ReportInterval: 60},
		log: newLogger(false),
	}
	transport := &reportTransport{agent: &agent}
	agent.reporter = transport

	if got := agent.currentWSSReportInterval(); got != 2*time.Second {
		t.Fatalf("currentWSSReportInterval before ack = %s, want 2s", got)
	}

	next := int64(60000)
	transport.setNextReportAfterMs(next)
	if got := agent.currentWSSReportInterval(); got != time.Minute {
		t.Fatalf("currentWSSReportInterval after ack = %s, want 1m", got)
	}

	transport.setNextReportAfterMs(1)
	if got := agent.currentWSSReportInterval(); got != time.Second {
		t.Fatalf("currentWSSReportInterval after small ack = %s, want 1s", got)
	}

	transport.setNextReportAfterMs(int64((10 * time.Minute) / time.Millisecond))
	if got := agent.currentWSSReportInterval(); got != wssDynamicMaxInterval {
		t.Fatalf("currentWSSReportInterval after large ack = %s, want %s", got, wssDynamicMaxInterval)
	}

	transport.resetReportInterval()
	if got := agent.currentWSSReportInterval(); got != 2*time.Second {
		t.Fatalf("currentWSSReportInterval after reset = %s, want 2s", got)
	}
}

func TestReportTransportSuggestedIntervalWakesAgentLoop(t *testing.T) {
	agent := Agent{
		cfg:  Config{ReportInterval: 60},
		log:  newLogger(false),
		wake: make(chan struct{}, 1),
	}
	transport := &reportTransport{agent: &agent}

	transport.setNextReportAfterMs(int64((60 * time.Second) / time.Millisecond))
	select {
	case <-agent.wake:
	default:
		t.Fatal("setNextReportAfterMs did not wake agent loop")
	}

	transport.setNextReportAfterMs(int64((2 * time.Second) / time.Millisecond))
	select {
	case <-agent.wake:
	default:
		t.Fatal("shorter setNextReportAfterMs did not wake agent loop")
	}
}

func TestHTTPConnectionModeUsesReportInterval(t *testing.T) {
	agent := Agent{
		cfg: Config{
			ReportInterval: 60,
			ConnectionMode: connectionModeHTTP,
		},
	}
	if got := agent.currentWSSReportInterval(); got != time.Minute {
		t.Fatalf("currentWSSReportInterval in http mode = %s, want 1m", got)
	}
	if got := agent.tickInterval(); got != time.Minute {
		t.Fatalf("tickInterval in http mode = %s, want 1m", got)
	}
}

func TestWSSConfigBodySupportsPayloadObject(t *testing.T) {
	body, headers, ok := wssConfigBodyAndHeaders(wsServerFrame{
		Type: "config",
		Payload: map[string]any{
			"collect_interval":    float64(0),
			"report_interval":     float64(60),
			"wss_report_interval": float64(2),
			"reset_day":           float64(1),
			"schema_version":      configSchemaVersion,
			"interface":           "",
			"connection_mode":     "auto",
			"configMd5":           "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		},
	})
	if !ok {
		t.Fatal("wssConfigBodyAndHeaders returned ok=false")
	}
	values, err := url.ParseQuery(string(body))
	if err != nil {
		t.Fatalf("config body is not query string: %v", err)
	}
	if values.Get("report_interval") != "60" {
		t.Fatalf("report_interval = %q, want 60", values.Get("report_interval"))
	}
	if values.Get("wss_report_interval") != "2" {
		t.Fatalf("wss_report_interval = %q, want 2", values.Get("wss_report_interval"))
	}
	if headers.Get("X-Agent-Config-Md5") != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("X-Agent-Config-Md5 = %q", headers.Get("X-Agent-Config-Md5"))
	}
}

func TestWSSConfigBodySupportsConfigBodyAndConfigObject(t *testing.T) {
	body, headers, ok := wssConfigBodyAndHeaders(wsServerFrame{
		Type:       "ack",
		ConfigBody: "collect_interval=0&report_interval=60&reset_day=1&schema_version=5&interface=&connection_mode=auto",
		ConfigMD5:  "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	})
	if !ok {
		t.Fatal("config_body frame returned ok=false")
	}
	if string(body) != "collect_interval=0&report_interval=60&reset_day=1&schema_version=5&interface=&connection_mode=auto" {
		t.Fatalf("body = %q", string(body))
	}
	if headers.Get("X-Agent-Config-Md5") != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("X-Agent-Config-Md5 = %q", headers.Get("X-Agent-Config-Md5"))
	}

	body, headers, ok = wssConfigBodyAndHeaders(wsServerFrame{
		Type: "ack",
		Config: map[string]any{
			"collect_interval": float64(0),
			"report_interval":  float64(120),
			"reset_day":        float64(1),
			"schema_version":   configSchemaVersion,
			"interface":        "",
			"connection_mode":  "auto",
			"config_md5":       "cccccccccccccccccccccccccccccccc",
		},
	})
	if !ok {
		t.Fatal("config object frame returned ok=false")
	}
	values, err := url.ParseQuery(string(body))
	if err != nil {
		t.Fatalf("config object body is not query string: %v", err)
	}
	if values.Get("report_interval") != "120" {
		t.Fatalf("report_interval = %q, want 120", values.Get("report_interval"))
	}
	if headers.Get("X-Agent-Config-Md5") != "cccccccccccccccccccccccccccccccc" {
		t.Fatalf("X-Agent-Config-Md5 = %q", headers.Get("X-Agent-Config-Md5"))
	}
}

func TestReportTransportWSSConfigAppliesRemoteConfig(t *testing.T) {
	tmp := t.TempDir()
	configFile := tmp + "/config.conf"
	agent := Agent{
		cfg: Config{
			ServerID:       "sid",
			Secret:         "secret",
			WorkerURL:      "https://worker.example.com/update",
			ReportInterval: 60,
			ResetDay:       1,
			ConfigMD5:      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		paths: Paths{ConfigFile: configFile, TrafficFile: tmp + "/traffic.dat"},
		log:   newLogger(false),
		wake:  make(chan struct{}, 1),
	}
	transport := reportTransport{agent: &agent}
	transport.handleConfigFrame(wsServerFrame{
		Type:      "config",
		Config:    "collect_interval=10&report_interval=120&wss_report_interval=4&reset_day=1&schema_version=5&interface=&connection_mode=auto",
		ConfigMD5: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	})

	if agent.cfg.ReportInterval != 120 {
		t.Fatalf("ReportInterval = %d, want 120", agent.cfg.ReportInterval)
	}
	if agent.cfg.CollectInterval != 4 {
		t.Fatalf("CollectInterval = %d, want 4", agent.cfg.CollectInterval)
	}
	if got := agent.effectiveCollectInterval(); got != 4*time.Second {
		t.Fatalf("effectiveCollectInterval = %s, want 4s", got)
	}
	if agent.cfg.ConfigMD5 != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("ConfigMD5 = %q", agent.cfg.ConfigMD5)
	}
	persisted, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatalf("read persisted config: %v", err)
	}
	if strings.Contains(string(persisted), "WSS_REPORT_INTERVAL") {
		t.Fatal("WSS_REPORT_INTERVAL should not be persisted in agent config")
	}
	select {
	case <-agent.wake:
	default:
		t.Fatal("WSS config apply did not wake agent loop")
	}
}

func TestReportTransportWSSConfigCanSwitchToHTTP(t *testing.T) {
	tmp := t.TempDir()
	configFile := tmp + "/config.conf"
	agent := Agent{
		cfg: Config{
			ServerID:       "sid",
			Secret:         "secret",
			WorkerURL:      "https://worker.example.com/update",
			ReportInterval: 60,
			ResetDay:       1,
			ConnectionMode: connectionModeAuto,
			ConfigMD5:      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		paths: Paths{ConfigFile: configFile, TrafficFile: tmp + "/traffic.dat"},
		log:   newLogger(false),
	}
	transport := reportTransport{agent: &agent}
	agent.reporter = &transport
	transport.handleConfigFrame(wsServerFrame{
		Type:      "config",
		Config:    "collect_interval=0&report_interval=60&reset_day=1&schema_version=5&interface=&connection_mode=http",
		ConfigMD5: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	})

	if agent.cfg.ConnectionMode != connectionModeHTTP {
		t.Fatalf("ConnectionMode = %q, want %q", agent.cfg.ConnectionMode, connectionModeHTTP)
	}
	if got := agent.currentWSSReportInterval(); got != time.Minute {
		t.Fatalf("currentWSSReportInterval after http config = %s, want 1m", got)
	}
	persisted, err := readConfig(configFile)
	if err != nil {
		t.Fatalf("readConfig returned error: %v", err)
	}
	if persisted.ConnectionMode != connectionModeHTTP {
		t.Fatalf("persisted ConnectionMode = %q, want %q", persisted.ConnectionMode, connectionModeHTTP)
	}
}

func TestReportTransportWSSConfigAllowsMissingMD5(t *testing.T) {
	tmp := t.TempDir()
	agent := Agent{
		cfg: Config{
			ServerID:       "sid",
			Secret:         "secret",
			WorkerURL:      "https://worker.example.com/update",
			ReportInterval: 60,
			ResetDay:       1,
			ConfigMD5:      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		paths: Paths{ConfigFile: tmp + "/config.conf", TrafficFile: tmp + "/traffic.dat"},
		log:   newLogger(false),
	}
	transport := reportTransport{agent: &agent}
	transport.handleConfigFrame(wsServerFrame{
		Type:   "config",
		Config: "collect_interval=0&report_interval=120&reset_day=1&schema_version=5&interface=&connection_mode=auto",
	})

	if agent.cfg.ReportInterval != 120 {
		t.Fatalf("ReportInterval = %d, want 120", agent.cfg.ReportInterval)
	}
	if agent.cfg.ConfigMD5 != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("ConfigMD5 changed to %q without remote md5", agent.cfg.ConfigMD5)
	}
}

func TestReportTransportWSSConfigThrottlesToOneMinute(t *testing.T) {
	tmp := t.TempDir()
	agent := Agent{
		cfg: Config{
			ServerID:       "sid",
			Secret:         "secret",
			WorkerURL:      "https://worker.example.com/update",
			ReportInterval: 60,
			ResetDay:       1,
			ConfigMD5:      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		paths: Paths{ConfigFile: tmp + "/config.conf", TrafficFile: tmp + "/traffic.dat"},
		log:   newLogger(false),
	}
	transport := reportTransport{agent: &agent}
	transport.handleConfigFrame(wsServerFrame{
		Type:   "config",
		Config: "collect_interval=0&report_interval=120&reset_day=1&schema_version=5&interface=&connection_mode=auto",
	})
	transport.handleConfigFrame(wsServerFrame{
		Type:   "config",
		Config: "collect_interval=0&report_interval=180&reset_day=1&schema_version=5&interface=&connection_mode=auto",
	})
	if agent.cfg.ReportInterval != 120 {
		t.Fatalf("ReportInterval after throttled config = %d, want 120", agent.cfg.ReportInterval)
	}

	transport.mu.Lock()
	transport.lastConfigAt = time.Now().Add(-wssConfigMinInterval)
	transport.mu.Unlock()
	transport.handleConfigFrame(wsServerFrame{
		Type:   "config",
		Config: "collect_interval=0&report_interval=180&reset_day=1&schema_version=5&interface=&connection_mode=auto",
	})
	if agent.cfg.ReportInterval != 180 {
		t.Fatalf("ReportInterval after throttle window = %d, want 180", agent.cfg.ReportInterval)
	}
}

func TestNextWSSBackoffStartsAtSixtySeconds(t *testing.T) {
	if got := nextWSSBackoff(0); got != 60*time.Second {
		t.Fatalf("nextWSSBackoff(0) = %s, want 60s", got)
	}
	if got := nextWSSBackoff(60 * time.Second); got != 120*time.Second {
		t.Fatalf("nextWSSBackoff(60s) = %s, want 120s", got)
	}
}

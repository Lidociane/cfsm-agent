package cfprobe

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

func TestWSSReportIntervalRoundsUpReportIntervalByTwenty(t *testing.T) {
	if got := wssReportInterval(60); got != 3*time.Second {
		t.Fatalf("wssReportInterval(60) = %s, want 3s", got)
	}
	if got := wssReportInterval(30); got != 2*time.Second {
		t.Fatalf("wssReportInterval(30) = %s, want 2s", got)
	}
	if got := wssReportInterval(21); got != 2*time.Second {
		t.Fatalf("wssReportInterval(21) = %s, want 2s", got)
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
		},
		log: newLogger(false),
	}
	body, _, err := agent.buildReportBody(Metrics{CPU: "1.23", BootTime: "0"}, time.Now())
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
	if _, ok := payload["type"]; ok {
		t.Fatalf("payload unexpectedly uses wrapper type field: %s", body)
	}
	if _, ok := payload["payload"]; ok {
		t.Fatalf("payload unexpectedly uses wrapper payload field: %s", body)
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

func TestNextWSSBackoffStartsAtSixtySeconds(t *testing.T) {
	if got := nextWSSBackoff(0); got != 60*time.Second {
		t.Fatalf("nextWSSBackoff(0) = %s, want 60s", got)
	}
	if got := nextWSSBackoff(60 * time.Second); got != 120*time.Second {
		t.Fatalf("nextWSSBackoff(60s) = %s, want 120s", got)
	}
}

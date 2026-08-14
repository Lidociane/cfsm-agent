package cfprobe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	wssProtocolRetryDelay = 120 * time.Second
	wssNetworkMinRetry    = 60 * time.Second
	wssNetworkMaxRetry    = 5 * time.Minute
	wssHandshakeTimeout   = 10 * time.Second
	wssHelloTimeout       = 10 * time.Second
	wssWriteTimeout       = 8 * time.Second
)

type reportTransport struct {
	agent *Agent
	wsURL string

	mu               sync.Mutex
	conn             *webSocketConn
	pauseUntil       time.Time
	pauseReason      string
	lastPostDelayLog time.Time
}

type wsProtocolDelayError struct {
	reason string
}

func (e *wsProtocolDelayError) Error() string {
	return e.reason
}

type wsHelloFrame struct {
	Type     string `json:"type"`
	TS       int64  `json:"ts"`
	Protocol string `json:"protocol"`
}

type wsServerFrame struct {
	Type               string `json:"type"`
	TS                 int64  `json:"ts"`
	Persisted          *bool  `json:"persisted"`
	NextD1WriteAfterMs *int64 `json:"nextD1WriteAfterMs"`
	Error              string `json:"error"`
	Code               int    `json:"code"`
}

func newReportTransport(a *Agent) *reportTransport {
	wsURL, err := reportWebSocketURL(a.cfg.WorkerURL)
	if err != nil {
		a.log.info("WSS retry delayed reason=invalid_url error=%v", err)
	}
	return &reportTransport{
		agent: a,
		wsURL: wsURL,
	}
}

func reportWebSocketURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	if u.Host == "" {
		return "", fmt.Errorf("missing host in %q", raw)
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	case "wss", "ws":
	default:
		return "", fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	return u.String(), nil
}

func wssReportInterval(reportIntervalSec int) time.Duration {
	if reportIntervalSec < 1 {
		reportIntervalSec = defaultReportIntervalSec
	}
	seconds := (reportIntervalSec + 19) / 20
	return time.Duration(seconds) * time.Second
}

func (r *reportTransport) run(ctx context.Context) {
	if r == nil || r.wsURL == "" {
		return
	}
	backoff := wssNetworkMinRetry
	for {
		if !r.waitProtocolPause(ctx) {
			return
		}
		conn, headers, started, received, err := r.dial(ctx)
		if err != nil {
			if handshakeErr := (*wsHandshakeError)(nil); errors.As(err, &handshakeErr) && isAuthConfigHTTPStatus(handshakeErr.StatusCode) {
				r.delayProtocol(fmt.Sprintf("WSS handshake http=%d", handshakeErr.StatusCode))
				backoff = wssNetworkMinRetry
				continue
			}
			if !r.waitNetworkRetry(ctx, err, backoff) {
				return
			}
			backoff = nextWSSBackoff(backoff)
			continue
		}
		backoff = wssNetworkMinRetry
		r.calibrateHandshakeDate(headers, started, received)

		hello, err := r.waitHello(conn)
		if err != nil {
			_ = conn.Close()
			if r.handleConnectionError(ctx, err, &backoff) {
				continue
			}
			return
		}
		r.setConn(conn)
		r.agent.log.info("WSS connected url=%s protocol=%s ts=%d", r.wsURL, hello.Protocol, hello.TS)

		err = r.readLoop(ctx, conn)
		r.clearConn(conn)
		_ = conn.Close()
		if r.handleConnectionError(ctx, err, &backoff) {
			continue
		}
		return
	}
}

func (r *reportTransport) dial(ctx context.Context) (*webSocketConn, http.Header, time.Time, time.Time, error) {
	headers := http.Header{}
	headers.Set("Accept", "*/*")
	headers.Set("User-Agent", "cfsm")
	headers.Set("X-Agent-Config-Schema", configSchemaVersion)
	headers.Set("X-Agent-Version", r.agent.version)
	headers.Set("X-Agent-Config-Md5", firstNonEmpty(r.agent.cfg.ConfigMD5, "none"))
	return dialReportWebSocket(ctx, r.wsURL, headers, usePublicDNSResolver(r.agent.cfg), wssHandshakeTimeout)
}

func (r *reportTransport) waitHello(conn *webSocketConn) (wsHelloFrame, error) {
	_ = conn.SetReadDeadline(time.Now().Add(wssHelloTimeout))
	defer conn.SetReadDeadline(time.Time{})

	payload, opcode, err := conn.ReadDataMessage()
	if err != nil {
		return wsHelloFrame{}, err
	}
	if opcode != wsOpcodeText {
		return wsHelloFrame{}, fmt.Errorf("WSS hello invalid opcode=%d", opcode)
	}
	var hello wsHelloFrame
	if err := json.Unmarshal(payload, &hello); err != nil {
		return wsHelloFrame{}, fmt.Errorf("WSS hello invalid json: %w", err)
	}
	if hello.Type != "hello" || hello.Protocol != "update" {
		return wsHelloFrame{}, fmt.Errorf("WSS hello invalid type=%q protocol=%q", hello.Type, hello.Protocol)
	}
	return hello, nil
}

func (r *reportTransport) readLoop(ctx context.Context, conn *webSocketConn) error {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	defer close(done)

	for {
		payload, opcode, err := conn.ReadDataMessage()
		if err != nil {
			return err
		}
		if opcode != wsOpcodeText {
			r.agent.log.debugf("WSS message ignored opcode=%d bytes=%d", opcode, len(payload))
			continue
		}
		var frame wsServerFrame
		if err := json.Unmarshal(payload, &frame); err != nil {
			r.agent.log.debugf("WSS message ignored invalid_json=%v", err)
			continue
		}
		switch frame.Type {
		case "ack":
			persisted := false
			if frame.Persisted != nil {
				persisted = *frame.Persisted
			}
			nextD1 := int64(0)
			if frame.NextD1WriteAfterMs != nil {
				nextD1 = *frame.NextD1WriteAfterMs
			}
			r.agent.log.debugf("WSS ack ts=%d persisted=%v nextD1WriteAfterMs=%d", frame.TS, persisted, nextD1)
		case "error":
			reason := firstNonEmpty(frame.Error, "server_error")
			r.agent.log.info("WSS error ts=%d code=%d error=%s", frame.TS, frame.Code, reason)
			return &wsProtocolDelayError{reason: fmt.Sprintf("server error code=%d error=%s", frame.Code, reason)}
		case "hello":
			r.agent.log.debugf("WSS hello repeated ts=%d", frame.TS)
		default:
			r.agent.log.debugf("WSS message ignored type=%q", frame.Type)
		}
	}
}

func (r *reportTransport) connected() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.conn != nil && time.Now().After(r.pauseUntil)
}

func (r *reportTransport) url() string {
	if r == nil {
		return ""
	}
	return r.wsURL
}

func (r *reportTransport) send(body []byte) bool {
	conn := r.currentConn()
	if conn == nil {
		return false
	}
	if err := conn.WriteText(body, wssWriteTimeout); err != nil {
		r.clearConn(conn)
		_ = conn.Close()
		return false
	}
	return true
}

func (r *reportTransport) postFallbackAllowed() bool {
	delay, _ := r.pauseDelay()
	return delay <= 0
}

func (r *reportTransport) logPostFallbackDelayed() {
	r.mu.Lock()
	delay := time.Until(r.pauseUntil)
	reason := r.pauseReason
	now := time.Now()
	if delay <= 0 || now.Sub(r.lastPostDelayLog) < 30*time.Second {
		r.mu.Unlock()
		return
	}
	r.lastPostDelayLog = now
	r.mu.Unlock()
	r.agent.log.info("POST fallback delayed reason=%s remaining=%s", reason, delay.Round(time.Second))
}

func (r *reportTransport) delayProtocol(reason string) {
	if reason == "" {
		reason = "protocol_error"
	}
	r.mu.Lock()
	r.pauseUntil = time.Now().Add(wssProtocolRetryDelay)
	r.pauseReason = reason
	conn := r.conn
	r.conn = nil
	r.lastPostDelayLog = time.Now()
	r.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	r.agent.log.info("WSS retry delayed reason=%s delay=%s", reason, wssProtocolRetryDelay)
	r.agent.log.info("POST fallback delayed reason=%s delay=%s", reason, wssProtocolRetryDelay)
}

func (r *reportTransport) currentConn() *webSocketConn {
	r.mu.Lock()
	defer r.mu.Unlock()
	if time.Now().Before(r.pauseUntil) {
		return nil
	}
	return r.conn
}

func (r *reportTransport) setConn(conn *webSocketConn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.conn = conn
}

func (r *reportTransport) clearConn(conn *webSocketConn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.conn == conn {
		r.conn = nil
	}
}

func (r *reportTransport) pauseDelay() (time.Duration, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delay := time.Until(r.pauseUntil)
	if delay <= 0 {
		return 0, ""
	}
	return delay, r.pauseReason
}

func (r *reportTransport) waitProtocolPause(ctx context.Context) bool {
	for {
		delay, _ := r.pauseDelay()
		if delay <= 0 {
			return true
		}
		if !sleepContext(ctx, delay) {
			return false
		}
	}
}

func (r *reportTransport) waitNetworkRetry(ctx context.Context, err error, delay time.Duration) bool {
	if delay < wssNetworkMinRetry {
		delay = wssNetworkMinRetry
	}
	r.agent.log.info("WSS retry delayed reason=%v delay=%s", err, delay)
	return sleepContext(ctx, delay)
}

func (r *reportTransport) handleConnectionError(ctx context.Context, err error, backoff *time.Duration) bool {
	if err == nil {
		return !isContextDone(ctx)
	}
	if isContextDone(ctx) {
		return false
	}
	var delayErr *wsProtocolDelayError
	if errors.As(err, &delayErr) {
		r.delayProtocol(delayErr.reason)
		*backoff = wssNetworkMinRetry
		return true
	}
	var closeErr *wsCloseError
	if errors.As(err, &closeErr) && closeErr.Code == 1008 {
		reason := fmt.Sprintf("WSS close code=1008 reason=%s", closeErr.Reason)
		r.agent.log.info("WSS error %s", reason)
		r.delayProtocol(reason)
		*backoff = wssNetworkMinRetry
		return true
	}
	delay := *backoff
	if !r.waitNetworkRetry(ctx, err, delay) {
		return false
	}
	*backoff = nextWSSBackoff(delay)
	return true
}

func (r *reportTransport) calibrateHandshakeDate(headers http.Header, started, received time.Time) {
	if dateTime, ok := responseDateTime(headers); ok {
		snapshot, updated := r.agent.clock.updateDate(dateTime, received.Sub(started), received)
		if updated {
			r.agent.log.debugf("Date header time calibrated offset_ms=%d round_trip_ms=%d",
				valueOrZero(snapshot.OffsetMS), valueOrZero(snapshot.RoundTripMS))
		} else {
			r.agent.log.debugf("Date header time calibration skipped offset_ms=%d threshold_ms=%d",
				valueOrZero(snapshot.OffsetMS), int64(dateCalibrationThreshold/time.Millisecond))
		}
	}
}

func isAuthConfigHTTPStatus(statusCode int) bool {
	return statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden || statusCode == http.StatusNotFound
}

func nextWSSBackoff(current time.Duration) time.Duration {
	if current < wssNetworkMinRetry {
		return wssNetworkMinRetry
	}
	next := current * 2
	if next > wssNetworkMaxRetry {
		return wssNetworkMaxRetry
	}
	return next
}

func durationGCD(a, b time.Duration) time.Duration {
	if a < 0 {
		a = -a
	}
	if b < 0 {
		b = -b
	}
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func isContextDone(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	default:
		return false
	}
}

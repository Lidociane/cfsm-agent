package cfprobe

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	ntpQueryTimeout      = 2 * time.Second
	ntpRefreshInterval   = 10 * time.Minute
	maxCalibrationAge    = 24 * time.Hour
	ntpUnixEpochSeconds  = int64(2_208_988_800)
	nanosecondsPerSecond = int64(time.Second)
	nanosecondsPerMilli  = int64(time.Millisecond)
	serverTimeHeader     = "X-Server-Time"
)

var ntpServerHosts = []string{
	"time.cloudflare.com",
	"time.google.com",
	"time.nist.gov",
	"ntp.aliyun.com",
}

// ReportTime describes the local wall clock and the latest independent clock
// calibration. Pointer fields intentionally serialize as null before the first
// successful calibration.
type ReportTime struct {
	LocalTS     int64   `json:"local_ts"`
	AccurateTS  *int64  `json:"accurate_ts"`
	OffsetMS    *int64  `json:"offset_ms"`
	Source      *string `json:"source"`
	RoundTripMS *uint64 `json:"round_trip_ms"`
	SampleAgeMS *uint64 `json:"sample_age_ms"`
}

type calibratedClock struct {
	mu             sync.Mutex
	ntp            *clockCalibration
	server         *clockCalibration
	lastNTPAttempt time.Time
}

type clockCalibration struct {
	source           string
	anchor           time.Time
	accurateAtAnchor int64
	roundTripMS      uint64
}

type ntpSample struct {
	server           string
	anchor           time.Time
	accurateAtAnchor int64
	roundTripMS      uint64
	offsetNanos      int64
}

func newServerCalibration(serverTime int64, roundTrip time.Duration, anchor time.Time) clockCalibration {
	roundTripMS := durationMilliseconds(roundTrip)
	// The Worker stamps server_time close to response transmission. With one
	// server timestamp, half of the request RTT is the best available estimate
	// of the response's remaining travel time.
	halfRoundTrip := roundTripMS/2 + roundTripMS%2
	return clockCalibration{
		source:           "server",
		anchor:           anchor,
		accurateAtAnchor: addMilliseconds(serverTime, halfRoundTrip),
		roundTripMS:      roundTripMS,
	}
}

func newNTPCalibration(sample ntpSample) clockCalibration {
	return clockCalibration{
		source:           "ntp:" + sample.server,
		anchor:           sample.anchor,
		accurateAtAnchor: sample.accurateAtAnchor,
		roundTripMS:      sample.roundTripMS,
	}
}

func (c clockCalibration) snapshot(now time.Time) ReportTime {
	age := now.Sub(c.anchor)
	if age < 0 {
		age = 0
	}
	ageMS := durationMilliseconds(age)
	accurateTS := c.timestamp(now)
	localTS := now.UnixMilli()
	offsetMS := saturatingSubtract(accurateTS, localTS)
	source := c.source
	roundTripMS := c.roundTripMS
	return ReportTime{
		LocalTS:     localTS,
		AccurateTS:  &accurateTS,
		OffsetMS:    &offsetMS,
		Source:      &source,
		RoundTripMS: &roundTripMS,
		SampleAgeMS: &ageMS,
	}
}

func (c clockCalibration) timestamp(at time.Time) int64 {
	deltaMilliseconds := int64(at.Sub(c.anchor) / time.Millisecond)
	return saturatingAdd(c.accurateAtAnchor, deltaMilliseconds)
}

func (c *calibratedClock) snapshot(now time.Time) ReportTime {
	c.mu.Lock()
	defer c.mu.Unlock()

	calibration := c.calibrationAt(now, false)
	if calibration == nil {
		return ReportTime{LocalTS: now.UnixMilli()}
	}
	return calibration.snapshot(now)
}

// timestamp maps an observation time onto the calibrated Unix timeline. A
// fresh calibration may be used retroactively so samples collected shortly
// before the NTP response can still be corrected before they are reported.
func (c *calibratedClock) timestamp(at time.Time) (int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	calibration := c.calibrationAt(at, true)
	if calibration == nil {
		return at.UnixMilli(), false
	}
	return calibration.timestamp(at), true
}

// correctLocalTimestamp applies the calibrated offset at observedAt to an
// absolute timestamp supplied by the local OS, such as boot_time.
func (c *calibratedClock) correctLocalTimestamp(localTimestamp int64, observedAt time.Time) int64 {
	accurateAt, ok := c.timestamp(observedAt)
	if !ok {
		return localTimestamp
	}
	offset := saturatingSubtract(accurateAt, observedAt.UnixMilli())
	return saturatingAdd(localTimestamp, offset)
}

func (c *calibratedClock) calibrationAt(at time.Time, allowRetroactive bool) *clockCalibration {
	if calibrationUsable(c.ntp, at, allowRetroactive) {
		return c.ntp
	}
	if calibrationUsable(c.server, at, allowRetroactive) {
		return c.server
	}
	return nil
}

func calibrationUsable(calibration *clockCalibration, at time.Time, allowRetroactive bool) bool {
	if calibration == nil {
		return false
	}
	age := at.Sub(calibration.anchor)
	if age < 0 {
		return allowRetroactive && age >= -maxCalibrationAge
	}
	return age <= maxCalibrationAge
}

func (c *calibratedClock) beginNTPRefresh(now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.lastNTPAttempt.IsZero() && now.Sub(c.lastNTPAttempt) < ntpRefreshInterval {
		return false
	}
	c.lastNTPAttempt = now
	return true
}

func (c *calibratedClock) updateNTP(sample ntpSample) ReportTime {
	calibration := newNTPCalibration(sample)
	snapshot := calibration.snapshot(time.Now())
	c.mu.Lock()
	c.ntp = &calibration
	c.mu.Unlock()
	return snapshot
}

func (c *calibratedClock) updateServer(serverTime int64, roundTrip time.Duration, anchor time.Time) ReportTime {
	calibration := newServerCalibration(serverTime, roundTrip, anchor)
	snapshot := calibration.snapshot(time.Now())
	c.mu.Lock()
	c.server = &calibration
	c.mu.Unlock()
	return snapshot
}

func durationMilliseconds(duration time.Duration) uint64 {
	if duration <= 0 {
		return 0
	}
	return uint64(duration / time.Millisecond)
}

func addMilliseconds(timestamp int64, milliseconds uint64) int64 {
	if milliseconds > math.MaxInt64 || timestamp > math.MaxInt64-int64(milliseconds) {
		return math.MaxInt64
	}
	return timestamp + int64(milliseconds)
}

func saturatingSubtract(left, right int64) int64 {
	if right > 0 && left < math.MinInt64+right {
		return math.MinInt64
	}
	if right < 0 && left > math.MaxInt64+right {
		return math.MaxInt64
	}
	return left - right
}

func saturatingAdd(left, right int64) int64 {
	if right > 0 && left > math.MaxInt64-right {
		return math.MaxInt64
	}
	if right < 0 && left < math.MinInt64-right {
		return math.MinInt64
	}
	return left + right
}

func unixMillisecondsToNTPTimestamp(unixMilliseconds int64) [8]byte {
	seconds := unixMilliseconds / 1000
	milliseconds := unixMilliseconds % 1000
	if milliseconds < 0 {
		seconds--
		milliseconds += 1000
	}
	ntpSeconds := uint32(seconds + ntpUnixEpochSeconds)
	fraction := uint32((uint64(milliseconds) << 32) / 1000)
	var encoded [8]byte
	binary.BigEndian.PutUint32(encoded[:4], ntpSeconds)
	binary.BigEndian.PutUint32(encoded[4:], fraction)
	return encoded
}

func ntpTimestampToUnixNanoseconds(timestamp []byte) (int64, error) {
	if len(timestamp) != 8 {
		return 0, errors.New("invalid NTP timestamp length")
	}
	seconds := binary.BigEndian.Uint32(timestamp[:4])
	fraction := binary.BigEndian.Uint32(timestamp[4:])
	if seconds == 0 && fraction == 0 {
		return 0, errors.New("empty NTP timestamp")
	}
	unixSeconds := int64(seconds) - ntpUnixEpochSeconds
	fractionNanos := int64(uint64(fraction) * uint64(nanosecondsPerSecond) >> 32)
	return unixSeconds*nanosecondsPerSecond + fractionNanos, nil
}

func queryNTPServers(ctx context.Context, servers []string) (ntpSample, error) {
	if len(servers) == 0 {
		return ntpSample{}, errors.New("no NTP servers configured")
	}
	type result struct {
		sample ntpSample
		err    error
	}
	results := make(chan result, len(servers))
	for _, server := range servers {
		server := server
		go func() {
			sample, err := queryNTPServer(ctx, server)
			results <- result{sample: sample, err: err}
		}()
	}

	samples := make([]ntpSample, 0, len(servers))
	for range servers {
		result := <-results
		if result.err == nil {
			samples = append(samples, result.sample)
		}
	}
	if len(samples) == 0 {
		return ntpSample{}, errors.New("all NTP queries failed")
	}
	return selectMedianNTPSample(samples), nil
}

func selectMedianNTPSample(samples []ntpSample) ntpSample {
	sort.Slice(samples, func(i, j int) bool {
		return samples[i].offsetNanos < samples[j].offsetNanos
	})
	middle := len(samples) / 2
	median := samples[middle].offsetNanos
	if len(samples)%2 == 0 {
		lower := samples[middle-1].offsetNanos
		median = lower + (median-lower)/2
	}
	selected := 0
	for index := 1; index < len(samples); index++ {
		selectedDistance := absoluteDifference(samples[selected].offsetNanos, median)
		candidateDistance := absoluteDifference(samples[index].offsetNanos, median)
		if candidateDistance < selectedDistance ||
			(candidateDistance == selectedDistance && samples[index].roundTripMS < samples[selected].roundTripMS) {
			selected = index
		}
	}
	return samples[selected]
}

func absoluteDifference(left, right int64) uint64 {
	if left >= right {
		return uint64(left) - uint64(right)
	}
	return uint64(right) - uint64(left)
}

func queryNTPServer(parent context.Context, server string) (ntpSample, error) {
	ctx, cancel := context.WithTimeout(parent, ntpQueryTimeout)
	defer cancel()
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, server)
	if err != nil {
		return ntpSample{}, fmt.Errorf("resolve NTP server %s: %w", server, err)
	}
	if len(addresses) == 0 {
		return ntpSample{}, fmt.Errorf("NTP server %s has no addresses", server)
	}
	selected := addresses[0].IP
	for _, address := range addresses {
		if address.IP.To4() != nil {
			selected = address.IP
			break
		}
	}
	return queryNTPAddress(ctx, server, &net.UDPAddr{IP: selected, Port: 123})
}

func queryNTPAddress(ctx context.Context, server string, address *net.UDPAddr) (ntpSample, error) {
	connection, err := net.DialUDP("udp", nil, address)
	if err != nil {
		return ntpSample{}, fmt.Errorf("connect NTP server %s: %w", server, err)
	}
	defer connection.Close()
	deadline := time.Now().Add(ntpQueryTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return ntpSample{}, fmt.Errorf("set NTP deadline: %w", err)
	}

	started := time.Now()
	localStarted := started.UnixMilli()
	request := make([]byte, 48)
	request[0] = 0x23 // leap=0, version=4, mode=3 (client)
	transmitTimestamp := unixMillisecondsToNTPTimestamp(localStarted)
	copy(request[40:48], transmitTimestamp[:])
	if _, err := connection.Write(request); err != nil {
		return ntpSample{}, fmt.Errorf("send NTP request: %w", err)
	}
	response := make([]byte, 512)
	received, err := connection.Read(response)
	anchor := time.Now()
	if err != nil {
		if ctx.Err() != nil {
			return ntpSample{}, ctx.Err()
		}
		return ntpSample{}, fmt.Errorf("receive NTP response: %w", err)
	}
	if received < 48 {
		return ntpSample{}, fmt.Errorf("short NTP response: %d bytes", received)
	}
	response = response[:received]
	header := response[0]
	leap := header >> 6
	version := (header >> 3) & 0x07
	mode := header & 0x07
	stratum := response[1]
	if leap == 3 || version < 3 || version > 4 || mode != 4 || stratum < 1 || stratum > 15 {
		return ntpSample{}, errors.New("invalid NTP response header")
	}
	if !bytes.Equal(response[24:32], request[40:48]) {
		return ntpSample{}, errors.New("NTP originate timestamp mismatch")
	}

	t1 := localStarted * nanosecondsPerMilli
	elapsedNanos := anchor.Sub(started).Nanoseconds()
	t4 := t1 + elapsedNanos
	t2, err := ntpTimestampToUnixNanoseconds(response[32:40])
	if err != nil {
		return ntpSample{}, fmt.Errorf("decode NTP receive timestamp: %w", err)
	}
	t3, err := ntpTimestampToUnixNanoseconds(response[40:48])
	if err != nil {
		return ntpSample{}, fmt.Errorf("decode NTP transmit timestamp: %w", err)
	}
	if t3 < t2 {
		return ntpSample{}, errors.New("NTP transmit time precedes receive time")
	}
	offsetNanos := ((t2 - t1) + (t3 - t4)) / 2
	accurateAtAnchor := (t4 + offsetNanos) / nanosecondsPerMilli
	networkDelay := elapsedNanos - (t3 - t2)
	if networkDelay < 0 {
		networkDelay = 0
	}
	return ntpSample{
		server:           server,
		anchor:           anchor,
		accurateAtAnchor: accurateAtAnchor,
		roundTripMS:      uint64((networkDelay + nanosecondsPerMilli - 1) / nanosecondsPerMilli),
		offsetNanos:      offsetNanos,
	}, nil
}

// responseServerTime accepts the URL-encoded CF response format and a small
// JSON envelope for forward compatibility. A response header is also accepted
// so Workers can add time calibration without changing their existing body.
func responseServerTime(body []byte, headers http.Header) (int64, bool) {
	if timestamp, ok := parseServerTimeValue(headers.Get(serverTimeHeader)); ok {
		return timestamp, true
	}
	raw := strings.TrimSpace(string(body))
	if raw == "" || strings.EqualFold(raw, "OK") {
		return 0, false
	}
	if strings.HasPrefix(raw, "{") {
		var envelope struct {
			ServerTime json.RawMessage `json:"server_time"`
		}
		if json.Unmarshal(body, &envelope) == nil && len(envelope.ServerTime) > 0 {
			var timestamp int64
			if json.Unmarshal(envelope.ServerTime, &timestamp) == nil && timestamp > 0 {
				return timestamp, true
			}
		}
		return 0, false
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return 0, false
	}
	return parseServerTimeValue(values.Get("server_time"))
}

func parseServerTimeValue(raw string) (int64, bool) {
	timestamp, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	return timestamp, err == nil && timestamp > 0
}

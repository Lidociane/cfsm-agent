package cfprobe

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

type trafficState struct {
	RXPrev      uint64
	TXPrev      uint64
	RXPeriod    uint64
	TXPeriod    uint64
	LastCheck   int64
	PeriodStart int64
	Interface   string
}

func readTrafficState(path string) trafficState {
	values, err := parseKVFile(path)
	if err != nil {
		return trafficState{}
	}
	return trafficState{
		RXPrev:      parseUintDefault(values["RX_PREV"], 0),
		TXPrev:      parseUintDefault(values["TX_PREV"], 0),
		RXPeriod:    parseUintDefault(values["RX_PERIOD"], 0),
		TXPeriod:    parseUintDefault(values["TX_PERIOD"], 0),
		LastCheck:   atoi64Default(values["LAST_CHECK"], 0),
		PeriodStart: atoi64Default(values["PERIOD_START"], 0),
		Interface:   values["INTERFACE"],
	}
}

func writeTrafficState(path string, st trafficState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data := fmt.Sprintf("RX_PREV=%d\nTX_PREV=%d\nRX_PERIOD=%d\nTX_PERIOD=%d\nLAST_CHECK=%d\nPERIOD_START=%d\nINTERFACE=%s\n",
		st.RXPrev, st.TXPrev, st.RXPeriod, st.TXPeriod, st.LastCheck, st.PeriodStart, st.Interface)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(data), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func calcMonthlyTraffic(path string, current NetBytes, resetDay int, iface string) (uint64, uint64) {
	now := time.Now().Unix()
	st := readTrafficState(path)
	if st.Interface != iface {
		st = trafficState{}
	}
	periodStart := periodStartTS(time.Unix(now, 0).UTC(), resetDay)
	if st.LastCheck == 0 {
		st.RXPrev = current.RX
		st.TXPrev = current.TX
		st.LastCheck = now
		st.PeriodStart = periodStart
		st.Interface = iface
		_ = writeTrafficState(path, st)
		return st.RXPeriod, st.TXPeriod
	}

	var rxDelta, txDelta uint64
	if current.RX >= st.RXPrev {
		rxDelta = current.RX - st.RXPrev
	}
	if current.TX >= st.TXPrev {
		txDelta = current.TX - st.TXPrev
	}
	if periodStart != 0 && st.PeriodStart != 0 && periodStart != st.PeriodStart {
		st.RXPeriod = rxDelta
		st.TXPeriod = txDelta
	} else {
		st.RXPeriod += rxDelta
		st.TXPeriod += txDelta
	}
	st.RXPrev = current.RX
	st.TXPrev = current.TX
	st.LastCheck = now
	st.PeriodStart = periodStart
	st.Interface = iface
	_ = writeTrafficState(path, st)
	return st.RXPeriod, st.TXPeriod
}

func applyTrafficCorrection(path string, current NetBytes, iface string, rxGB, txGB string) error {
	rx, err := parseTrafficCorrectionGB(rxGB)
	if err != nil {
		return err
	}
	tx, err := parseTrafficCorrectionGB(txGB)
	if err != nil {
		return err
	}
	st := readTrafficState(path)
	st.RXPrev = current.RX
	st.TXPrev = current.TX
	st.RXPeriod = rx
	st.TXPeriod = tx
	st.LastCheck = time.Now().Unix()
	st.Interface = iface
	return writeTrafficState(path, st)
}

func periodStartTS(now time.Time, resetDay int) int64 {
	if resetDay == 0 {
		return 0
	}
	if resetDay < 1 || resetDay > 31 {
		resetDay = 1
	}
	year, month, day := now.Date()
	startDay := clampDay(year, month, resetDay)
	start := time.Date(year, month, startDay, 0, 0, 0, 0, time.UTC)
	if day < startDay {
		prev := now.AddDate(0, -1, 0)
		py, pm, _ := prev.Date()
		start = time.Date(py, pm, clampDay(py, pm, resetDay), 0, 0, 0, 0, time.UTC)
	}
	return start.Unix()
}

func clampDay(year int, month time.Month, day int) int {
	last := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if day > last {
		return last
	}
	if day < 1 {
		return 1
	}
	return day
}

func parseUintDefault(raw string, def uint64) uint64 {
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return def
	}
	return n
}

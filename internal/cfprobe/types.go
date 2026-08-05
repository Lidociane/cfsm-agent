package cfprobe

import "time"

const (
	serviceNameDefault       = "cf-probe"
	legacyAgentVersion       = "1.0.0"
	maxTrafficCorrectionGB   = 1000000
	autoUpdateDelay          = 60 * time.Second
	configSchemaVersion      = "3"
	defaultReportIntervalSec = 60
)

type Config struct {
	ServerID        string
	Secret          string
	WorkerURL       string
	CollectInterval int
	ReportInterval  int
	CTNode          string
	CUNode          string
	CMNode          string
	BDNode          string
	Interface       string
	ResetDay        int
	AutoUpdate      bool
	UpdateProxy     string
	// ConfigMD5 mirrors the worker dynamic config version; local fields like
	// AutoUpdate and UpdateProxy are intentionally outside that comparison.
	ConfigMD5 string
}

type Paths struct {
	ServiceName     string
	BinaryFile      string
	ConfigDir       string
	ConfigFile      string
	TrafficFile     string
	OldTrafficFile  string
	PIDFile         string
	LogFile         string
	ServiceFile     string
	DebugEnvFile    string
	LaunchdLabel    string
	LaunchdUserFile string
	LaunchdRootFile string
}

type InstallOptions struct {
	Config
	Debug          bool
	RXCorrectionGB string
	TXCorrectionGB string
	NoStart        bool
}

type ProbeResult struct {
	RTTMs int
	Loss  int
	OK    bool
}

type ProbeSnapshot struct {
	IPv4 string
	IPv6 string
	CT   ProbeResult
	CU   ProbeResult
	CM   ProbeResult
	BD   ProbeResult
}

type Metrics struct {
	CPU          string
	RAMTotal     string
	RAMUsed      string
	SwapTotal    string
	SwapUsed     string
	DiskTotal    string
	DiskUsed     string
	LoadAvg      string
	BootTime     string
	NetRX        string
	NetTX        string
	NetRXMonthly string
	NetTXMonthly string
	NetInSpeed   string
	NetOutSpeed  string
	OS           string
	Arch         string
	Kernel       string
	CPUInfo      string
	CPUCores     string
	GPUInfo      any
	Processes    string
	TCPConn      string
	UDPConn      string
	IPv4         string
	IPv6         string
	PingCT       any
	PingCU       any
	PingCM       any
	PingBD       any
	LossCT       any
	LossCU       any
	LossCM       any
	LossBD       any
}

type BasicStats struct {
	MemTotalMB  uint64
	MemUsedMB   uint64
	SwapTotalMB uint64
	SwapUsedMB  uint64
	DiskTotalMB uint64
	DiskUsedMB  uint64
	LoadAvg     string
	BootTimeMS  int64
	OSName      string
	Arch        string
	Kernel      string
	CPUInfo     string
	CPUCores    int
	GPUInfo     any
	Processes   int
	TCPConn     int
	UDPConn     int
}

type NetBytes struct {
	RX uint64
	TX uint64
}

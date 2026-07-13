package models

import "time"

type User struct {
	ID                 int64
	Username           string
	PasswordHash       string
	MustChangePassword bool
	CreatedAt          time.Time
}

type Account struct {
	ID                   int64
	Tag                  string
	Directory            string
	Status               string
	PrivateKey           string
	ClientID             string
	AccessToken          string
	DeviceID             string
	LicenseKey           string
	PeerPublicKey        string
	LocalAddressV4       string
	LocalAddressV6       string
	EndpointHost         string
	EndpointPort         int
	MTU                  int
	ListenPort           int
	MasquePrivateKey     string
	MasqueEndpointPubKey string
	MasqueEndpointV4     string
	MasqueEndpointV6     string
	LastPublicIP         string
	LastColo             string
	LastCountry          string
	LastLatencyMs        int
	LastSpeedBps         int
	LastPacketLoss       float64
	LastScore            float64
	LastTestedAt         *time.Time
	TrafficUp            int64
	TrafficDown          int64
	IsIPKeeper           bool
	CreatedAt            time.Time
	DisabledReason       string
}

type AccountTest struct {
	ID         int64
	AccountID  int64
	TestedAt   time.Time
	PublicIP   string
	Colo       string
	Country    string
	LatencyMs  int
	SpeedBps   int
	PacketLoss float64
	Score      float64
	Error      string
}

type IPPoolEntry struct {
	ID              int64
	PublicIP        string
	KeeperAccountID *int64
	TotalUp         int64
	TotalDown       int64
	CurrentUpBps    int64
	CurrentDownBps  int64
	LastSeenAt      *time.Time
}

type ProxyClientUsage struct {
	ClientIP   string
	Username   string
	AccountTag string
	TotalUp    int64
	TotalDown  int64
	HitCount   int64
	FirstSeen  time.Time
	LastSeen   time.Time
}

type ProxySlot struct {
	ID              int64
	Username        string
	Password        string
	AccountID       *int64
	AccountTag      string
	AccountStatus   string
	PublicIP        string
	PinnedPublicIP  string
	Country         string
	LatencyMs       int
	SpeedBps        int
	PacketLoss      float64
	Score           float64
	IsKeeper        bool
	Status          string
	LastError       string
	ProbeFailures   int
	IPDriftFailures int
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type ScheduleRun struct {
	ID               int64
	StartedAt        time.Time
	FinishedAt       *time.Time
	Kind             string
	Status           string
	Detail           string
	AccountsKept     *int
	AccountsDisabled *int
}

type Setting struct {
	Key   string
	Value string
}

// AgentNode 是一台注册进来的远程 agent（部署在其他地区 VPS 上）。它主动反连
// 主控，并通过 VPS 本地的 WARP 隧道提供对应地区出口。每条 WARP 出口作为独立
// 会话持久化；实时在线状态和 VPS 主机元数据由 agenthub 在内存中维护。
type AgentNode struct {
	ID         int64
	NodeID     string // 稳定标识，agent 首次注册时生成并写进它的本地配置，重连复用
	Name       string // 展示名，默认取地区，可为空
	PublicIP   string
	Country    string
	Colo       string
	Enabled    bool
	LastSeenAt *time.Time
	CreatedAt  time.Time
	// 以下字段不落库，由 agenthub 内存态在查询时填充。
	Online    bool
	LatencyMs int
}

type TrafficSample struct {
	SampledAt time.Time
	UpBps     int64
	DownBps   int64
}

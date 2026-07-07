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

package domain

import (
	"errors"
	"time"
)

var ErrDeviceAlreadyExists = errors.New("device already exists")

// Device is one row of the device inventory as the dashboard reads it. These
// are display values, not protocol state: the network server owns that.
type Device struct {
	DevEUI   string
	Name     string
	Group    string
	Gateway  string
	LastSeen *time.Time
	Battery  int16
	RSSI     int16
	State    string
}

// CreateDevice is what the registration form sends. It carries no keys: those
// are generated or supplied during provisioning.
type CreateDevice struct {
	DevEUI string
	Name   string
	Group  string
}

// Overview holds the counters behind the dashboard cards, plus the per-hour
// uplink counts the traffic chart draws.
type Overview struct {
	DeviceCount      int
	OnlineDevices    int
	AttentionDevices int
	OfflineDevices   int
	GatewayCount     int
	OnlineGateways   int
	Uplinks24h       int
	QueuedDownlinks  int
	SentDownlinks24h int
	LastDownlink     *time.Time
	HourlyUplinks    []int
}

// Gateway is one gateway as stored by this service. Live radio health comes
// from the network server and is deliberately not merged into this record.
type Gateway struct {
	ID          string
	Name        string
	Region      string
	LastSeen    *time.Time
	Uplinks24h  int
	AverageRSSI *float64
	AverageSNR  *float64
}

// NetworkEvent is one row of the activity feed.
type NetworkEvent struct {
	ID        int64
	Type      string
	Title     string
	Detail    string
	CreatedAt time.Time
}

// FacilitySettings is the facility profile: the details shown in the sidebar
// and used to fill in defaults.
type FacilitySettings struct {
	Configured   bool
	FacilityName string
	Region       string
	OperatorName string
	OperatorRole string
}

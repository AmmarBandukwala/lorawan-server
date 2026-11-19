package domain

import (
	"errors"
	"time"
)

var ErrDeviceAlreadyExists = errors.New("device already exists")

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

type CreateDevice struct {
	DevEUI string
	Name   string
	Group  string
}

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

type Gateway struct {
	ID          string
	Name        string
	Region      string
	LastSeen    *time.Time
	Uplinks24h  int
	AverageRSSI *float64
	AverageSNR  *float64
}

type NetworkEvent struct {
	ID        int64
	Type      string
	Title     string
	Detail    string
	CreatedAt time.Time
}

type FacilitySettings struct {
	Configured   bool
	FacilityName string
	Region       string
	OperatorName string
	OperatorRole string
}

package httpapi

import (
	"net/http"
	"time"
)

type overviewResponse struct {
	DeviceCount      int    `json:"deviceCount"`
	OnlineDevices    int    `json:"onlineDevices"`
	AttentionDevices int    `json:"attentionDevices"`
	OfflineDevices   int    `json:"offlineDevices"`
	GatewayCount     int    `json:"gatewayCount"`
	OnlineGateways   int    `json:"onlineGateways"`
	Uplinks24h       int    `json:"uplinks24h"`
	QueuedDownlinks  int    `json:"queuedDownlinks"`
	SentDownlinks24h int    `json:"sentDownlinks24h"`
	LastDownlink     string `json:"lastDownlink"`
	HourlyUplinks    []int  `json:"hourlyUplinks"`
}

type gatewayResponse struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Region      string   `json:"region"`
	LastSeen    string   `json:"lastSeen"`
	State       string   `json:"state"`
	Uplinks24h  int      `json:"uplinks24h"`
	AverageRSSI *float64 `json:"averageRssi"`
	AverageSNR  *float64 `json:"averageSnr"`
}

type eventResponse struct {
	ID      int64  `json:"id"`
	Type    string `json:"type"`
	Title   string `json:"title"`
	Detail  string `json:"detail"`
	TimeAgo string `json:"timeAgo"`
}

func (s *Server) getOverview(w http.ResponseWriter, r *http.Request) {
	result, err := s.repository.GetOverview(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "overview_query_failed")
		return
	}
	response := overviewResponse{
		DeviceCount: result.DeviceCount, OnlineDevices: result.OnlineDevices,
		AttentionDevices: result.AttentionDevices, OfflineDevices: result.OfflineDevices,
		GatewayCount: result.GatewayCount, OnlineGateways: result.OnlineGateways,
		Uplinks24h: result.Uplinks24h, QueuedDownlinks: result.QueuedDownlinks,
		SentDownlinks24h: result.SentDownlinks24h, LastDownlink: relativeTime(time.Now(), result.LastDownlink),
		HourlyUplinks: result.HourlyUplinks,
	}
	if response.HourlyUplinks == nil {
		response.HourlyUplinks = []int{}
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) listGateways(w http.ResponseWriter, r *http.Request) {
	items, err := s.repository.ListGateways(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "gateway_query_failed")
		return
	}
	now := time.Now()
	response := make([]gatewayResponse, 0, len(items))
	for _, item := range items {
		state := "Offline"
		if item.LastSeen != nil && now.Sub(*item.LastSeen) <= 3*time.Minute {
			state = "Online"
		}
		response = append(response, gatewayResponse{
			ID: item.ID, Name: item.Name, Region: item.Region,
			LastSeen: relativeTime(now, item.LastSeen), State: state,
			Uplinks24h: item.Uplinks24h, AverageRSSI: item.AverageRSSI, AverageSNR: item.AverageSNR,
		})
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
	items, err := s.repository.ListEvents(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "event_query_failed")
		return
	}
	now := time.Now()
	response := make([]eventResponse, 0, len(items))
	for _, item := range items {
		createdAt := item.CreatedAt
		response = append(response, eventResponse{
			ID: item.ID, Type: item.Type, Title: item.Title, Detail: item.Detail,
			TimeAgo: relativeTime(now, &createdAt),
		})
	}
	writeJSON(w, http.StatusOK, response)
}

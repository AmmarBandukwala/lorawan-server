package main

import "time"

// JSON shapes this API returns.
//
// They live together, rather than inline in each handler, because they are the
// contract every client parses: the Fieldnode API's nsclient, the dashboard, and
// any operator's curl one-liner. Field names written into a map[string]any at the
// call site are invisible to that search — a rename leaves no compile error and
// no reference for a client to grep — while a struct tag shows up in both.
//
// Renaming or removing a field is a breaking change for whoever parses it. Adding
// one is not, so prefer adding.

// downlinkQueuedResponse answers POST .../downlink.
type downlinkQueuedResponse struct {
	DevEUI    string `json:"dev_eui"`
	Queued    bool   `json:"queued"`
	FPort     int    `json:"f_port"`
	Confirmed bool   `json:"confirmed"`
	// Explanation is aimed at the caller who expected a synchronous send. The
	// API is deliberately asynchronous, and saying so in the body saves reading
	// the docs to work out why nothing happened yet.
	Explanation string `json:"explanation"`
}

// queuedExplanation is that reassurance, kept in one place so the wording cannot
// drift between endpoints.
const queuedExplanation = "the frame goes out in the device's next RX window"

// downlinkCancelledResponse answers DELETE .../downlink.
type downlinkCancelledResponse struct {
	DevEUI    string `json:"dev_eui"`
	Cancelled bool   `json:"cancelled"`
}

// deviceStatusScheduledResponse answers POST .../device-status.
type deviceStatusScheduledResponse struct {
	DevEUI    string `json:"dev_eui"`
	Scheduled bool   `json:"scheduled"`
}

// sessionResponse is what the network currently believes about a device's radio
// parameters. Optional observations are pointers so an absent measurement is
// absent from the JSON rather than indistinguishable from a zero.
type sessionResponse struct {
	DevEUI         string `json:"dev_eui"`
	DevAddr        string `json:"dev_addr"`
	Region         string `json:"region"`
	FCntUp         uint32 `json:"f_cnt_up"`
	FCntDown       uint32 `json:"f_cnt_down"`
	DataRate       int    `json:"data_rate"`
	TxPowerIndex   int    `json:"tx_power_index"`
	NbTrans        int    `json:"nb_trans"`
	ADR            bool   `json:"adr"`
	Rx1Delay       int    `json:"rx1_delay"`
	Rx1DROffset    int    `json:"rx1_dr_offset"`
	Rx2FrequencyHz uint32 `json:"rx2_frequency_hz"`
	Rx2DataRate    int    `json:"rx2_data_rate"`
	// DutyCycle is the fraction of time the device may transmit, 0 meaning no
	// restriction beyond the band's own.
	DutyCycle float64 `json:"duty_cycle"`
	LastRSSI  float64 `json:"last_rssi"`
	LastSNR   float64 `json:"last_snr"`

	// LastUplinkAt is absent until the first uplink of the session; a zero
	// timestamp would read as "1 January year 1" to a client that parses it.
	LastUplinkAt *time.Time `json:"last_uplink_at,omitempty"`

	// Battery and MarginDB are absent until the device has answered a
	// DevStatusReq. Battery is the LoRaWAN battery level, 255 meaning "the
	// device cannot report"; omitting them keeps "unknown" distinct from zero.
	Battery  *int `json:"battery,omitempty"`
	MarginDB *int `json:"margin_db,omitempty"`

	Pending *pendingDownlinkResponse `json:"pending_downlink,omitempty"`
}

// pendingDownlinkResponse describes the one application frame waiting for the
// device's next window.
type pendingDownlinkResponse struct {
	Confirmed bool `json:"confirmed"`
	// FPort is absent for a frame that carries only MAC commands.
	FPort *int `json:"f_port,omitempty"`
	// Data is the frame payload as hex, which is what an operator compares
	// against what the application submitted.
	Data string `json:"data,omitempty"`
	// MACCommands counts the commands riding in FOpts, without decoding them:
	// the count is what tells you a control frame is not an application frame.
	MACCommands int `json:"mac_commands,omitempty"`
}

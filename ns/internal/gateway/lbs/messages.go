package lbs

// The message shapes of the LoRa Basics Station LNS protocol.
//
// Field names and JSON spellings are the protocol's, not ours: a station
// firmware will not negotiate, so a wrong tag is a gateway that connects and
// then does nothing. The reference for every tag here is Semtech's tcproto
// definition, cross-checked against The Things Stack's `lbslns` package, which
// has run against real stations for years.
//
// Fields the station sends that nothing in this server can act on are
// deliberately absent. Modelling them would mean maintaining fields that are
// read by nobody, which is how a codec accumulates lies.

// radioMetadata is the `rxq` object: how the station received a frame.
type radioMetadata struct {
	// DataRate is the regional data-rate index — the same numbering the region
	// tables use, so no translation is needed on this path.
	DataRate  int     `json:"DR"`
	Frequency uint64  `json:"Freq"` // Hz
	RSSI      float64 `json:"Rssi"`
	SNR       float64 `json:"Snr"`
	// XTime is the 64-bit concentrator timestamp of the reception. Tmst, the
	// 32-bit form, is the low 32 bits of the same counter; XTime is preferred
	// because it also carries the session identifier.
	XTime uint64 `json:"XTime"`
	Tmst  uint32 `json:"Tmst"`
	// Time is an ISO 8601 UTC arrival time, present when the station has a
	// synchronised clock.
	Time string `json:"Time"`
}

// uplinkInfo is the `upinfo` object: the station's timing context for a frame.
type uplinkInfo struct {
	// XTime is the authoritative reference timestamp a downlink answer is built
	// from.
	XTime uint64 `json:"xtime"`
	// RxTime is the reception time as UNIX fractional seconds, usable when the
	// station has no concentrator lock.
	RxTime float64 `json:"rxtime"`
	// GPSTime is microseconds since the GPS epoch, present on GPS-locked
	// stations.
	GPSTime int64 `json:"gpstime"`
	RCtx    int64 `json:"rctx"`
}

// uplink is the `updf` message, and `jreq` with different payload fields.
//
// Drawx is the raw PHYPayload, base64url-encoded without padding, and is the
// field this server prefers: it is the frame as it came off the radio, MIC
// included, so feeding it to the codec means validating the same bytes the
// device sent. The decoded fields (MHdr, DevAddr, Fcnt, FRMPayload, MIC) are a
// convenience view that some builds send and some omit, used only to
// reconstruct a frame when Drawx is absent.
type uplink struct {
	Type    string        `json:"msgtype"`
	DevEUI  eui           `json:"DevEui"`
	Class   int           `json:"dC"`
	DIID    int64         `json:"diid"`
	Radio   radioMetadata `json:"rxq"`
	UpInfo  uplinkInfo    `json:"upinfo"`
	Drawx   string        `json:"drawx"`
	RefTime float64       `json:"RefTime"`

	// Decoded view, used only as a fallback.
	MHdr       uint   `json:"MHdr"`
	JoinEUI    eui    `json:"JoinEui"`
	DevAddr    uint32 `json:"DevAddr"`
	DevNonce   uint32 `json:"DevNonce"`
	FCtrl      uint   `json:"FCtrl"`
	FCnt       uint   `json:"Fcnt"`
	FOpts      string `json:"FOpts"`
	FPort      int    `json:"FPort"`
	FRMPayload string `json:"FRMPayload"`
	MIC        int64  `json:"MIC"`
}

// version is the handshake a station sends first. The answer is a
// router_config; a station that does not get one retries forever.
type version struct {
	Station  string `json:"station"`
	Firmware string `json:"firmware"`
	Package  string `json:"package"`
	Model    string `json:"model"`
	Protocol int    `json:"protocol"`
	Features string `json:"features"`
}

// txConfirmation is `dntxed`: the station reporting what it did with a dnmsg.
//
// This is the only acknowledgement the protocol offers, and it is the
// difference between "the server scheduled a downlink" and "the gateway
// transmitted it".
type txConfirmation struct {
	DIID    int64   `json:"diid"`
	DevEUI  eui     `json:"DevEui"`
	RCtx    int64   `json:"rctx"`
	XTime   uint64  `json:"xtime"`
	TxTime  float64 `json:"txtime"`
	GPSTime int64   `json:"gpstime"`
}

// timeSyncRequest is `timesync`, a station asking what the network's clock
// reads so it can correct its own.
type timeSyncRequest struct {
	TxTime float64 `json:"txtime"`
}

// timeSyncResponse answers a timesync. MuxTime is the server's clock when it
// answered; XTime and GPSTime let the station tie its concentrator counter to
// wall-clock time. XTime is omitted when the station has never sent one,
// because there is then nothing to correlate against.
//
// msgtype is not decoration. A station classifies an incoming message by that
// field and silently discards anything it cannot place, so a response without
// it looks like a success in the server's log and does nothing at the gateway:
// the station keeps an uncorrected clock, and every downlink on that gateway is
// then transmitted at a small, drifting offset.
type timeSyncResponse struct {
	Type    string  `json:"msgtype"`
	TxTime  float64 `json:"txtime,omitempty"`
	XTime   uint64  `json:"xtime,omitempty"`
	GPSTime int64   `json:"gpstime"`
	MuxTime float64 `json:"MuxTime,omitempty"`
}

// downlink is `dnmsg`, one transmission request.
//
// The station transmits at xtime + RxDelay seconds, using RX1DR and RX1Freq.
// The field names say RX1 but they mean "the data rate and frequency of this
// transmission", so an RX2 downlink sets RxDelay to the RX2 delay and puts the
// RX2 parameters in them.
type downlink struct {
	Type     string  `json:"msgtype"`
	DevEUI   string  `json:"DevEui"`
	Class    int     `json:"dC"`
	DIID     int64   `json:"diid"`
	PDU      string  `json:"pdu"` // hex PHYPayload
	Priority int     `json:"priority"`
	RCtx     int64   `json:"rctx"`
	MuxTime  float64 `json:"MuxTime"`

	RxDelay int    `json:"RxDelay"`
	Rx1DR   int    `json:"RX1DR"`
	Rx1Freq uint64 `json:"RX1Freq"`
	XTime   uint64 `json:"xtime"`
}

// discoverQuery is the body a station POSTs to the discovery endpoint.
type discoverQuery struct {
	Router eui `json:"router"`
}

// discoverResponse points a station at the WebSocket endpoint. Muxs names the
// multiplexor identity, which the station echoes back when it connects.
type discoverResponse struct {
	Router eui    `json:"router"`
	Muxs   string `json:"muxs"`
	URI    string `json:"uri,omitempty"`
	Error  string `json:"error,omitempty"`
}

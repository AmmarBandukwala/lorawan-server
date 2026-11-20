package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
	"github.com/fieldnode/lorawan-server/ns/internal/region"
)

type config struct {
	// listenAddr is the UDP port gateways are pointed at. 6858 is the
	// well-known packet-forwarder port.
	listenAddr string
	// statusAddr serves /healthz and /status; "off" or empty disables it.
	statusAddr string
	// apiAddr serves the application API. It only matters when NS_API_TOKEN is
	// set; the API is not created without a token.
	apiAddr     string
	region      string
	netID       lorawan.NetID
	databaseURL string

	// seedDevices is a comma separated list of devEUI:joinEUI:appKey triples to
	// provision at startup — a bring-up convenience until the Fieldnode API owns
	// provisioning. Devices that already exist are never overwritten.
	seedDevices string

	downlinkPowerdBm  int8
	deviceStatusEvery int
	gatewayTimeout    time.Duration
	// gatewayFlushInterval is how often the gateway registry is written to
	// ns_gateways. It sets how stale the dashboard's gateway panel can get, and
	// the write rate the database sees from gateway presence — nothing more.
	gatewayFlushInterval time.Duration

	// ledgerFlushInterval is how often the duty-cycle ledger and the frame
	// de-duplication book are written. Unlike presence, this one bounds a
	// compliance claim: a crash forgets the bookings made since the last flush,
	// so the interval is the window of occupancy a restart under-counts.
	ledgerFlushInterval time.Duration
	// aggregationDelay is how long to wait for other gateways' copies of a frame.
	// Negative means unset, which leaves the engine's default in force.
	aggregationDelay time.Duration
	logLevel         slog.Level

	// disableDutyCycle turns off the server's own airtime enforcement. It
	// exists for test rigs and for concentrators known to enforce locally;
	// leaving it unset is the compliant default, so the variable has to be set
	// to a truthy value deliberately.
	disableDutyCycle bool
	// dutyCycleWindow is the period duty cycle is measured over. Empty selects
	// the one-hour default the regulation uses.
	dutyCycleWindow time.Duration

	// webhookURLs are the integration endpoints events are POSTed to. Empty
	// means no integrations, which is the default and costs nothing at runtime.
	webhookURLs []string
	// webhookToken is sent as a bearer token to every webhook. One token for all
	// endpoints is a deliberate simplification: per-device keys are a
	// provisioning concern of the Fieldnode API, not of this process.
	webhookToken string

	// codecTimeout bounds one whole decode step (store lookups plus execution)
	// and codecExecution bounds the JavaScript itself. The outer one is larger: a
	// formatter allowed 100 ms should not be killed because the database took
	// 40 ms to answer, and a formatter that overruns its own budget should be
	// reported as a formatter problem rather than an unattributable timeout.
	codecTimeout   time.Duration
	codecExecution time.Duration

	// apiToken authenticates the application API (downlink submission and
	// session reads). Empty disables the API entirely rather than leaving it
	// open: an unauthenticated endpoint that can transmit radio frames on a LAN
	// is not a safe default.
	apiToken string

	// --- LoRa Basics Station (WebSocket) transport --------------------------

	// lbsAddr is the LNS endpoint stations connect to. 30001 is the port a
	// station uses when it is given nothing but a host name, so the default
	// keeps gateway configuration to one field. "off" or empty disables the
	// transport for deployments that will never have a station.
	lbsAddr string
	// lbsToken, when set, must be presented by a station. Empty leaves the
	// endpoint open, which is logged at startup: an open LNS endpoint lets
	// anyone on the network inject uplinks and claim a gateway EUI.
	lbsToken string
	// lbsRadioPlanPath names an sx1301_conf file pushed to stations. Empty lets
	// each station keep its own radio plan, which is the safe default.
	lbsRadioPlanPath string
	// lbsJoinEUIs restricts which join servers stations forward. Empty forwards
	// every join-request, which is right while join EUIs live on the device.
	lbsJoinEUIs []string
	// lbsUnlimitedTransmit tells stations to skip duty cycle and dwell time.
	lbsUnlimitedTransmit bool
}

// loadConfig reads the environment and validates it. Every failure names the
// variable to fix, because inside a container a log line is the only feedback an
// operator gets.
func loadConfig() (config, error) {
	cfg := config{
		listenAddr: envString("NS_LISTEN_ADDR", ":6858"),
		statusAddr: envString("NS_STATUS_ADDR", ":8090"),
		apiAddr:    envString("NS_API_ADDR", ":8091"),
		// The band a network starts on when NS_REGION is unset. The value comes
		// from the region package so this process, the engine's fallback and the
		// documentation cannot name three different bands.
		region:               envString("NS_REGION", region.DefaultRegion),
		databaseURL:          strings.TrimSpace(os.Getenv("NS_DATABASE_URL")),
		seedDevices:          strings.TrimSpace(os.Getenv("NS_SEED_DEVICES")),
		gatewayTimeout:       envDuration("NS_GATEWAY_TIMEOUT", 3*time.Minute),
		gatewayFlushInterval: envDuration("NS_GATEWAY_FLUSH_INTERVAL", 30*time.Second),
		ledgerFlushInterval:  envDuration("NS_LEDGER_FLUSH_INTERVAL", DefaultLedgerFlushInterval),

		// -1 means "unset", so the engine's own default stays authoritative and
		// this package does not duplicate the constant. An explicit 0 is honoured
		// as "do not wait for other gateways", which is the only way to turn
		// aggregation off.
		aggregationDelay: envDuration("NS_AGGREGATION_DELAY", -1),
	}

	netIDStr := envString("NS_NET_ID", "000000")
	netID, err := lorawan.ParseNetID(netIDStr)
	if err != nil {
		return cfg, fmt.Errorf("NS_NET_ID %q is not a 6-hex-digit NetID: %w", netIDStr, err)
	}
	cfg.netID = netID

	// Validating the band here means a typo fails at startup instead of on the
	// first uplink.
	if _, err := region.New(cfg.region); err != nil {
		return cfg, fmt.Errorf("NS_REGION: %w", err)
	}

	power, err := envInt("NS_DOWNLINK_POWER_DBM", 14)
	if err != nil {
		return cfg, err
	}
	if power < -30 || power > 36 {
		return cfg, fmt.Errorf("NS_DOWNLINK_POWER_DBM = %d, want -30..36", power)
	}
	cfg.downlinkPowerdBm = int8(power)

	if cfg.deviceStatusEvery, err = envInt("NS_DEVICE_STATUS_EVERY", 0); err != nil {
		return cfg, err
	}
	if cfg.deviceStatusEvery < 0 {
		return cfg, errors.New("NS_DEVICE_STATUS_EVERY must be 0 (disabled) or a positive count")
	}

	cfg.disableDutyCycle, err = envBool("NS_DISABLE_DUTY_CYCLE")
	if err != nil {
		return cfg, err
	}
	if cfg.disableDutyCycle {
		slog.Warn("NS_DISABLE_DUTY_CYCLE is set: the server will transmit without " +
			"checking the band's duty cycle or dwell time. Only do this where the " +
			"concentrator enforces them locally.")
	}
	cfg.dutyCycleWindow = envDuration("NS_DUTY_CYCLE_WINDOW", 0)
	if cfg.dutyCycleWindow < 0 {
		return cfg, errors.New("NS_DUTY_CYCLE_WINDOW must be positive")
	}

	cfg.logLevel = envLogLevel("NS_LOG_LEVEL", slog.LevelInfo)

	cfg.webhookURLs = envList("NS_WEBHOOK_URLS")
	cfg.webhookToken = strings.TrimSpace(os.Getenv("NS_WEBHOOK_TOKEN"))
	for _, raw := range cfg.webhookURLs {
		u, err := url.Parse(raw)
		if err != nil {
			return cfg, fmt.Errorf("NS_WEBHOOK_URLS entry %q is not a URL: %w", raw, err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return cfg, fmt.Errorf("NS_WEBHOOK_URLS entry %q must be http or https", raw)
		}
		if u.Host == "" {
			return cfg, fmt.Errorf("NS_WEBHOOK_URLS entry %q has no host", raw)
		}
	}
	// Not fatal: a webhook on loopback legitimately needs no token. Warned about
	// at startup instead, so the operator decides.
	if len(cfg.webhookURLs) > 0 && cfg.webhookToken == "" {
		slog.Warn("NS_WEBHOOK_TOKEN is not set: the receiver cannot tell these " +
			"events from anything else on the network")
	}

	// Payload formatters are vendor-supplied code, so both bounds are settable:
	// a deployment running a genuinely heavy decoder needs to raise the inner one
	// without weakening the outer guard that protects the worker.
	cfg.codecExecution = envDuration("NS_CODEC_TIMEOUT", 100*time.Millisecond)
	cfg.codecTimeout = envDuration("NS_CODEC_DEADLINE", 2*time.Second)
	if cfg.codecExecution <= 0 || cfg.codecTimeout <= 0 {
		return cfg, errors.New("NS_CODEC_TIMEOUT and NS_CODEC_DEADLINE must both be positive")
	}
	if cfg.codecExecution >= cfg.codecTimeout {
		return cfg, fmt.Errorf("NS_CODEC_TIMEOUT (%v) must be shorter than "+
			"NS_CODEC_DEADLINE (%v): the script budget has to fit inside the step budget",
			cfg.codecExecution, cfg.codecTimeout)
	}

	cfg.apiToken = strings.TrimSpace(os.Getenv("NS_API_TOKEN"))
	if len(cfg.apiToken) > 0 && len(cfg.apiToken) < 16 {
		// A short bearer token on a LAN is brute-forceable, and the API can make
		// a device radio. Refusing to start beats a quiet warning here.
		return cfg, fmt.Errorf("NS_API_TOKEN must be at least 16 characters; " +
			"generate one with: openssl rand -hex 32")
	}

	// Basics Station. Enabled by default on the port a station assumes, because a
	// MultiTech or Kerlink gateway that can only speak LNS is otherwise
	// unusable and the fix is invisible. NS_LBS_ADDR=off turns it off for
	// deployments that will never have one.
	cfg.lbsAddr = strings.TrimSpace(envString("NS_LBS_ADDR", ":30001"))
	if strings.EqualFold(cfg.lbsAddr, "off") {
		cfg.lbsAddr = ""
	}
	cfg.lbsToken = strings.TrimSpace(os.Getenv("NS_LBS_TOKEN"))
	if cfg.lbsAddr != "" && cfg.lbsToken == "" {
		// Warned rather than refused: a station behind a proxy that cannot set
		// headers is a real deployment, and the UDP port is open by default too.
		// The difference is worth stating — an open LNS endpoint lets anyone who
		// can reach it claim a gateway EUI and inject uplinks.
		slog.Warn("NS_LBS_TOKEN is not set: any client that can reach the LNS " +
			"port can register as a gateway and inject uplinks")
	}
	if len(cfg.lbsToken) > 0 && len(cfg.lbsToken) < 16 {
		return cfg, fmt.Errorf("NS_LBS_TOKEN must be at least 16 characters; " +
			"generate one with: openssl rand -hex 32")
	}
	cfg.lbsRadioPlanPath = strings.TrimSpace(os.Getenv("NS_LBS_RADIO_PLAN"))
	cfg.lbsJoinEUIs = envList("NS_LBS_JOIN_EUIS")
	for _, raw := range cfg.lbsJoinEUIs {
		if _, err := lorawan.ParseEUI64(raw); err != nil {
			return cfg, fmt.Errorf("NS_LBS_JOIN_EUIS entry %q: %w", raw, err)
		}
	}
	cfg.lbsUnlimitedTransmit, err = envBool("NS_LBS_UNLIMITED_TRANSMIT")
	if err != nil {
		return cfg, err
	}
	return cfg, nil
}

// envList splits a comma separated variable, dropping blanks so a trailing
// comma in a compose file does not become an empty endpoint.
// envBool reads a truthy flag. An unrecognised value is an error rather than a
// silent false: a flag meant to relax a regulatory limit must not be disabled
// by a typo in either direction.
func envBool(name string) (bool, error) {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	if raw == "" {
		return false, nil
	}
	switch raw {
	case "1", "t", "true", "yes", "y", "on":
		return true, nil
	case "0", "f", "false", "no", "n", "off":
		return false, nil
	default:
		return false, fmt.Errorf("%s = %q is not a boolean", name, raw)
	}
}

func envList(name string) []string {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if v := strings.TrimSpace(part); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func envString(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}

func envInt(name string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s %q is not an integer: %w", name, raw, err)
	}
	return v, nil
}

// envDuration accepts both a Go duration ("90s") and a bare number of seconds
// ("90"), because operators write both.
func envDuration(name string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	if secs, err := strconv.Atoi(raw); err == nil {
		return time.Duration(secs) * time.Second
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return fallback
	}
	return d
}

func envLogLevel(name string, fallback slog.Level) slog.Level {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return fallback
	}
}

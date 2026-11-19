package main

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

// env seeds the environment for one config test and restores it afterwards.
func env(t *testing.T, kv map[string]string) {
	t.Helper()
	t.Setenv("NS_LISTEN_ADDR", "")
	t.Setenv("NS_STATUS_ADDR", "")
	t.Setenv("NS_REGION", "")
	t.Setenv("NS_NET_ID", "")
	t.Setenv("NS_DATABASE_URL", "")
	t.Setenv("NS_DOWNLINK_POWER_DBM", "")
	t.Setenv("NS_DEVICE_STATUS_EVERY", "")
	t.Setenv("NS_GATEWAY_TIMEOUT", "")
	t.Setenv("NS_LOG_LEVEL", "")
	t.Setenv("NS_WEBHOOK_URLS", "")
	t.Setenv("NS_WEBHOOK_TOKEN", "")
	t.Setenv("NS_API_ADDR", "")
	t.Setenv("NS_API_TOKEN", "")
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	env(t, nil)
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.listenAddr != ":6858" {
		t.Errorf("listenAddr = %q, want :6858 (the packet-forwarder port)", cfg.listenAddr)
	}
	if cfg.region != "EU868" || cfg.netID.String() != "000000" {
		t.Errorf("region/NetID = %s/%s", cfg.region, cfg.netID)
	}
	if cfg.downlinkPowerdBm != 14 {
		t.Errorf("downlinkPowerdBm = %d, want 14", cfg.downlinkPowerdBm)
	}
	if cfg.gatewayTimeout != 3*time.Minute {
		t.Errorf("gatewayTimeout = %v, want 3m", cfg.gatewayTimeout)
	}
	if cfg.logLevel != slog.LevelInfo {
		t.Errorf("logLevel = %v, want INFO", cfg.logLevel)
	}
	if cfg.databaseURL != "" {
		t.Errorf("databaseURL = %q, want empty for the in-memory default", cfg.databaseURL)
	}
}

func TestLoadConfigRejectsBadValuesAtStartup(t *testing.T) {
	cases := []struct {
		name  string
		kv    map[string]string
		wants string
	}{
		{"bad NetID", map[string]string{"NS_NET_ID": "nothex"}, "NS_NET_ID"},
		{"short NetID", map[string]string{"NS_NET_ID": "0102"}, "NS_NET_ID"},
		{"unknown band", map[string]string{"NS_REGION": "AU915"}, "NS_REGION"},
		{"power too high", map[string]string{"NS_DOWNLINK_POWER_DBM": "39"}, "NS_DOWNLINK_POWER_DBM"},
		{"power not a number", map[string]string{"NS_DOWNLINK_POWER_DBM": "fourteen"}, "NS_DOWNLINK_POWER_DBM"},
		{"negative poll", map[string]string{"NS_DEVICE_STATUS_EVERY": "-1"}, "NS_DEVICE_STATUS_EVERY"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env(t, tc.kv)
			_, err := loadConfig()
			if err == nil {
				t.Fatalf("loadConfig accepted %v", tc.kv)
			}
			// The message must name the variable: inside a container it is the
			// only thing an operator reads.
			if !strings.Contains(err.Error(), tc.wants) {
				t.Errorf("err = %q, want it to name %s", err, tc.wants)
			}
		})
	}
}

func TestLoadConfigAcceptsRealisticDeployment(t *testing.T) {
	env(t, map[string]string{
		"NS_LISTEN_ADDR":         "0.0.0.0:6858",
		"NS_STATUS_ADDR":         "127.0.0.1:8090",
		"NS_REGION":              "AS923-3",
		"NS_NET_ID":              "010203",
		"NS_DATABASE_URL":        "postgres://ns:ns@db:5432/ns?sslmode=disable",
		"NS_DOWNLINK_POWER_DBM":  "16",
		"NS_DEVICE_STATUS_EVERY": "20",
		"NS_GATEWAY_TIMEOUT":     "90",
		"NS_LOG_LEVEL":           "debug",
	})
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.region != "AS923-3" || cfg.netID.String() != "010203" {
		t.Errorf("region/NetID = %s/%s", cfg.region, cfg.netID)
	}
	if cfg.downlinkPowerdBm != 16 || cfg.deviceStatusEvery != 20 {
		t.Errorf("power/poll = %d/%d", cfg.downlinkPowerdBm, cfg.deviceStatusEvery)
	}
	// A bare number means seconds, which is what an .env file usually means.
	if cfg.gatewayTimeout != 90*time.Second {
		t.Errorf("gatewayTimeout = %v, want 90s", cfg.gatewayTimeout)
	}
	if cfg.logLevel != slog.LevelDebug {
		t.Errorf("logLevel = %v, want DEBUG", cfg.logLevel)
	}
}

func TestEnvDurationAcceptsBothSpellings(t *testing.T) {
	t.Setenv("T_SECONDS", "45")
	t.Setenv("T_GO_DURATION", "1m30s")
	t.Setenv("T_EMPTY", "")
	t.Setenv("T_JUNK", "soon")

	if got := envDuration("T_SECONDS", time.Minute); got != 45*time.Second {
		t.Errorf("bare seconds = %v, want 45s", got)
	}
	if got := envDuration("T_GO_DURATION", time.Minute); got != 90*time.Second {
		t.Errorf("Go duration = %v, want 90s", got)
	}
	if got := envDuration("T_EMPTY", time.Minute); got != time.Minute {
		t.Errorf("empty = %v, want the fallback", got)
	}
	// Unparseable input falls back rather than killing the process: a duration
	// typo must not take the network server down.
	if got := envDuration("T_JUNK", time.Minute); got != time.Minute {
		t.Errorf("junk = %v, want the fallback", got)
	}
}

func TestLoadConfigWebhooks(t *testing.T) {
	env(t, map[string]string{
		"NS_WEBHOOK_URLS":  "https://a.example.test/hook, http://localhost:9099/b ,, ",
		"NS_WEBHOOK_TOKEN": "shared-secret",
	})
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	// The trailing comma and the blank entry must not become endpoints.
	if len(cfg.webhookURLs) != 2 {
		t.Fatalf("webhookURLs = %q, want two entries", cfg.webhookURLs)
	}
	if cfg.webhookURLs[0] != "https://a.example.test/hook" ||
		cfg.webhookURLs[1] != "http://localhost:9099/b" {
		t.Errorf("webhookURLs = %q", cfg.webhookURLs)
	}
	if cfg.webhookToken != "shared-secret" {
		t.Errorf("webhookToken = %q", cfg.webhookToken)
	}
}

func TestLoadConfigRejectsUnusableWebhookURLs(t *testing.T) {
	for _, raw := range []string{"not a url", "ftp://example.test/hook", "http:///no-host"} {
		env(t, map[string]string{"NS_WEBHOOK_URLS": raw, "NS_WEBHOOK_TOKEN": "x"})
		if _, err := loadConfig(); err == nil {
			t.Errorf("NS_WEBHOOK_URLS=%q was accepted", raw)
		} else if !strings.Contains(err.Error(), "NS_WEBHOOK_URLS") {
			t.Errorf("error %q must name the variable to fix", err)
		}
	}
}

// TestLoadConfigNoWebhooksIsTheDefault: nothing is configured, nothing runs.
func TestLoadConfigNoWebhooksIsTheDefault(t *testing.T) {
	env(t, nil)
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if len(cfg.webhookURLs) != 0 {
		t.Errorf("webhookURLs = %q, want none", cfg.webhookURLs)
	}
	if cfg.apiToken != "" {
		t.Errorf("apiToken = %q, want empty so the API stays disabled", cfg.apiToken)
	}
	if cfg.apiAddr != ":8091" {
		t.Errorf("apiAddr = %q, want :8091", cfg.apiAddr)
	}
}

func TestLoadConfigRejectsAShortAPIToken(t *testing.T) {
	env(t, map[string]string{"NS_API_TOKEN": "tooshort"})
	_, err := loadConfig()
	if err == nil {
		t.Fatal("a 16-character floor on a token that guards radio transmission must be enforced")
	}
	if !strings.Contains(err.Error(), "NS_API_TOKEN") {
		t.Errorf("error %q must name the variable to fix", err)
	}

	// Exactly at the floor it is accepted, and a long one is the normal case.
	for _, raw := range []string{"0123456789abcdef", "0123456789abcdef0123456789abcdef"} {
		env(t, map[string]string{"NS_API_TOKEN": raw})
		cfg, err := loadConfig()
		if err != nil {
			t.Fatalf("NS_API_TOKEN=%q: %v", raw, err)
		}
		if cfg.apiToken != raw {
			t.Errorf("apiToken = %q, want %q", cfg.apiToken, raw)
		}
	}
}

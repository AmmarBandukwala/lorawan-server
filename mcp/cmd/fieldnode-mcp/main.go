// Command fieldnode-mcp serves the Fieldnode LoRaWAN network server to an MCP
// client.
//
// It is a thin translation layer on purpose. The network server decides what a
// valid device and a legal transmission are; this process turns its HTTP API into
// tools and adds nothing to those rules. Everything it exposes is a read — see the
// package comment in internal/tools for what that excludes and why.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fieldnode/lorawan-server/mcp/internal/audit"
	"github.com/fieldnode/lorawan-server/mcp/internal/config"
	"github.com/fieldnode/lorawan-server/mcp/internal/nsclient"
	"github.com/fieldnode/lorawan-server/mcp/internal/tools"
)

func main() {
	// stdout is the protocol channel. Every log line, including the ones written
	// before anything else can fail, goes to stderr; one stray print on stdout and
	// the client's JSON framing breaks in a way that is miserable to diagnose.
	logger := newLogger()

	if err := run(logger); err != nil {
		logger.Error("fieldnode-mcp stopped", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	profile, err := tools.ParseProfile(os.Getenv("MCP_PROFILE"))
	if err != nil {
		return err
	}
	client, err := nsclient.New(cfg.APIURL, cfg.Token, nsclient.WithStatusURL(cfg.StatusURL))
	if err != nil {
		return err
	}

	// Built through tools.New rather than assembled here, so that what the tests
	// assert a client receives is what this process actually sends.
	server, names, err := tools.New(&tools.Deps{NS: client, Audit: audit.New(logger)},
		profile, cfg.Version, logger)
	if err != nil {
		return err
	}

	// Reported rather than assumed: which token this process holds, and therefore
	// what it can read and write, is the first question to answer when an agent's
	// access does not match what someone expected. The token itself is never logged.
	//
	// The profile is the line to read when something was allowed that should not
	// have been, so it is logged at Info even at the default level.
	logger.Info("fieldnode-mcp ready",
		"version", cfg.Version,
		"profile", profile,
		"writes", profile == tools.ProfileOperate,
		"tools", len(names),
		"api", cfg.APIURL,
		"status", cfg.StatusURL,
	)
	if profile == tools.ProfileOperate {
		logger.Warn("fieldnode-mcp is running with writes enabled: this process can " +
			"queue and discard radio frames and change device configuration. Confirm your " +
			"MCP host prompts before approving tool calls, because these annotations are " +
			"hints and nothing here can force a prompt.")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Stdio only. The process is spawned by the host, so whoever can start it is
	// whoever can use the token; there is no second access control to get wrong.
	// A network transport needs its own authentication and arrives later.
	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil {
		return err
	}
	logger.Info("fieldnode-mcp: client disconnected")
	return nil
}

// newLogger builds the stderr logger before the configuration is read, because a
// configuration failure has to be reported through it.
func newLogger() *slog.Logger {
	level := slog.LevelInfo
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MCP_LOG_LEVEL"))) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)
	return logger
}

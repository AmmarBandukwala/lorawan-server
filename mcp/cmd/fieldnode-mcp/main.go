// Command fieldnode-mcp serves the Fieldnode LoRaWAN network server to an MCP
// client.
//
// It is a thin translation layer on purpose. The network server decides what a valid
// device and a legal transmission are; this process turns its HTTP API into tools and
// adds nothing to those rules. Which tools exist is fixed at startup by MCP_PROFILE —
// see the package comment in internal/tools for what no profile offers, and why.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fieldnode/lorawan-server/mcp/internal/audit"
	"github.com/fieldnode/lorawan-server/mcp/internal/config"
	"github.com/fieldnode/lorawan-server/mcp/internal/httpserver"
	"github.com/fieldnode/lorawan-server/mcp/internal/nsclient"
	"github.com/fieldnode/lorawan-server/mcp/internal/tools"
)

func main() {
	// stdout is the protocol channel. Every log line, including the ones written
	// before anything else can fail, goes to stderr; one stray print on stdout and
	// the client's JSON framing breaks in a way that is miserable to diagnose.
	logger := newLogger()

	// The container health check. Handled before configuration is read, because a
	// probe must be able to answer even when the environment is wrong — and when it
	// is, the answer is exit 2 with a reason rather than a silent failure.
	if healthzFlag() {
		os.Exit(runHealthz(os.Getenv("MCP_HTTP_ADDR"), 3*time.Second))
	}

	if err := run(logger); err != nil {
		logger.Error("fieldnode-mcp stopped", "err", err)
		os.Exit(1)
	}
}

// healthzFlag reports whether -healthz was passed.
//
// Parsed by hand rather than with the flag package because this process takes no
// other flags and the flag package exits the process on an unrecognised argument —
// which is right for a CLI and wrong here, where an MCP host is the one supplying
// the command line and a stray argument must not kill the server.
func healthzFlag() bool {
	for _, arg := range os.Args[1:] {
		if arg == "-healthz" || arg == "--healthz" {
			return true
		}
	}
	return false
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

	if cfg.HTTPAddr != "" {
		return serveHTTP(ctx, server, cfg, logger)
	}

	// Stdio, which is the default: the process is spawned by the host, so whoever
	// can start it is whoever can use the token, and there is no second access
	// control to get wrong.
	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil {
		return err
	}
	logger.Info("fieldnode-mcp: client disconnected")
	return nil
}

// serveHTTP runs the streamable transport. The MCP server is the same object stdio
// would have used, so a deployment that switches transports does not switch
// behaviour.
func serveHTTP(ctx context.Context, server *mcp.Server, cfg config.Config, logger *slog.Logger) error {
	handler, err := httpserver.NewHandler(server, httpserver.Config{
		BindAddr:  cfg.HTTPAddr,
		AuthToken: cfg.AuthToken,
		Logger:    logger,
	})
	if err != nil {
		return err
	}
	httpServer := httpserver.NewServer(cfg.HTTPAddr, handler)

	logger.Info("fieldnode-mcp listening", "addr", cfg.HTTPAddr, "endpoint", httpserver.Endpoint)

	// Shutdown on the same signal that stops stdio. The timeout is generous because
	// a tool call in flight is waiting on a network server, not on this process;
	// cutting it off early turns a slow answer into a dropped connection.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		// A listener that fails to bind must not report success: a supervisor that
		// sees a clean exit will restart it into the same occupied port.
		return err
	case <-ctx.Done():
		logger.Info("fieldnode-mcp shutting down the HTTP listener")
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
		return nil
	}
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

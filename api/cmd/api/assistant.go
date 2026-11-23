package main

import (
	"context"
	"log"
	"os"
	"strings"
	"time"

	"github.com/fieldnode/lorawan-server/api/internal/assistant"
	"github.com/fieldnode/lorawan-server/api/internal/httpapi"
	"github.com/fieldnode/lorawan-server/api/internal/llm"
	"github.com/fieldnode/lorawan-server/api/internal/mcpclient"
)

// vocabularyURI is the listener's resource holding every event reason and what each
// one means.
//
// The string is repeated from the mcp module rather than imported because api and mcp
// are separate Go modules and mcp's packages are internal to it. Nothing at compile
// time notices if one side renames it, so mcp/README.md lists the URIs, and the
// assistant degrades to answering without the reference text rather than failing —
// which is the correct outcome for a rename and a loud one for a wrong URL.
const vocabularyURI = "fieldnode://event-vocabulary"

// vocabularyBudget bounds the startup read. It is short because it is a convenience:
// waiting longer would delay the console serving its dashboard for a feature that is
// not needed until someone asks a question.
const vocabularyBudget = 5 * time.Second

// buildAssistant wires the assistant, or returns nil when this deployment has no
// model to ask.
//
// A missing configuration is a normal state and is logged as one: the assistant is an
// addition to a console that is otherwise complete, and refusing to start because
// LLM_BASE_URL is unset would take the dashboard down with it. A half-configured one
// is different and does stop the process — see the fatal below.
func buildAssistant(ctx context.Context) (httpapi.Assistant, httpapi.AssistantInfo) {
	baseURL := strings.TrimSpace(os.Getenv("LLM_BASE_URL"))
	model := strings.TrimSpace(os.Getenv("LLM_MODEL"))
	mcpURL := strings.TrimSpace(os.Getenv("MCP_URL"))

	switch {
	case baseURL == "" && model == "":
		log.Print("LLM_BASE_URL and LLM_MODEL are not set: the assistant will answer " +
			"nothing and the page will say so. Set both to enable it")
		return nil, httpapi.AssistantInfo{}
	case baseURL == "" || model == "":
		// Half a configuration. Starting anyway would leave a page with an assistant
		// panel that fails on every question, which reads as a bug in the console
		// rather than as a missing variable.
		log.Fatal("the assistant needs both LLM_BASE_URL and LLM_MODEL; only one is set")
	}
	if mcpURL == "" {
		// The assistant's only way to look at anything is the listener. Without one
		// it would be a model answering about a network it cannot see, which is the
		// specific failure the whole design is arranged to prevent, so it is off
		// rather than on and wrong.
		log.Print("MCP_URL is not set: the assistant has nothing to read and stays off")
		return nil, httpapi.AssistantInfo{}
	}

	modelClient, err := llm.New(llm.Config{
		BaseURL: baseURL,
		Model:   model,
		APIKey:  strings.TrimSpace(os.Getenv("LLM_API_KEY")),
	})
	if err != nil {
		log.Fatalf("assistant model endpoint: %v", err)
	}

	listener, err := mcpclient.New(mcpclient.Config{
		URL:   mcpEndpoint(mcpURL),
		Token: os.Getenv("MCP_AUTH_TOKEN"),
	})
	if err != nil {
		log.Fatalf("assistant MCP listener: %v", err)
	}

	vocabulary := readVocabulary(ctx, listener)

	service, err := assistant.New(assistant.Config{
		// A session per question, not one for the life of the process: an MCP
		// session belongs to a conversation, and holding one open would mean a
		// listener restart leaves the assistant broken until the console restarts
		// too.
		Dial: func(ctx context.Context) (assistant.Network, func(), error) {
			session, err := listener.Open(ctx)
			if err != nil {
				return nil, nil, err
			}
			return session, func() {
				if err := session.Close(); err != nil {
					log.Printf("closing the assistant's MCP session: %v", err)
				}
			}, nil
		},
		Model:      modelClient,
		Vocabulary: vocabulary,
		ModelID:    modelClient.Model(),
		Endpoint:   modelClient.Endpoint(),
	})
	if err != nil {
		log.Fatalf("assistant: %v", err)
	}

	log.Printf("assistant answering with %s at %s, reading the network through %s",
		modelClient.Model(), modelClient.Endpoint(), listener.Endpoint())
	return service, httpapi.AssistantInfo{
		Model:         modelClient.Model(),
		Endpoint:      modelClient.Endpoint(),
		HasVocabulary: vocabulary != "",
	}
}

// mcpEndpoint turns the listener's base URL into its MCP path.
//
// MCP_URL already exists and points at the base, because the reachability probe needs
// the base in order to ask /healthz. Requiring a second variable holding the same host
// with /mcp appended would be two things to keep in agreement, so the path is appended
// here unless it is already there.
func mcpEndpoint(raw string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(raw), "/")
	if strings.HasSuffix(trimmed, "/mcp") {
		return trimmed
	}
	return trimmed + "/mcp"
}

// readVocabulary fetches the reason vocabulary once, at startup.
//
// Failure is logged and tolerated. The listener sits behind a compose profile that is
// off by default, so a console that refused to start without it could not be brought
// up in the order most people bring a deployment up. The assistant answers without the
// reference text, and the page says it has none.
func readVocabulary(ctx context.Context, listener *mcpclient.Client) string {
	startup, cancel := context.WithTimeout(ctx, vocabularyBudget)
	defer cancel()

	session, err := listener.Open(startup)
	if err != nil {
		log.Printf("assistant: cannot reach %s to read the event vocabulary (%v); it "+
			"will answer without the reference text, so reason strings will be "+
			"paraphrased rather than quoted", listener.Endpoint(), err)
		return ""
	}
	defer func() {
		if err := session.Close(); err != nil {
			log.Printf("closing the startup MCP session: %v", err)
		}
	}()

	text, err := session.Read(startup, vocabularyURI)
	if err != nil {
		log.Printf("assistant: %s could not be read from %s (%v); reason strings will "+
			"be paraphrased rather than quoted", vocabularyURI, listener.Endpoint(), err)
		return ""
	}
	return text
}

package assistant

import (
	"strings"

	"github.com/fieldnode/lorawan-server/api/internal/llm"
	"github.com/fieldnode/lorawan-server/api/internal/mcpclient"
)

// readOnlyTools keeps only what the listener itself declared a read.
//
// This is the access control, and it sits here rather than in the prompt because a
// prompt is a request and this is a fact about the tool list the model is shown. A
// model cannot be persuaded into not calling a tool it was never told exists.
//
// The filter is by annotation, which is the listener's claim rather than a proof. That
// is sound in one direction only: this deployment talks to its own MCP service, whose
// annotations are written in this repository and covered by its tests. Pointing the
// assistant at a third-party server would make this the security boundary, and it is
// not built to be one.
func readOnlyTools(tools []mcpclient.Tool) []mcpclient.Tool {
	out := make([]mcpclient.Tool, 0, len(tools))
	for _, tool := range tools {
		if tool.ReadOnly {
			out = append(out, tool)
		}
	}
	return out
}

// toolSpecs converts to the shape the chat endpoint expects. The schema travels
// untouched: it is the only thing telling the model which arguments a tool takes, and
// dropping it produces confident calls with invented parameter names.
func toolSpecs(tools []mcpclient.Tool) []llm.Tool {
	specs := make([]llm.Tool, 0, len(tools))
	for _, tool := range tools {
		specs = append(specs, llm.Tool{
			Type: "function",
			Function: llm.ToolSpec{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.Schema,
			},
		})
	}
	return specs
}

// systemPrompt is the orientation the model receives before every question.
//
// It is rebuilt each time rather than cached, because the tool list and the retrieved
// context change between questions. It is also the only chance to prevent the three
// mistakes a model makes on a live network: answering from general LoRaWAN knowledge
// about a build that differs from the standard, reading an absent field as a zero, and
// treating a queued frame as a sent one.
func (s *Service) systemPrompt(contextBlock string, historyTrimmed bool) string {
	var b strings.Builder
	b.WriteString(guidance)

	if historyTrimmed {
		b.WriteString(trimmedHistoryNote)
	}
	if contextBlock != "" {
		b.WriteString(referenceHeader)
		b.WriteString(contextBlock)
		b.WriteString("\n\n")
	}
	if vocabulary := s.vocabularyBlock(); vocabulary != "" {
		b.WriteString(vocabularyHeader)
		b.WriteString(vocabulary)
	}
	return b.String()
}

// vocabularyBlock renders the event vocabulary read from the listener, capped.
//
// It is included because it is the one reference text that must not be paraphrased:
// these are the exact strings the network writes, and a model that has them will quote
// them correctly instead of approximating. It is capped because the vocabulary is long
// and a small context model spends it on nothing else.
func (s *Service) vocabularyBlock() string {
	const maxVocabulary = 6000
	text := strings.TrimSpace(s.kb.vocabulary)
	if text == "" {
		return ""
	}
	if len(text) > maxVocabulary {
		text = text[:maxVocabulary] + "\n… (the vocabulary was cut short; " +
			"use explain_event_reason for any reason not shown)"
	}
	return text + "\n\n"
}

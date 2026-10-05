package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/toolrunner"
)

// Tool is one thing the model may call during a run. Act marks a tool that
// changes something; those exist only in act mode.
type Tool struct {
	Name        string
	Description string
	Schema      string // JSON schema of the input object
	Act         bool
	Call        func(ctx context.Context, input json.RawMessage) (string, error)
}

// RunResult is what a run produced and what it cost.
type RunResult struct {
	Text         string
	InputTokens  int64
	OutputTokens int64
	Iterations   int
}

// Model runs one diagnosis: a system prompt, the alert, and tools to call
// until it has a report. The interface exists so the service can be tested
// with a scripted model.
type Model interface {
	Run(ctx context.Context, system, user string, tools []Tool) (RunResult, error)
}

// defaultModel is the current Opus. Adaptive thinking is always on for it;
// effort is set explicitly because its default (medium) is a choice here,
// not an accident: a diagnosis is a handful of tool calls, not a long task.
const defaultModel = "claude-opus-5-5"

type claudeModel struct {
	client        anthropic.Client
	model         string
	maxIterations int
}

func newClaudeModel(model string, maxIterations int) *claudeModel {
	if model == "" {
		model = defaultModel
	}
	// NewClient reads ANTHROPIC_API_KEY from the environment.
	return &claudeModel{client: anthropic.NewClient(), model: model, maxIterations: maxIterations}
}

func (m *claudeModel) Run(ctx context.Context, system, user string, tools []Tool) (RunResult, error) {
	var res RunResult
	betaTools := make([]anthropic.BetaTool, 0, len(tools))
	for _, t := range tools {
		t := t
		bt, err := toolrunner.NewBetaToolFromBytes(t.Name, t.Description, []byte(t.Schema),
			func(ctx context.Context, input json.RawMessage) (anthropic.BetaToolResultBlockParamContentUnion, error) {
				out, err := t.Call(ctx, input)
				if err != nil {
					// Returned as an error tool_result; the model must report
					// it, not work around it.
					return anthropic.BetaToolResultBlockParamContentUnion{}, err
				}
				return anthropic.BetaToolResultBlockParamContentUnion{OfText: &anthropic.BetaTextBlockParam{Text: out}}, nil
			})
		if err != nil {
			return res, fmt.Errorf("tool %s: %w", t.Name, err)
		}
		betaTools = append(betaTools, bt)
	}

	runner := m.client.Beta.Messages.NewToolRunner(betaTools, anthropic.BetaToolRunnerParams{
		BetaMessageNewParams: anthropic.BetaMessageNewParams{
			Model:     m.model,
			MaxTokens: 16000,
			// The playbooks never change between runs: cache them.
			System: []anthropic.BetaTextBlockParam{{Text: system, CacheControl: anthropic.NewBetaCacheControlEphemeralParam()}},
			Messages: []anthropic.BetaMessageParam{
				anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(user)),
			},
			OutputConfig: anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffortMedium},
			// A safety classifier can decline a request about failing
			// integrations by mistake; let the API re-serve it on another
			// model instead of leaving the alert without a report.
			Fallbacks: anthropic.BetaFallbacksParamOfDefault(),
			Betas:     []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01},
		},
		MaxIterations: m.maxIterations,
	})

	var last *anthropic.BetaMessage
	for msg, err := range runner.All(ctx) {
		if err != nil {
			return res, err
		}
		res.Iterations++
		res.InputTokens += msg.Usage.InputTokens + msg.Usage.CacheReadInputTokens + msg.Usage.CacheCreationInputTokens
		res.OutputTokens += msg.Usage.OutputTokens
		last = msg
	}
	if last == nil {
		return res, errors.New("the model returned no message")
	}
	if last.StopReason == anthropic.BetaStopReasonRefusal {
		return res, errors.New("the model declined the request")
	}
	var text strings.Builder
	for _, block := range last.Content {
		if b, ok := block.AsAny().(anthropic.BetaTextBlock); ok {
			text.WriteString(b.Text)
		}
	}
	res.Text = strings.TrimSpace(text.String())
	if res.Text == "" {
		return res, fmt.Errorf("the model produced no report (stop reason %q)", last.StopReason)
	}
	return res, nil
}

// costUSD estimates a run's cost at the model's list price ($ per MTok).
func costUSD(r RunResult) float64 {
	const inPerMTok, outPerMTok = 4.0, 20.0 // claude-opus-5-5
	return float64(r.InputTokens)/1e6*inPerMTok + float64(r.OutputTokens)/1e6*outPerMTok
}

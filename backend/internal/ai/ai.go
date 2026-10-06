// Package ai adapts a chat-completions model to ImpactPay's two narrative hooks:
// the verification summary shown to admins, and the donor-facing answer to
// "what has my money accomplished?".
//
// The model's authority is deliberately narrow. Both hooks receive facts that
// have already been computed deterministically and are asked only to rephrase
// them. Verification numbers, checks, recommendations and citations are never
// taken from the model, and every call degrades silently to the deterministic
// text if the model is unavailable, slow, or returns something unusable. That is
// why DEMO_MODE reproduces the same verdicts whether or not a model is configured.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/impactpay/backend/internal/core"
	"github.com/impactpay/backend/internal/models"
)

// Config configures the model client.
type Config struct {
	APIKey    string
	BaseURL   string
	Model     string
	Timeout   time.Duration
	MaxTokens int
}

// Client is an OpenAI-compatible chat client.
type Client struct {
	cfg  Config
	http *http.Client
}

// New creates a client. An empty API key yields a client whose Enabled is false.
func New(cfg Config) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.openai.com/v1"
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.Model == "" {
		cfg.Model = "gpt-4o-mini"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 45 * time.Second
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 700
	}
	return &Client{cfg: cfg, http: &http.Client{Timeout: cfg.Timeout}}
}

// Enabled reports whether a key is configured.
func (c *Client) Enabled() bool { return c != nil && strings.TrimSpace(c.cfg.APIKey) != "" }

// Model returns the configured model name.
func (c *Client) Model() string {
	if c == nil {
		return ""
	}
	return c.cfg.Model
}

const groundingRule = `You are the writing layer of ImpactPay, an outcome-based funding platform.

Hard rules:
1. Use ONLY the facts in the JSON payload. Never invent a number, name, date, or artefact.
2. Never change a verdict, status, quantity or recommendation; restate them verbatim.
3. If a fact is absent, do not mention it.
4. Be concise, plain and professional. No marketing language, no exclamation marks.
5. Do not address the reader as "you" in the verification summary; in the donor answer, "you" is the donor.`

// Narrate rewrites the verification summary for an admin.
func (c *Client) Narrate(ctx context.Context, in VerificationFacts, res models.VerificationResult) (string, string, string) {
	fallback := res.Summary
	if !c.Enabled() {
		return fallback, "deterministic", "impactpay-verifier-v1"
	}

	payload := map[string]any{
		"project":           in.ProjectTitle,
		"milestone":         in.MilestoneTitle,
		"unit_label":        res.UnitLabel,
		"claim":             in.Claim,
		"required_quantity": res.RequiredQuantity,
		"verified_quantity": res.VerifiedQuantity,
		"evidence_count":    res.EvidenceCount,
		"usable_count":      res.UsableCount,
		"duplicate_count":   res.DuplicateCount,
		"missing_evidence":  res.MissingEvidence,
		"inconsistencies":   res.Inconsistencies,
		"financial":         res.Financial,
		"recommendation":    res.Recommendation,
		"confidence":        res.Confidence,
	}

	prompt := "Write a 2-4 sentence verification summary for an ImpactPay administrator from these facts. " +
		"State the claimed quantity, what the evidence supports, what is missing or duplicated, the financial reconciliation, " +
		"and the recommendation. The recommendation and quantities must be reproduced exactly.\n\n" +
		core.JSONString(payload)

	out, err := c.chat(ctx, groundingRule, prompt)
	if err != nil || strings.TrimSpace(out) == "" {
		return fallback, "deterministic", "impactpay-verifier-v1"
	}
	return strings.TrimSpace(out), "llm+numeric", c.cfg.Model
}

// Refine rewrites a donor answer.
func (c *Client) Refine(ctx context.Context, question, deterministic string, facts map[string]any) (string, string, string, bool) {
	if !c.Enabled() {
		return "", "", "", false
	}
	prompt := "Rewrite the following grounded answer so it reads naturally, keeping every fact, number and caveat identical. " +
		"Answer in at most 6 sentences.\n\nQUESTION: " + question +
		"\n\nSTORED FACTS (authoritative): " + core.JSONString(facts) +
		"\n\nCURRENT ANSWER: " + deterministic

	out, err := c.chat(ctx, groundingRule, prompt)
	if err != nil || strings.TrimSpace(out) == "" {
		return "", "", "", false
	}
	return strings.TrimSpace(out), "llm+grounded", c.cfg.Model, true
}

// VerificationFacts is the minimal project/milestone context passed to the model.
type VerificationFacts struct {
	ProjectTitle   string
	MilestoneTitle string
	Claim          string
}

// ---------------------------------------------------------------------------
// transport
// ---------------------------------------------------------------------------

func (c *Client) chat(ctx context.Context, system, user string) (string, error) {
	body := map[string]any{
		"model": c.cfg.Model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"temperature":     0,
		"max_tokens":      c.cfg.MaxTokens,
		"response_format": map[string]string{"type": "text"},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.cfg.BaseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("ai: chat completions returned %s: %s", resp.Status, core.Truncate(string(payload), 300))
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return "", err
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("ai: response contained no choices")
	}
	return parsed.Choices[0].Message.Content, nil
}

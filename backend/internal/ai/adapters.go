package ai

import (
	"context"

	"github.com/impactpay/backend/internal/impact"
	"github.com/impactpay/backend/internal/models"
	"github.com/impactpay/backend/internal/verification"
)

// VerificationNarrator adapts the model client to verification.Narrator.
type VerificationNarrator struct {
	client *Client
}

// NewVerificationNarrator returns a narrator backed by the model client.
func NewVerificationNarrator(c *Client) *VerificationNarrator {
	return &VerificationNarrator{client: c}
}

// Narrate implements verification.Narrator.
func (n *VerificationNarrator) Narrate(ctx context.Context, in verification.Input, res models.VerificationResult) (string, string, string) {
	claim := ""
	if len(res.Claimed) > 0 {
		claim = res.Claimed[0].Label
	}
	return n.client.Narrate(ctx, VerificationFacts{
		ProjectTitle:   in.Project.Title,
		MilestoneTitle: in.Milestone.Title,
		Claim:          claim,
	}, res)
}

// ImpactAnswerer adapts the model client to impact.Answerer.
type ImpactAnswerer struct {
	client *Client
}

// NewImpactAnswerer returns an answerer backed by the model client.
func NewImpactAnswerer(c *Client) *ImpactAnswerer { return &ImpactAnswerer{client: c} }

// Refine implements impact.Answerer.
func (a *ImpactAnswerer) Refine(ctx context.Context, question, deterministic string, facts map[string]any) (string, string, string, bool) {
	return a.client.Refine(ctx, question, deterministic, facts)
}

var (
	_ verification.Narrator = (*VerificationNarrator)(nil)
	_ impact.Answerer       = (*ImpactAnswerer)(nil)
)

package httpapi

import (
	"net/http"

	"github.com/impactpay/backend/internal/impact"
)

// handleTrace builds the "Trace My Impact" Proof Graph for one contribution.
func (s *Server) handleTrace(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	d, err := s.ownedDonation(ctx, pathValue(r, "donationId"))
	if err != nil {
		s.handleServiceError(w, err, "trace donation")
		return
	}
	graph, err := s.Impact.Trace(ctx, d.ID)
	if err != nil {
		s.handleServiceError(w, err, "build proof graph")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"trace": graph})
}

type askRequest struct {
	DonationID string `json:"donation_id"`
	Question   string `json:"question"`
}

// handleAsk answers a donor's question using only stored records. The optional
// language model may rephrase the answer, but it cannot change the figures, the
// citations or the grounding flag.
func (s *Server) handleAsk(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req askRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	d, err := s.ownedDonation(ctx, req.DonationID)
	if err != nil {
		s.handleServiceError(w, err, "ask donation")
		return
	}
	answer, err := s.Impact.Ask(ctx, d.ID, req.Question, s.Answerer)
	if err != nil {
		s.handleServiceError(w, err, "grounded answer")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"answer": answer})
}

func (s *Server) handleSuggestedQuestions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"questions": impact.SuggestedQuestions})
}

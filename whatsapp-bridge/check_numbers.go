package main

// Phone number lookup exposed over the authenticated REST API.
//
// POST /api/check {"phones": ["5531977776666", ...]} asks WhatsApp which numbers
// are registered and returns the canonical phone JID of each one. Callers must
// not build the JID from the number they have: Brazilian mobiles registered
// before the extra 9 was added are often still 553177776666 on WhatsApp, while
// contact lists carry today's 5531977776666. Sending to a number that is not on
// WhatsApp can also "succeed" without ever being delivered.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types"
)

const (
	maxCheckPhones = 50
	checkTimeout   = 20 * time.Second
)

type CheckNumbersRequest struct {
	Phones []string `json:"phones"` // country code + number, digits only
}

type CheckNumberResult struct {
	Phone  string `json:"phone"`
	Exists bool   `json:"exists"`
	JID    string `json:"jid"` // canonical phone JID (or the LID when WhatsApp hides the number); empty if not registered
}

type CheckNumbersResponse struct {
	Success bool                `json:"success"`
	Message string              `json:"message,omitempty"`
	Results []CheckNumberResult `json:"results,omitempty"`
}

// numberChecker is client.IsOnWhatsApp; tests swap it for a fake.
type numberChecker func(ctx context.Context, phones []string) ([]types.IsOnWhatsAppResponse, error)

func writeCheckResponse(w http.ResponseWriter, status int, resp CheckNumbersResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
}

func isPhoneDigits(p string) bool {
	if len(p) < 8 || len(p) > 15 {
		return false
	}
	for _, c := range p {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func checkNumbersHandler(isConnected func() bool, check numberChecker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req CheckNumbersRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeCheckResponse(w, http.StatusBadRequest, CheckNumbersResponse{Message: "Invalid request format"})
			return
		}
		if len(req.Phones) == 0 || len(req.Phones) > maxCheckPhones {
			writeCheckResponse(w, http.StatusBadRequest, CheckNumbersResponse{
				Message: fmt.Sprintf("phones must have 1 to %d numbers", maxCheckPhones),
			})
			return
		}
		queries := make([]string, len(req.Phones))
		for i, p := range req.Phones {
			if !isPhoneDigits(p) {
				writeCheckResponse(w, http.StatusBadRequest, CheckNumbersResponse{
					Message: "phones must be 8 to 15 digits (country code + number, no +)",
				})
				return
			}
			queries[i] = "+" + p // IsOnWhatsApp wants the international format
		}
		if !isConnected() {
			writeCheckResponse(w, http.StatusServiceUnavailable, CheckNumbersResponse{Message: "Not connected to WhatsApp"})
			return
		}

		// Phone numbers are personal data: log only how many were checked.
		fmt.Printf("→ /api/check phones=%d\n", len(queries))
		ctx, cancel := context.WithTimeout(r.Context(), checkTimeout)
		defer cancel()
		found, err := check(ctx, queries)
		if err != nil {
			writeCheckResponse(w, http.StatusBadGateway, CheckNumbersResponse{Message: fmt.Sprintf("Lookup failed: %v", err)})
			return
		}

		byPhone := make(map[string]types.IsOnWhatsAppResponse, len(found))
		for _, f := range found {
			byPhone[strings.TrimPrefix(f.Query, "+")] = f
		}
		results := make([]CheckNumberResult, len(req.Phones))
		for i, p := range req.Phones {
			results[i] = CheckNumberResult{Phone: p}
			f, ok := byPhone[p]
			if !ok || !f.IsIn {
				continue
			}
			jid := f.PhoneNumber // with LID addressing, f.JID is the hidden LID
			if jid.IsEmpty() {
				jid = f.JID
			}
			if jid.IsEmpty() {
				continue
			}
			results[i].Exists = true
			results[i].JID = jid.String()
		}
		writeCheckResponse(w, http.StatusOK, CheckNumbersResponse{Success: true, Results: results})
	}
}

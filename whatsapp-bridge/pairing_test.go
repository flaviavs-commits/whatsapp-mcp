package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPairingStartsWaitingWithoutQR(t *testing.T) {
	s := newPairingStatus().snapshot()
	if s.State != "waiting_qr" || s.QR != "" || s.JID != "" {
		t.Fatalf("unexpected initial snapshot: %+v", s)
	}
}

func TestSetQRExposesLatestCode(t *testing.T) {
	p := newPairingStatus()
	p.setQR("a")
	p.setQR("b")
	s := p.snapshot()
	if s.QR != "b" || s.State != "waiting_qr" {
		t.Fatalf("unexpected snapshot: %+v", s)
	}
}

func TestSetConnectedClearsQRAndStoresJID(t *testing.T) {
	p := newPairingStatus()
	p.setQR("a")
	p.setConnected("5511999@s.whatsapp.net")
	s := p.snapshot()
	if s.QR != "" || s.JID != "5511999@s.whatsapp.net" || s.State != "connected" {
		t.Fatalf("unexpected snapshot: %+v", s)
	}
}

func TestSetTimedOutAndLoggedOut(t *testing.T) {
	p := newPairingStatus()
	p.setQR("a")
	p.setTimedOut()
	if s := p.snapshot(); s.State != "timed_out" || s.QR != "" {
		t.Fatalf("unexpected timed_out snapshot: %+v", s)
	}
	p.setQR("b")
	p.setLoggedOut()
	if s := p.snapshot(); s.State != "logged_out" || s.QR != "" {
		t.Fatalf("unexpected logged_out snapshot: %+v", s)
	}
}

func TestPairingHandlerReturnsJSON(t *testing.T) {
	p := newPairingStatus()
	p.setQR("qr-code")

	rec := httptest.NewRecorder()
	pairingHandler(p)(rec, httptest.NewRequest(http.MethodGet, "/api/pairing", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q", ct)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	for _, k := range []string{"state", "qr", "jid", "updated_at"} {
		if _, ok := body[k]; !ok {
			t.Fatalf("missing key %q in %v", k, body)
		}
	}
	if body["state"] != "waiting_qr" || body["qr"] != "qr-code" {
		t.Fatalf("unexpected body: %v", body)
	}
	if _, err := time.Parse(time.RFC3339, body["updated_at"].(string)); err != nil {
		t.Fatalf("updated_at not RFC3339: %v", err)
	}
}

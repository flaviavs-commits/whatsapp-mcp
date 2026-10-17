package main

// Pairing state exposed over the authenticated REST API.
//
// The QR code used to link a device is otherwise only printed to the
// terminal. pairingStatus records the latest pairing state so GET
// /api/pairing can hand it to a web UI.

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

const (
	pairingStateWaitingQR = "waiting_qr"
	pairingStateConnected = "connected"
	pairingStateTimedOut  = "timed_out"
	pairingStateLoggedOut = "logged_out"
)

type pairingSnapshot struct {
	State     string `json:"state"`
	QR        string `json:"qr"`
	JID       string `json:"jid"`
	UpdatedAt string `json:"updated_at"`
}

type pairingStatus struct {
	mu        sync.Mutex
	state     string
	qr        string
	jid       string
	updatedAt time.Time
}

// pairing is the process-wide pairing state shared by main and the REST server.
var pairing = newPairingStatus()

func newPairingStatus() *pairingStatus {
	return &pairingStatus{state: pairingStateWaitingQR, updatedAt: time.Now().UTC()}
}

func (p *pairingStatus) set(state, qr, jid string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.state = state
	p.qr = qr
	p.jid = jid
	p.updatedAt = time.Now().UTC()
}

func (p *pairingStatus) setQR(code string) { p.set(pairingStateWaitingQR, code, "") }

func (p *pairingStatus) setConnected(jid string) { p.set(pairingStateConnected, "", jid) }

func (p *pairingStatus) setTimedOut() { p.set(pairingStateTimedOut, "", "") }

func (p *pairingStatus) setLoggedOut() { p.set(pairingStateLoggedOut, "", "") }

func (p *pairingStatus) snapshot() pairingSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	return pairingSnapshot{
		State:     p.state,
		QR:        p.qr,
		JID:       p.jid,
		UpdatedAt: p.updatedAt.Format(time.RFC3339),
	}
}

func pairingHandler(p *pairingStatus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(p.snapshot())
	}
}

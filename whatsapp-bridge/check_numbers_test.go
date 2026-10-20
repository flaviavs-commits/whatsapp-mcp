package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/types"
)

func doCheck(t *testing.T, h http.HandlerFunc, method, body string) (*httptest.ResponseRecorder, CheckNumbersResponse) {
	t.Helper()
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(method, "/api/check", strings.NewReader(body)))
	var resp CheckNumbersResponse
	if rec.Body.Len() > 0 && strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("invalid JSON %q: %v", rec.Body.String(), err)
		}
	}
	return rec, resp
}

func connected() bool { return true }

func TestCheckNumbersReturnsCanonicalPhoneJID(t *testing.T) {
	var asked []string
	check := func(_ context.Context, phones []string) ([]types.IsOnWhatsAppResponse, error) {
		asked = phones
		return []types.IsOnWhatsAppResponse{
			{
				// Old Brazilian mobile: registered without the extra 9; LID addressing hides it behind a LID.
				Query:       "+5531977776666",
				IsIn:        true,
				JID:         types.NewJID("123456789", types.HiddenUserServer),
				PhoneNumber: types.NewJID("553177776666", types.DefaultUserServer),
			},
			{Query: "+5511900000000", IsIn: false},
			{Query: "+351912345678", IsIn: true, JID: types.NewJID("351912345678", types.DefaultUserServer)},
		}, nil
	}
	rec, resp := doCheck(t, checkNumbersHandler(connected, check), http.MethodPost,
		`{"phones": ["5531977776666", "5511900000000", "351912345678", "5521988887777"]}`)

	if rec.Code != http.StatusOK || !resp.Success {
		t.Fatalf("expected 200 success, got %d %+v", rec.Code, resp)
	}
	if want := []string{"+5531977776666", "+5511900000000", "+351912345678", "+5521988887777"}; !reflect.DeepEqual(asked, want) {
		t.Fatalf("expected international queries %v, got %v", want, asked)
	}
	want := []CheckNumberResult{
		{Phone: "5531977776666", Exists: true, JID: "553177776666@s.whatsapp.net"},
		{Phone: "5511900000000"},
		{Phone: "351912345678", Exists: true, JID: "351912345678@s.whatsapp.net"},
		{Phone: "5521988887777"}, // missing from the answer: not registered
	}
	if !reflect.DeepEqual(resp.Results, want) {
		t.Fatalf("unexpected results:\n got %+v\nwant %+v", resp.Results, want)
	}
}

func TestCheckNumbersRejectsBadRequests(t *testing.T) {
	never := func(context.Context, []string) ([]types.IsOnWhatsAppResponse, error) {
		t.Fatal("must not ask WhatsApp for a bad request")
		return nil, nil
	}
	many := `{"phones": [` + strings.TrimSuffix(strings.Repeat(`"5511999998888",`, maxCheckPhones+1), ",") + `]}`
	cases := []struct {
		name, method, body string
		code               int
	}{
		{"get", http.MethodGet, "", http.StatusMethodNotAllowed},
		{"bad json", http.MethodPost, "{", http.StatusBadRequest},
		{"no phones", http.MethodPost, `{"phones": []}`, http.StatusBadRequest},
		{"too many", http.MethodPost, many, http.StatusBadRequest},
		{"plus sign", http.MethodPost, `{"phones": ["+5511999998888"]}`, http.StatusBadRequest},
		{"too short", http.MethodPost, `{"phones": ["1234"]}`, http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec, resp := doCheck(t, checkNumbersHandler(connected, never), c.method, c.body)
			if rec.Code != c.code || resp.Success {
				t.Fatalf("expected %d without success, got %d %+v", c.code, rec.Code, resp)
			}
		})
	}
}

func TestCheckNumbersWhenDisconnectedOrLookupFails(t *testing.T) {
	never := func(context.Context, []string) ([]types.IsOnWhatsAppResponse, error) {
		t.Fatal("must not ask WhatsApp while disconnected")
		return nil, nil
	}
	rec, resp := doCheck(t, checkNumbersHandler(func() bool { return false }, never), http.MethodPost, `{"phones": ["5511999998888"]}`)
	if rec.Code != http.StatusServiceUnavailable || resp.Success {
		t.Fatalf("expected 503 while disconnected, got %d %+v", rec.Code, resp)
	}

	failing := func(context.Context, []string) ([]types.IsOnWhatsAppResponse, error) {
		return nil, errors.New("usync timed out")
	}
	rec, resp = doCheck(t, checkNumbersHandler(connected, failing), http.MethodPost, `{"phones": ["5511999998888"]}`)
	if rec.Code != http.StatusBadGateway || resp.Success || !strings.Contains(resp.Message, "usync timed out") {
		t.Fatalf("expected 502 with the error, got %d %+v", rec.Code, resp)
	}
}

func TestCheckRouteRequiresToken(t *testing.T) {
	const token = "supersecrettoken1234567890abcdef"
	mux := newRESTMux(newTestClient(&mockLIDStore{}), newTestMessageStore(t), 8080, token, nil)
	for _, withToken := range []bool{false, true} {
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/api/check", strings.NewReader(`{"phones": ["5511999998888"]}`))
		req.RemoteAddr = "127.0.0.1:54321"
		if withToken {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		// The test client is never connected: with the token the route answers 503, without it 401.
		want := http.StatusUnauthorized
		if withToken {
			want = http.StatusServiceUnavailable
		}
		if rec.Code != want {
			t.Fatalf("withToken=%v: expected %d, got %d (%s)", withToken, want, rec.Code, rec.Body.String())
		}
	}
}

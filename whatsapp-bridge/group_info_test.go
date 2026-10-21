package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

func doGroupInfo(t *testing.T, h http.HandlerFunc, method, body string) (*httptest.ResponseRecorder, GroupInfoResponse) {
	t.Helper()
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(method, "/api/group-info", strings.NewReader(body)))
	var resp GroupInfoResponse
	if rec.Body.Len() > 0 && strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("invalid JSON %q: %v", rec.Body.String(), err)
		}
	}
	return rec, resp
}

var (
	testGroup   = types.NewJID("120363000000000001", types.GroupServer)
	ownerLID    = types.NewJID("111111111111111", types.HiddenUserServer)
	ownerPN     = types.NewJID("5531977776666", types.DefaultUserServer)
	adminPN     = types.NewJID("5511988887777", types.DefaultUserServer)
	hiddenLID   = types.NewJID("222222222222222", types.HiddenUserServer)
	mappedLID   = types.NewJID("333333333333333", types.HiddenUserServer)
	mappedPN    = types.NewJID("351912345678", types.DefaultUserServer)
	memberPN    = types.NewJID("5521966665555", types.DefaultUserServer)
	testCreated = time.Date(2024, 3, 5, 12, 30, 0, 0, time.UTC)
)

func groupDeps(info *types.GroupInfo, err error) groupInfoDeps {
	return groupInfoDeps{
		ready: connected,
		getInfo: func(_ context.Context, jid types.JID) (*types.GroupInfo, error) {
			if err != nil {
				return nil, err
			}
			if jid != testGroup {
				return nil, fmt.Errorf("unexpected group %s", jid)
			}
			return info, nil
		},
		phoneForLID: func(_ context.Context, lid types.JID) types.JID {
			if lid == mappedLID {
				return mappedPN
			}
			return types.EmptyJID
		},
		nameFor: func(_ context.Context, jid types.JID) string {
			return map[types.JID]string{ownerPN: "Ana Criadora", adminPN: "Bruno Admin"}[jid]
		},
	}
}

// A LID-addressed group: the creator is a super admin known by LID + phone, one admin is a
// plain phone JID, one admin hides the number (no mapping) and one only maps through the store.
func lidGroup() *types.GroupInfo {
	info := &types.GroupInfo{
		JID:              testGroup,
		OwnerJID:         ownerLID,
		OwnerPN:          ownerPN,
		GroupCreated:     testCreated,
		ParticipantCount: 57,
		Participants: []types.GroupParticipant{
			{JID: memberPN, PhoneNumber: memberPN},
			{JID: adminPN, PhoneNumber: adminPN, IsAdmin: true},
			{JID: hiddenLID, LID: hiddenLID, IsAdmin: true},
			{JID: ownerLID, LID: ownerLID, PhoneNumber: ownerPN, IsAdmin: true, IsSuperAdmin: true},
			{JID: mappedLID, LID: mappedLID, IsAdmin: true},
		},
	}
	info.Name = "Clientes VIP"
	return info
}

func TestGroupInfoReturnsCreatorAndAdmins(t *testing.T) {
	rec, resp := doGroupInfo(t, groupInfoHandler(groupDeps(lidGroup(), nil)), http.MethodPost,
		`{"chat_jid": "120363000000000001@g.us"}`)

	if rec.Code != http.StatusOK || !resp.Success || resp.Group == nil {
		t.Fatalf("expected 200 success with group, got %d %+v", rec.Code, resp)
	}
	g := resp.Group
	if g.JID != "120363000000000001@g.us" || g.Name != "Clientes VIP" || g.ParticipantCount != 57 {
		t.Fatalf("unexpected group header %+v", g)
	}
	if g.CreatedAt != "2024-03-05T12:30:00Z" {
		t.Fatalf("expected RFC3339 creation time, got %q", g.CreatedAt)
	}
	wantOwner := &GroupMember{
		JID: "111111111111111@lid", Phone: "5531977776666", LID: "111111111111111@lid",
		Name: "Ana Criadora", IsAdmin: true, IsSuperAdmin: true, InGroup: true,
	}
	if !reflect.DeepEqual(g.Owner, wantOwner) {
		t.Fatalf("owner:\n got %+v\nwant %+v", g.Owner, wantOwner)
	}
	// Super admins first, then the order WhatsApp gave; plain members never show up.
	wantAdmins := []GroupMember{
		*wantOwner,
		{JID: "5511988887777@s.whatsapp.net", Phone: "5511988887777", Name: "Bruno Admin", IsAdmin: true, InGroup: true},
		{JID: "222222222222222@lid", LID: "222222222222222@lid", IsAdmin: true, InGroup: true},
		{JID: "333333333333333@lid", Phone: "351912345678", LID: "333333333333333@lid", IsAdmin: true, InGroup: true},
	}
	if !reflect.DeepEqual(g.Admins, wantAdmins) {
		t.Fatalf("admins:\n got %+v\nwant %+v", g.Admins, wantAdmins)
	}
}

func TestGroupInfoCreatorWhoLeftIsStillReported(t *testing.T) {
	info := &types.GroupInfo{
		JID:      testGroup,
		OwnerJID: ownerPN, // phone-addressed group, creator no longer a participant
		Participants: []types.GroupParticipant{
			{JID: adminPN, PhoneNumber: adminPN, IsAdmin: true, IsSuperAdmin: true},
			{JID: memberPN},
		},
	}
	_, resp := doGroupInfo(t, groupInfoHandler(groupDeps(info, nil)), http.MethodPost, `{"chat_jid": "120363000000000001@g.us"}`)
	if !resp.Success || resp.Group == nil {
		t.Fatalf("expected success, got %+v", resp)
	}
	want := &GroupMember{JID: "5531977776666@s.whatsapp.net", Phone: "5531977776666", Name: "Ana Criadora"}
	if !reflect.DeepEqual(resp.Group.Owner, want) {
		t.Fatalf("owner:\n got %+v\nwant %+v", resp.Group.Owner, want)
	}
	if resp.Group.CreatedAt != "" {
		t.Fatalf("zero creation time must be omitted, got %q", resp.Group.CreatedAt)
	}
	if resp.Group.ParticipantCount != 2 {
		t.Fatalf("participant count falls back to the list size, got %d", resp.Group.ParticipantCount)
	}
	if len(resp.Group.Admins) != 1 || resp.Group.Admins[0].Phone != "5511988887777" {
		t.Fatalf("unexpected admins %+v", resp.Group.Admins)
	}
}

func TestGroupInfoWithoutOwnerOmitsIt(t *testing.T) {
	info := &types.GroupInfo{JID: testGroup, Participants: []types.GroupParticipant{{JID: adminPN, IsAdmin: true}}}
	rec, resp := doGroupInfo(t, groupInfoHandler(groupDeps(info, nil)), http.MethodPost, `{"chat_jid": "120363000000000001@g.us"}`)
	if rec.Code != http.StatusOK || resp.Group == nil || resp.Group.Owner != nil {
		t.Fatalf("expected no owner, got %d %+v", rec.Code, resp.Group)
	}
	if !strings.Contains(rec.Body.String(), `"admins":[`) {
		t.Fatalf("admins must always be a list, got %s", rec.Body.String())
	}
}

func TestGroupInfoNotInGroupIs403(t *testing.T) {
	for _, err := range []error{whatsmeow.ErrNotInGroup, whatsmeow.ErrGroupNotFound, fmt.Errorf("wrapped: %w", whatsmeow.ErrNotInGroup)} {
		rec, resp := doGroupInfo(t, groupInfoHandler(groupDeps(nil, err)), http.MethodPost, `{"chat_jid": "120363000000000001@g.us"}`)
		if rec.Code != http.StatusForbidden || resp.Success || resp.Message == "" {
			t.Fatalf("%v: expected 403 with a message, got %d %+v", err, rec.Code, resp)
		}
	}
}

func TestGroupInfoLookupFailureIs502(t *testing.T) {
	rec, resp := doGroupInfo(t, groupInfoHandler(groupDeps(nil, errors.New("info query timed out"))), http.MethodPost,
		`{"chat_jid": "120363000000000001@g.us"}`)
	if rec.Code != http.StatusBadGateway || resp.Success {
		t.Fatalf("expected 502, got %d %+v", rec.Code, resp)
	}
}

func TestGroupInfoNotReadyIs503WithoutLookup(t *testing.T) {
	deps := groupDeps(lidGroup(), nil)
	deps.ready = func() bool { return false }
	deps.getInfo = func(context.Context, types.JID) (*types.GroupInfo, error) {
		t.Fatal("must not ask WhatsApp while not connected")
		return nil, nil
	}
	rec, _ := doGroupInfo(t, groupInfoHandler(deps), http.MethodPost, `{"chat_jid": "120363000000000001@g.us"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

func TestGroupInfoRejectsBadRequests(t *testing.T) {
	h := groupInfoHandler(groupDeps(lidGroup(), nil))
	if rec, _ := doGroupInfo(t, h, http.MethodGet, ""); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: expected 405, got %d", rec.Code)
	}
	for _, body := range []string{
		`not json`,
		`{}`,
		`{"chat_jid": "5531977776666@s.whatsapp.net"}`, // not a group
		`{"chat_jid": "120363000000000001"}`,           // no server
	} {
		rec, resp := doGroupInfo(t, h, http.MethodPost, body)
		if rec.Code != http.StatusBadRequest || resp.Success || resp.Message == "" {
			t.Fatalf("%s: expected 400 with a message, got %d %+v", body, rec.Code, resp)
		}
	}
}

func TestGroupInfoRouteRequiresToken(t *testing.T) {
	const token = "supersecrettoken1234567890abcdef"
	mux := newRESTMux(newTestClient(&mockLIDStore{}), newTestMessageStore(t), 8080, token, nil)
	for _, withToken := range []bool{false, true} {
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/api/group-info", strings.NewReader(`{"chat_jid": "120363000000000001@g.us"}`))
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

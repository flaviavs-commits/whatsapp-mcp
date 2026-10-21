package main

// Group creator and admins exposed over the authenticated REST API.
//
// POST /api/group-info {"chat_jid": "1203...@g.us"} asks WhatsApp for the group's
// metadata and returns who created it and who its admins are, so a caller can reach
// the people responsible for a group. Members that are not admins are never
// returned: callers only need the people to contact, not the whole member list.
//
// In LID-addressed groups WhatsApp identifies participants by LID; the phone number
// comes from the participant's PhoneNumber, the group's OwnerPN or the local LID map,
// and stays empty when WhatsApp hides it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

const groupInfoTimeout = 20 * time.Second

type GroupInfoRequest struct {
	ChatJID string `json:"chat_jid"`
}

type GroupMember struct {
	JID          string `json:"jid"`             // JID to message this person (LID or phone JID)
	Phone        string `json:"phone,omitempty"` // country code + number, digits only; empty when hidden
	LID          string `json:"lid,omitempty"`
	Name         string `json:"name,omitempty"` // from the local contact store
	IsAdmin      bool   `json:"is_admin"`
	IsSuperAdmin bool   `json:"is_super_admin"`
	InGroup      bool   `json:"in_group"`
}

type GroupInfoResult struct {
	JID              string        `json:"jid"`
	Name             string        `json:"name"`
	CreatedAt        string        `json:"created_at,omitempty"` // RFC3339
	ParticipantCount int           `json:"participant_count"`
	Owner            *GroupMember  `json:"owner,omitempty"` // creator; omitted when WhatsApp does not say
	Admins           []GroupMember `json:"admins"`
}

type GroupInfoResponse struct {
	Success bool             `json:"success"`
	Message string           `json:"message,omitempty"`
	Group   *GroupInfoResult `json:"group,omitempty"`
}

// groupInfoDeps are the client calls the handler needs; tests swap them for fakes.
type groupInfoDeps struct {
	ready       func() bool
	getInfo     func(ctx context.Context, jid types.JID) (*types.GroupInfo, error)
	phoneForLID func(ctx context.Context, lid types.JID) types.JID // EmptyJID when unknown
	nameFor     func(ctx context.Context, jid types.JID) string    // "" when unknown
}

func clientGroupInfoDeps(client *whatsmeow.Client, ready func() bool) groupInfoDeps {
	return groupInfoDeps{
		ready:   ready,
		getInfo: client.GetGroupInfo,
		phoneForLID: func(ctx context.Context, lid types.JID) types.JID {
			pn, err := client.Store.LIDs.GetPNForLID(ctx, lid)
			if err != nil {
				return types.EmptyJID
			}
			return pn
		},
		nameFor: func(ctx context.Context, jid types.JID) string {
			contact, err := client.Store.Contacts.GetContact(ctx, jid)
			if err != nil || !contact.Found {
				return ""
			}
			for _, name := range []string{contact.FullName, contact.PushName, contact.BusinessName} {
				if name != "" {
					return name
				}
			}
			return ""
		},
	}
}

func writeGroupInfoResponse(w http.ResponseWriter, status int, resp GroupInfoResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
}

// phoneJID picks the phone JID among the ones WhatsApp gave, falling back to the LID map.
func (d groupInfoDeps) phoneJID(ctx context.Context, candidates ...types.JID) types.JID {
	for _, j := range candidates {
		if !j.IsEmpty() && j.Server == types.DefaultUserServer {
			return j.ToNonAD()
		}
	}
	for _, j := range candidates {
		if !j.IsEmpty() && j.Server == types.HiddenUserServer {
			if pn := d.phoneForLID(ctx, j.ToNonAD()); !pn.IsEmpty() {
				return pn.ToNonAD()
			}
		}
	}
	return types.EmptyJID
}

func (d groupInfoDeps) member(ctx context.Context, p types.GroupParticipant) GroupMember {
	m := GroupMember{
		JID:          p.JID.ToNonAD().String(),
		IsAdmin:      p.IsAdmin || p.IsSuperAdmin,
		IsSuperAdmin: p.IsSuperAdmin,
		InGroup:      true,
	}
	lid := p.LID
	if lid.IsEmpty() && p.JID.Server == types.HiddenUserServer {
		lid = p.JID
	}
	if !lid.IsEmpty() {
		m.LID = lid.ToNonAD().String()
	}
	pn := d.phoneJID(ctx, p.JID, p.PhoneNumber, lid)
	if !pn.IsEmpty() {
		m.Phone = pn.User
		m.Name = d.nameFor(ctx, pn)
	} else {
		m.Name = d.nameFor(ctx, p.JID.ToNonAD())
	}
	return m
}

func samePerson(p types.GroupParticipant, j types.JID) bool {
	if j.IsEmpty() {
		return false
	}
	j = j.ToNonAD()
	for _, c := range []types.JID{p.JID, p.LID, p.PhoneNumber} {
		if !c.IsEmpty() && c.ToNonAD() == j {
			return true
		}
	}
	return false
}

func (d groupInfoDeps) result(ctx context.Context, info *types.GroupInfo) *GroupInfoResult {
	res := &GroupInfoResult{
		JID:              info.JID.String(),
		Name:             info.Name,
		ParticipantCount: info.ParticipantCount,
		Admins:           []GroupMember{},
	}
	if res.ParticipantCount == 0 {
		res.ParticipantCount = len(info.Participants)
	}
	if !info.GroupCreated.IsZero() {
		res.CreatedAt = info.GroupCreated.UTC().Format(time.RFC3339)
	}

	var supers, admins []GroupMember
	for _, p := range info.Participants {
		isOwner := samePerson(p, info.OwnerJID) || samePerson(p, info.OwnerPN)
		if !p.IsAdmin && !p.IsSuperAdmin && !isOwner {
			continue
		}
		m := d.member(ctx, p)
		if isOwner {
			if m.Phone == "" && !info.OwnerPN.IsEmpty() {
				m.Phone = info.OwnerPN.User
				m.Name = d.nameFor(ctx, info.OwnerPN.ToNonAD())
			}
			owner := m
			res.Owner = &owner
		}
		if !m.IsAdmin {
			continue
		}
		if m.IsSuperAdmin {
			supers = append(supers, m)
		} else {
			admins = append(admins, m)
		}
	}
	res.Admins = append(append(res.Admins, supers...), admins...)

	if res.Owner == nil && !info.OwnerJID.IsEmpty() {
		// The creator left the group: WhatsApp still says who it was.
		owner := GroupMember{JID: info.OwnerJID.ToNonAD().String()}
		if info.OwnerJID.Server == types.HiddenUserServer {
			owner.LID = owner.JID
		}
		if pn := d.phoneJID(ctx, info.OwnerPN, info.OwnerJID); !pn.IsEmpty() {
			owner.Phone = pn.User
			owner.Name = d.nameFor(ctx, pn)
		} else {
			owner.Name = d.nameFor(ctx, info.OwnerJID.ToNonAD())
		}
		res.Owner = &owner
	}
	return res
}

func groupInfoHandler(d groupInfoDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req GroupInfoRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeGroupInfoResponse(w, http.StatusBadRequest, GroupInfoResponse{Message: "Invalid request format"})
			return
		}
		jid, err := types.ParseJID(req.ChatJID)
		if req.ChatJID == "" || err != nil || jid.Server != types.GroupServer {
			writeGroupInfoResponse(w, http.StatusBadRequest, GroupInfoResponse{Message: "chat_jid must be a group JID (ending in @g.us)"})
			return
		}
		if !d.ready() {
			writeGroupInfoResponse(w, http.StatusServiceUnavailable, GroupInfoResponse{Message: "Not connected to WhatsApp (or not paired yet)"})
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), groupInfoTimeout)
		defer cancel()
		info, err := d.getInfo(ctx, jid)
		if errors.Is(err, whatsmeow.ErrNotInGroup) || errors.Is(err, whatsmeow.ErrGroupNotFound) {
			writeGroupInfoResponse(w, http.StatusForbidden, GroupInfoResponse{Message: "This number is not in that group (or the group no longer exists)"})
			return
		}
		if err != nil || info == nil {
			writeGroupInfoResponse(w, http.StatusBadGateway, GroupInfoResponse{Message: fmt.Sprintf("Group lookup failed: %v", err)})
			return
		}

		res := d.result(ctx, info)
		// Phone numbers and group ids are personal data: log only how many admins came back.
		fmt.Printf("→ /api/group-info admins=%d\n", len(res.Admins))
		writeGroupInfoResponse(w, http.StatusOK, GroupInfoResponse{Success: true, Group: res})
	}
}

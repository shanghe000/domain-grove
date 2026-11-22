package spaceapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"ontology-engine/internal/auth"
)

func TestSpaceMemberCandidatesTrackCurrentDirectoryAccess(t *testing.T) {
	_, catalog := openFixture(t)
	ctx := context.Background()
	if _, err := catalog.Create(ctx, "owner", "alpha", "策划组"); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.AddMember(ctx, "alpha", "owner", "reviewer"); err != nil {
		t.Fatal(err)
	}
	roster := []auth.DirectoryPerson{
		{SubjectID: "owner", DisplayName: "负责人"},
		{SubjectID: "reviewer", DisplayName: "现有成员"},
		{SubjectID: "other", DisplayName: "可选成员"},
	}
	calls := 0
	var directoryErr error
	mux := http.NewServeMux()
	New(catalog, false, nil, "", func(context.Context) ([]auth.DirectoryPerson, error) {
		calls++
		return roster, directoryErr
	}).Register(mux)
	app := identityFixture(t).Middleware(mux)
	const candidates = spacesPath + "/alpha/member-candidates"
	const members = spacesPath + "/alpha/members"
	response(t, app, http.MethodGet, candidates, "", "", http.StatusUnauthorized)
	response(t, app, http.MethodGet, candidates, "fixture-reviewer", "", http.StatusForbidden)
	response(t, app, http.MethodGet, candidates, "fixture-other", "", http.StatusNotFound)
	response(t, app, http.MethodPost, members, "fixture-reviewer", `{"subject":"other"}`, http.StatusForbidden)
	if calls != 0 {
		t.Fatal("unauthorized access initiated an identity directory lookup")
	}

	result := response(t, app, http.MethodGet, candidates, "fixture-owner", "", http.StatusOK)
	var body struct {
		Candidates  []auth.DirectoryPerson `json:"candidates"`
		MemberNames map[string]string      `json:"member_names"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if result.Header().Get("Cache-Control") != "no-store" || len(body.Candidates) != 1 ||
		body.Candidates[0] != roster[2] || body.MemberNames["owner"] != "负责人" || body.MemberNames["reviewer"] != "现有成员" {
		t.Fatalf("unexpected eligible directory: %+v", body)
	}

	const selection = `{"subject":"other"}`
	eligible := roster
	roster = roster[:2]
	response(t, app, http.MethodPost, members, "fixture-owner", selection, http.StatusUnprocessableEntity)
	roster = eligible
	added := response(t, app, http.MethodPost, members, "fixture-owner", selection, http.StatusOK)
	if !strings.Contains(added.Body.String(), `"display_name":"可选成员"`) {
		t.Fatalf("unexpected add result: %s", added.Body)
	}
	empty := response(t, app, http.MethodGet, candidates, "fixture-owner", "", http.StatusOK)
	if !strings.Contains(empty.Body.String(), `"candidates":[]`) {
		t.Fatalf("new member still selectable: %s", empty.Body)
	}
	directoryErr = errors.New("upstream unavailable")
	response(t, app, http.MethodGet, candidates, "fixture-owner", "", http.StatusBadGateway)
	response(t, app, http.MethodGet, members, "fixture-owner", "", http.StatusOK)
}

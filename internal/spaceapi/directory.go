package spaceapi

import (
	"cmp"
	"context"
	"net/http"
	"slices"
	"strings"

	"ontology-engine/internal/auth"
)

type ListPeople func(context.Context) ([]auth.DirectoryPerson, error)

func (a *API) directory(w http.ResponseWriter, r *http.Request) (map[string]string, bool) {
	if a.listPeople == nil {
		writeProblem(w, http.StatusServiceUnavailable, "当前环境未配置人员目录。")
		return nil, false
	}
	roster, err := a.listPeople(r.Context())
	if err != nil {
		writeProblem(w, http.StatusBadGateway, "暂时无法读取人员目录，请重试。")
		return nil, false
	}
	bySubject := make(map[string]string, len(roster))
	for _, person := range roster {
		if person.SubjectID != "" && strings.TrimSpace(person.SubjectID) == person.SubjectID {
			bySubject[person.SubjectID] = strings.TrimSpace(person.DisplayName)
		}
	}
	return bySubject, true
}

func (a *API) memberCandidates(w http.ResponseWriter, r *http.Request, user *auth.UserIdentity) {
	members, err := a.catalog.Members(r.Context(), r.PathValue("space"), user.UserID)
	if err != nil {
		writeCatalogError(w, err)
		return
	}
	people, ok := a.directory(w, r)
	if !ok {
		return
	}
	names := make(map[string]string, len(members))
	for _, member := range members {
		if name := people[member.Subject]; name != "" {
			names[member.Subject] = name
		}
		delete(people, member.Subject)
	}
	candidates := make([]auth.DirectoryPerson, 0, len(people))
	for subject, name := range people {
		candidates = append(candidates, auth.DirectoryPerson{SubjectID: subject, DisplayName: name})
	}
	slices.SortFunc(candidates, func(left, right auth.DirectoryPerson) int {
		return cmp.Or(strings.Compare(left.DisplayName, right.DisplayName), strings.Compare(left.SubjectID, right.SubjectID))
	})
	writeJSON(w, http.StatusOK, struct {
		Candidates  []auth.DirectoryPerson `json:"candidates"`
		MemberNames map[string]string      `json:"member_names,omitempty"`
	}{candidates, names})
}

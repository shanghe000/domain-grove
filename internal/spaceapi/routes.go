package spaceapi

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"

	"ontology-engine/internal/auth"
	"ontology-engine/internal/index"
	"ontology-engine/internal/indexapi"
	"ontology-engine/internal/spaces"
)

const spacesPath = "/api/v1/spaces"
const requestLimit = 16 << 10

type API struct {
	catalog        *spaces.Catalog
	allowDevReview bool
	planner        indexapi.ModelingPlanner
	submitterID    string
	listPeople     ListPeople

	mutex         sync.Mutex
	indexHandlers map[*index.Store]http.Handler
}

func New(catalog *spaces.Catalog, allowDevReview bool, planner indexapi.ModelingPlanner, submitterID string, listPeople ListPeople) *API {
	submitterID = strings.TrimSpace(submitterID)
	if submitterID == "" {
		submitterID = "bi-agent"
	}
	return &API{catalog: catalog, allowDevReview: allowDevReview, planner: planner,
		submitterID: submitterID, listPeople: listPeople, indexHandlers: make(map[*index.Store]http.Handler)}
}

// Register must be mounted below the authentication middleware.
func (a *API) Register(operate *http.ServeMux) {
	operate.Handle("GET "+spacesPath, a.identity(false, a.list))
	operate.Handle("POST "+spacesPath, a.identity(true, a.create))

	const project = spacesPath + "/{space}"
	operate.Handle("GET "+project+"/members", a.identity(true, a.members))
	operate.Handle("GET "+project+"/member-candidates", a.identity(true, a.memberCandidates))
	operate.Handle("POST "+project+"/members", a.identity(true, a.addMember))
	operate.Handle("POST "+project+"/members/remove", a.identity(true, a.removeMember))
	operate.Handle("POST "+project+"/credentials", a.identity(true, a.rotate))
	index := a.identity(false, a.index)
	operate.Handle(project+"/index", index)
	operate.Handle(project+"/index/", index)
	operate.Handle(project+"/index.yaml", index)
}

type userHandler func(http.ResponseWriter, *http.Request, *auth.UserIdentity)

func (a *API) identity(manage bool, next userHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		user, found := auth.IdentityFromContext(r.Context())
		if !found || user == nil || strings.TrimSpace(user.UserID) == "" {
			writeProblem(w, http.StatusUnauthorized, "需要登录。")
			return
		}
		if !a.allowDevReview && user.UserID == "dev_user" && (manage ||
			(r.PathValue("space") != "" && r.PathValue("space") != spaces.DefaultID)) {
			writeProblem(w, http.StatusForbidden, "项目空间需要登录。")
			return
		}
		next(w, r, user)
	})
}

func (a *API) list(w http.ResponseWriter, r *http.Request, user *auth.UserIdentity) {
	result, err := a.catalog.List(r.Context(), user.UserID)
	if err != nil {
		writeCatalogError(w, err)
		return
	}
	if user.UserID == "dev_user" && !a.allowDevReview {
		visible := []spaces.Space{}
		for _, space := range result {
			if space.ID == spaces.DefaultID {
				visible = append(visible, space)
				break
			}
		}
		result = visible
	}
	writeJSON(w, http.StatusOK, struct {
		Spaces []spaces.Space `json:"spaces"`
	}{result})
}

func (a *API) create(w http.ResponseWriter, r *http.Request, user *auth.UserIdentity) {
	var input struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	space, err := a.catalog.Create(r.Context(), user.UserID, input.ID, input.Name)
	if err != nil {
		writeCatalogError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, space)
}

func (a *API) members(w http.ResponseWriter, r *http.Request, user *auth.UserIdentity) {
	result, err := a.catalog.Members(r.Context(), r.PathValue("space"), user.UserID)
	if err != nil {
		writeCatalogError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Members []spaces.Member `json:"members"`
	}{result})
}

type memberInput struct {
	Subject string `json:"subject"`
}

func (a *API) addMember(w http.ResponseWriter, r *http.Request, user *auth.UserIdentity) {
	var input memberInput
	if !readJSON(w, r, &input) {
		return
	}
	var name string
	if a.listPeople != nil {
		members, err := a.catalog.Members(r.Context(), r.PathValue("space"), user.UserID)
		if err != nil {
			writeCatalogError(w, err)
			return
		}
		for _, member := range members {
			if member.Subject == input.Subject && member.Role == spaces.RoleReviewer {
				writeJSON(w, http.StatusOK, member)
				return
			}
		}
		if input.Subject == user.UserID {
			writeCatalogError(w, spaces.ErrConflict)
			return
		}
		people, ok := a.directory(w, r)
		if !ok {
			return
		}
		name, ok = people[input.Subject]
		if !ok {
			writeProblem(w, http.StatusUnprocessableEntity, "该人员当前没有知识引擎使用权限，请刷新人员名单。")
			return
		}
	}
	result, err := a.catalog.AddMember(r.Context(), r.PathValue("space"), user.UserID, input.Subject)
	if err != nil {
		writeCatalogError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		spaces.Member
		DisplayName string `json:"display_name,omitempty"`
	}{result, name})
}

func (a *API) removeMember(w http.ResponseWriter, r *http.Request, user *auth.UserIdentity) {
	var input memberInput
	if !readJSON(w, r, &input) {
		return
	}
	if err := a.catalog.RemoveMember(r.Context(), r.PathValue("space"), user.UserID, input.Subject); err != nil {
		writeCatalogError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) rotate(w http.ResponseWriter, r *http.Request, user *auth.UserIdentity) {
	var input struct {
		Scope string `json:"scope"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	token, err := a.catalog.RotateToken(r.Context(), r.PathValue("space"), user.UserID, input.Scope)
	if err != nil {
		writeCatalogError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Scope string `json:"scope"`
		Token string `json:"token"`
	}{input.Scope, token})
}

func (a *API) index(w http.ResponseWriter, r *http.Request, user *auth.UserIdentity) {
	id := r.PathValue("space")
	space, err := a.catalog.Authorize(r.Context(), user.UserID, id)
	if err != nil {
		writeCatalogError(w, err)
		return
	}
	store, err := a.catalog.Store(r.Context(), space.ID)
	if err != nil {
		writeCatalogError(w, err)
		return
	}
	tail, exists := strings.CutPrefix(r.URL.Path, spacesPath+"/"+id)
	if !exists {
		http.NotFound(w, r)
		return
	}
	scoped := r.Clone(indexapi.WithSourceSpace(r.Context(), space.ID))

	scoped.URL.Path = "/api/v1" + tail
	scoped.URL.RawPath = ""
	a.indexHandler(store).ServeHTTP(w, scoped)
}

func (a *API) indexHandler(store *index.Store) http.Handler {
	a.mutex.Lock()
	defer a.mutex.Unlock()
	if handler, found := a.indexHandlers[store]; found {
		return handler
	}
	handler := http.NewServeMux()
	indexapi.RegisterReview(handler, store, a.allowDevReview)
	indexapi.RegisterModeling(handler, store, a.allowDevReview, a.planner)
	a.indexHandlers[store] = handler
	return handler
}

func readJSON(w http.ResponseWriter, r *http.Request, input any) bool {
	contentType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if contentType != "application/json" {
		writeProblem(w, http.StatusUnsupportedMediaType, "请使用 JSON 提交项目设置。")
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, requestLimit))
	decoder.DisallowUnknownFields()
	err := decoder.Decode(input)
	if err == nil {
		var extra json.RawMessage
		err = decoder.Decode(&extra)
		if err == io.EOF {
			return true
		}
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeProblem(w, http.StatusRequestEntityTooLarge, "项目设置超过允许的大小。")
	} else {
		writeProblem(w, http.StatusBadRequest, "项目设置的 JSON 格式无效。")
	}
	return false
}

func writeCatalogError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, spaces.ErrNotFound):
		writeProblem(w, http.StatusNotFound, "找不到项目空间或成员。")
	case errors.Is(err, spaces.ErrForbidden):
		writeProblem(w, http.StatusForbidden, "当前账号无权管理此项目空间。")
	case errors.Is(err, spaces.ErrConflict):
		writeProblem(w, http.StatusConflict, "项目编号或成员信息与现有设置冲突。")
	case errors.Is(err, spaces.ErrInvalid):
		writeProblem(w, http.StatusBadRequest, "请核对项目编号、名称、成员主体或凭据权限。")
	default:
		writeProblem(w, http.StatusInternalServerError, "项目空间暂时无法完成请求。")
	}
}

func writeProblem(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, result any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(result)
}

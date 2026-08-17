package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode"

	"github.com/go-chi/chi/v5"
	"sigs.k8s.io/yaml"

	"github.com/hkjang/AgentHub/internal/runtimetype"
	"github.com/hkjang/AgentHub/internal/store"
)

// GitOps export and import.
//
// An agent definition is configuration, and configuration that only exists in a
// database cannot be reviewed, diffed or promoted from a staging cluster to a
// production one. Exporting it as YAML makes it a file someone can put in a
// repository; importing it makes that file the source of truth again.
//
// References are exported by name rather than by identifier. An id is meaningless
// in the cluster the file is imported into — the profiles and endpoints there
// have their own — so a portable document has to name what it wants and let the
// import resolve it locally, or say plainly what is missing.

// agentDocument is the exported shape. It is deliberately not the store's row:
// owner, version and timestamps belong to a specific installation.
type agentDocument struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
	} `json:"metadata"`
	Spec struct {
		RuntimeType     string   `json:"runtimeType"`
		RuntimeProfile  string   `json:"runtimeProfile,omitempty"`
		Workspace       string   `json:"workspace,omitempty"`
		ModelEndpoint   string   `json:"modelEndpoint,omitempty"`
		MCPBundle       string   `json:"mcpBundle,omitempty"`
		SecurityProfile string   `json:"securityProfile,omitempty"`
		NetworkProfile  string   `json:"networkProfile,omitempty"`
		SystemPrompt    string   `json:"systemPrompt,omitempty"`
		CustomCommand   []string `json:"customCommand,omitempty"`
		CustomPort      int      `json:"customPort,omitempty"`
	} `json:"spec"`
}

const agentDocumentAPIVersion = "agenthub.io/v1alpha1"
const agentDocumentKind = "AgentDefinition"

// maxImportBytes bounds an uploaded document.
const maxImportBytes = 1 << 20

// exportAgent renders one agent as a portable YAML document.
func (s *Server) exportAgent(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	agent, err := s.store.AgentByID(r.Context(), chi.URLParam(r, "id"), u.ID, u.Role == "admin")
	if err != nil {
		writeStoreError(w, err)
		return
	}
	document, err := s.agentToDocument(r, agent)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	encoded, err := yaml.Marshal(document)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "export_failed", "정의를 YAML로 변환하지 못했습니다.")
		return
	}
	s.store.Audit(r.Context(), &u, "agent.export", "agent", agent.ID, "success", clientIP(r), nil)
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	// RFC 6266: the ASCII parameter is the fallback, filename* carries the name
	// as it was actually written.
	name := safeFileName(agent.Name)
	w.Header().Set("Content-Disposition",
		"attachment; filename=\""+asciiFileName(agent.Name)+".yaml\"; filename*=UTF-8''"+url.PathEscape(name+".yaml"))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encoded)
}

func (s *Server) agentToDocument(r *http.Request, agent store.Agent) (agentDocument, error) {
	var document agentDocument
	document.APIVersion, document.Kind = agentDocumentAPIVersion, agentDocumentKind
	document.Metadata.Name = agent.Name
	document.Metadata.Description = agent.Description
	document.Spec.RuntimeType = agent.RuntimeType
	// The spec column is JSON, so it is decoded as JSON rather than leaning on
	// YAML being a superset of it.
	if len(agent.Spec) > 0 {
		var spec struct {
			SystemPrompt string `json:"systemPrompt"`
		}
		_ = json.Unmarshal(agent.Spec, &spec)
		document.Spec.SystemPrompt = spec.SystemPrompt
	}
	document.Spec.CustomCommand, document.Spec.CustomPort = agent.CustomRuntime()

	// Every reference becomes a name; an id from this installation would mean
	// nothing in the cluster the file is imported into.
	names, err := s.referenceNames(r)
	if err != nil {
		return agentDocument{}, err
	}
	document.Spec.RuntimeProfile = names.profiles[deref(agent.RuntimeProfileID)]
	document.Spec.Workspace = names.workspaces[deref(agent.WorkspaceID)]
	document.Spec.ModelEndpoint = names.models[deref(agent.ModelEndpointID)]
	document.Spec.MCPBundle = names.bundles[deref(agent.MCPBundleID)]
	// Policy profiles are seeded with stable ids, so they travel as they are.
	document.Spec.SecurityProfile = deref(agent.SecurityProfileID)
	document.Spec.NetworkProfile = deref(agent.NetworkProfileID)
	return document, nil
}

// referenceLookup maps identifiers to names in both directions.
type referenceLookup struct {
	profiles, workspaces, models, bundles         map[string]string
	profileIDs, workspaceIDs, modelIDs, bundleIDs map[string]string
}

func (s *Server) referenceNames(r *http.Request) (referenceLookup, error) {
	lookup := referenceLookup{
		profiles: map[string]string{}, workspaces: map[string]string{}, models: map[string]string{}, bundles: map[string]string{},
		profileIDs: map[string]string{}, workspaceIDs: map[string]string{}, modelIDs: map[string]string{}, bundleIDs: map[string]string{},
	}
	u, _ := userFromContext(r.Context())
	profiles, err := s.store.RuntimeProfiles(r.Context())
	if err != nil {
		return lookup, err
	}
	for _, item := range profiles {
		lookup.profiles[item.ID] = item.Name
		lookup.profileIDs[strings.ToLower(item.Name)] = item.ID
	}
	workspaces, err := s.store.Workspaces(r.Context(), u.ID, u.Role == "admin")
	if err != nil {
		return lookup, err
	}
	for _, item := range workspaces {
		lookup.workspaces[item.ID] = item.Name
		lookup.workspaceIDs[strings.ToLower(item.Name)] = item.ID
	}
	models, err := s.store.ModelEndpoints(r.Context())
	if err != nil {
		return lookup, err
	}
	for _, item := range models {
		lookup.models[item.ID] = item.Name
		lookup.modelIDs[strings.ToLower(item.Name)] = item.ID
	}
	bundles, err := s.store.MCPBundles(r.Context(), true)
	if err != nil {
		return lookup, err
	}
	for _, item := range bundles {
		lookup.bundles[item.ID] = item.Name
		lookup.bundleIDs[strings.ToLower(item.Name)] = item.ID
	}
	return lookup, nil
}

// importAgent creates or updates an agent from a YAML document.
//
// It is deliberately not a blind upsert: a reference the target cluster does not
// have is reported by name rather than silently dropped, because an agent that
// comes up without its workspace or its model looks imported and is not.
func (s *Server) importAgent(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	body, err := io.ReadAll(io.LimitReader(r.Body, maxImportBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "정의 파일을 읽지 못했습니다.")
		return
	}
	var document agentDocument
	if err := yaml.Unmarshal(body, &document); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_yaml", "YAML을 해석하지 못했습니다: "+err.Error())
		return
	}
	if document.Kind != "" && document.Kind != agentDocumentKind {
		writeError(w, http.StatusBadRequest, "invalid_kind", "kind는 "+agentDocumentKind+"여야 합니다.")
		return
	}
	document.Metadata.Name = strings.TrimSpace(document.Metadata.Name)
	if document.Metadata.Name == "" || len(document.Metadata.Name) > 80 {
		writeError(w, http.StatusBadRequest, "invalid_name", "metadata.name은 1~80자여야 합니다.")
		return
	}
	if !runtimetype.IsSupported(document.Spec.RuntimeType) {
		writeError(w, http.StatusBadRequest, "invalid_runtime_type", "spec.runtimeType을 확인해 주세요.")
		return
	}

	lookup, err := s.referenceNames(r)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	missing := []string{}
	resolve := func(name string, table map[string]string, label string) string {
		if strings.TrimSpace(name) == "" {
			return ""
		}
		if id, ok := table[strings.ToLower(name)]; ok {
			return id
		}
		missing = append(missing, label+" "+name)
		return ""
	}
	input := store.CreateAgentInput{
		Name:              document.Metadata.Name,
		Description:       document.Metadata.Description,
		RuntimeType:       document.Spec.RuntimeType,
		SystemPrompt:      document.Spec.SystemPrompt,
		CustomCommand:     document.Spec.CustomCommand,
		CustomPort:        document.Spec.CustomPort,
		RuntimeProfileID:  resolve(document.Spec.RuntimeProfile, lookup.profileIDs, "런타임 프로파일"),
		WorkspaceID:       resolve(document.Spec.Workspace, lookup.workspaceIDs, "작업공간"),
		ModelEndpointID:   resolve(document.Spec.ModelEndpoint, lookup.modelIDs, "모델 엔드포인트"),
		MCPBundleID:       resolve(document.Spec.MCPBundle, lookup.bundleIDs, "MCP 번들"),
		SecurityProfileID: document.Spec.SecurityProfile,
		NetworkProfileID:  document.Spec.NetworkProfile,
	}
	if len(missing) > 0 {
		writeError(w, http.StatusBadRequest, "unresolved_references",
			"이 클러스터에 없는 항목을 참조합니다: "+strings.Join(missing, ", "))
		return
	}

	// Importing the same document twice updates rather than duplicating: a
	// GitOps flow re-applies the file on every change.
	existing, lookupErr := s.store.AgentByName(r.Context(), u.ID, document.Metadata.Name)
	switch {
	case lookupErr == nil:
		if existing.RuntimeType != input.RuntimeType {
			writeError(w, http.StatusBadRequest, "runtime_type_changed",
				"같은 이름의 Agent가 다른 런타임 유형으로 존재합니다. 런타임 유형은 변경할 수 없습니다.")
			return
		}
		saved, err := s.store.UpdateAgent(r.Context(), existing.ID, u.ID, u.Role == "admin", input)
		if err != nil {
			writeError(w, http.StatusBadRequest, "agent_import_failed", err.Error())
			return
		}
		s.store.Audit(r.Context(), &u, "agent.import", "agent", saved.ID, "success", clientIP(r), map[string]any{"mode": "update"})
		writeJSON(w, http.StatusOK, map[string]any{"agent": saved, "mode": "updated"})
	case errors.Is(lookupErr, store.ErrNotFound):
		saved, err := s.store.CreateAgent(r.Context(), u.ID, input)
		if err != nil {
			writeError(w, http.StatusBadRequest, "agent_import_failed", err.Error())
			return
		}
		s.store.Audit(r.Context(), &u, "agent.import", "agent", saved.ID, "success", clientIP(r), map[string]any{"mode": "create"})
		writeJSON(w, http.StatusCreated, map[string]any{"agent": saved, "mode": "created"})
	default:
		writeStoreError(w, lookupErr)
	}
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// safeFileName keeps an agent name usable as a download filename.
//
// Letters and digits of any script are kept — this product is used in Korean,
// and turning every such name into "agent" would make the export useless — while
// anything that could steer a path or break the header is replaced.
func safeFileName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	trimmed := strings.Trim(b.String(), "-")
	if trimmed == "" {
		return "agent"
	}
	return trimmed
}

// asciiFileName is the fallback for the plain filename parameter, which may not
// carry non-ASCII bytes; the filename* parameter carries the real name.
func asciiFileName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	trimmed := strings.Trim(b.String(), "-")
	if trimmed == "" {
		return "agent"
	}
	return trimmed
}

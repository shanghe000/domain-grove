// Package index stores a reviewed graph of knowledge references.
package index

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

const (
	SchemaVersion   = 1 // Original snapshot contract.
	SemanticVersion = 2
	KindArtifact    = "artifact"
	KindDomain      = "domain"
	KindConcept     = "concept"
	KindMetric      = "metric"
	KindDocument    = "document"
	KindSkill       = "skill"
	KindTool        = "tool"
)

var (
	ErrNotFound = errors.New("index item not found")
	ErrConflict = errors.New("index conflict")
	ErrInvalid  = errors.New("invalid index input")
)

type Graph struct {
	SchemaVersion int      `json:"schema_version" yaml:"schema_version"`
	Revision      int64    `json:"revision" yaml:"revision"`
	Nodes         []Node   `json:"nodes" yaml:"nodes"`
	Edges         []Edge   `json:"edges" yaml:"edges"`
	Changes       []Change `json:"changes,omitempty" yaml:"changes,omitempty"`
}

type Node struct {
	Axioms []Axiom `json:"axioms,omitempty" yaml:"axioms,omitempty"`

	ID          string         `json:"id" yaml:"id"`
	Kind        string         `json:"kind" yaml:"kind"`
	Label       string         `json:"label" yaml:"label"`
	ParentID    string         `json:"parent_id,omitempty" yaml:"parent_id,omitempty"`
	Description string         `json:"description,omitempty" yaml:"description,omitempty"`
	Ref         *Reference     `json:"ref,omitempty" yaml:"ref,omitempty"`
	Properties  map[string]any `json:"properties,omitempty" yaml:"properties,omitempty"`
}

type Reference struct {
	ReadStrategy string `json:"read_strategy,omitempty" yaml:"read_strategy,omitempty"`
	URL          string `json:"url,omitempty" yaml:"url,omitempty"`

	Repository string `json:"repository" yaml:"repository"`
	Path       string `json:"path" yaml:"path"`
	Anchor     string `json:"anchor,omitempty" yaml:"anchor,omitempty"`
	LineStart  int    `json:"line_start,omitempty" yaml:"line_start,omitempty"`
	LineEnd    int    `json:"line_end,omitempty" yaml:"line_end,omitempty"`
}

type Edge struct {
	Anchor      string `json:"anchor,omitempty" yaml:"anchor,omitempty"`
	LineStart   int    `json:"line_start,omitempty" yaml:"line_start,omitempty"`
	LineEnd     int    `json:"line_end,omitempty" yaml:"line_end,omitempty"`
	SourceField string `json:"source_field,omitempty" yaml:"source_field,omitempty"`
	TargetField string `json:"target_field,omitempty" yaml:"target_field,omitempty"`
	Separator   string `json:"separator,omitempty" yaml:"separator,omitempty"`

	ID          string `json:"id" yaml:"id"`
	From        string `json:"from" yaml:"from"`
	To          string `json:"to" yaml:"to"`
	Relation    string `json:"relation" yaml:"relation"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

type Patch struct {
	SchemaVersion   int      `json:"schema_version,omitempty" yaml:"schema_version,omitempty"`
	UpsertChanges   []Change `json:"upsert_changes,omitempty" yaml:"upsert_changes,omitempty"`
	RemoveChangeIDs []string `json:"remove_change_ids,omitempty" yaml:"remove_change_ids,omitempty"`

	UpsertNodes   []Node   `json:"upsert_nodes,omitempty" yaml:"upsert_nodes"`
	RemoveNodeIDs []string `json:"remove_node_ids,omitempty" yaml:"remove_node_ids"`
	UpsertEdges   []Edge   `json:"upsert_edges,omitempty" yaml:"upsert_edges"`
	RemoveEdgeIDs []string `json:"remove_edge_ids,omitempty" yaml:"remove_edge_ids"`
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

func conflict(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrConflict, fmt.Sprintf(format, args...))
}

func validID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for n, char := range id {
		if char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || char >= '0' && char <= '9' ||
			char == '_' || n > 0 && (char == '-' || char == '.' || char == ':') {
			continue
		}
		return false
	}
	return true
}

func textValid(value string, max int, multiline bool) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > max {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) && !(multiline && (char == '\n' || char == '\r' || char == '\t')) {
			return false
		}
	}
	return true
}

func safeRelativePath(value string) bool {
	return value != "" && value != "." && value != ".." && strings.TrimSpace(value) == value &&
		textValid(value, 2048, false) && !strings.ContainsAny(value, "\\:%?#") &&
		!strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "../") && path.Clean(value) == value
}

func leafKind(kind string) bool {
	return kind == KindArtifact || kind == KindDocument || kind == KindSkill || kind == KindTool
}

// ValidateReference checks a locator without resolving it against a repository.
func ValidateReference(ref Reference) error {
	if !safeRelativePath(ref.Repository) {
		return invalid("repository must be a logical repository name or qualified relative slug")
	}
	if !safeRelativePath(ref.Path) {
		return invalid("reference path must be a canonical relative POSIX path")
	}
	if !textValid(ref.Anchor, 512, false) {
		return invalid("reference anchor is invalid")
	}
	if ref.LineStart < 0 || ref.LineEnd < 0 || (ref.LineEnd > 0 && (ref.LineStart == 0 || ref.LineEnd < ref.LineStart)) {
		return invalid("reference line range is invalid")
	}
	if ref.URL != "" {
		u, err := url.Parse(ref.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || !textValid(ref.URL, 2048, false) {
			return invalid("source URL must be a web locator without embedded credentials")
		}
	}
	if !textValid(ref.ReadStrategy, 256, false) {
		return invalid("invalid read strategy")
	}
	return nil
}

func validateNode(node Node) error {
	if !validID(node.ID) {
		return invalid("node ID is invalid")
	}
	switch node.Kind {
	case KindDomain, KindConcept, KindMetric, KindArtifact, KindDocument, KindSkill, KindTool:
	default:
		return invalid("node %s has an unknown kind", node.ID)
	}
	if strings.TrimSpace(node.Label) == "" || !textValid(node.Label, 256, false) ||
		!textValid(node.Description, 16_384, true) {
		return invalid("node %s has an invalid label or description", node.ID)
	}
	if node.ParentID != "" && !validID(node.ParentID) {
		return invalid("node %s has an invalid parent ID", node.ID)
	}
	if node.Ref == nil && leafKind(node.Kind) {
		return invalid("node %s requires a reference", node.ID)
	}
	if node.Ref != nil {
		if err := ValidateReference(*node.Ref); err != nil {
			return fmt.Errorf("node %s: %w", node.ID, err)
		}
	}
	if _, err := json.Marshal(node.Properties); err != nil {
		return invalid("node %s properties must be JSON values", node.ID)
	}
	return nil
}

func validateEdge(edge Edge) error {
	if !validID(edge.ID) || !validID(edge.From) || !validID(edge.To) || !validID(edge.Relation) ||
		!textValid(edge.Description, 16_384, true) {
		return invalid("edge %q has an invalid ID, endpoint, relation or description", edge.ID)
	}
	return nil
}

// Validate checks references and containment. Relation edges are directed and
// may form cycles independently of the containment hierarchy.
func Validate(graph Graph) error {
	if (graph.SchemaVersion != SchemaVersion && graph.SchemaVersion != SemanticVersion) || graph.Revision < 0 {
		return invalid("unsupported graph schema version or revision")
	}
	nodes := make(map[string]Node, len(graph.Nodes))
	for _, node := range graph.Nodes {
		if err := validateNode(node); err != nil {
			return err
		}
		if _, duplicate := nodes[node.ID]; duplicate {
			return invalid("duplicate node %s", node.ID)
		}
		nodes[node.ID] = node
	}
	const visiting, visited = 1, 2
	seen := make(map[string]int, len(nodes))
	for _, node := range graph.Nodes {
		id := node.ID
		for id != "" && seen[id] != visited {
			if seen[id] == visiting {
				return invalid("containment cycle at node %s", id)
			}
			seen[id] = visiting
			parentID := nodes[id].ParentID
			if parentID != "" {
				parent, found := nodes[parentID]
				if !found {
					return invalid("node %s has missing parent %s", id, parentID)
				}
				if leafKind(parent.Kind) {
					return invalid("node %s cannot contain child %s", parentID, id)
				}
			}
			id = parentID
		}
		for id = node.ID; id != "" && seen[id] == visiting; id = nodes[id].ParentID {
			seen[id] = visited
		}
	}
	edges := make(map[string]bool, len(graph.Edges))
	for _, edge := range graph.Edges {
		if err := validateEdge(edge); err != nil {
			return err
		}
		if edges[edge.ID] {
			return invalid("duplicate edge %s", edge.ID)
		}
		edges[edge.ID] = true
		if _, found := nodes[edge.From]; !found {
			return invalid("edge %s has missing source %s", edge.ID, edge.From)
		}
		if _, found := nodes[edge.To]; !found {
			return invalid("edge %s has missing target %s", edge.ID, edge.To)
		}
	}
	if graph.SchemaVersion == SemanticVersion {
		return validateSemantic(graph, nodes)
	}
	if len(graph.Changes) > 0 {
		return invalid("temporal changes require schema_version 2")
	}
	for _, n := range graph.Nodes {
		if n.Kind == KindArtifact || len(n.Axioms) > 0 {
			return invalid("node %s requires schema_version 2", n.ID)
		}
	}
	return nil
}

func orderedGraph(graph Graph) Graph {
	graph.Changes = slices.Clone(graph.Changes)
	slices.SortFunc(graph.Changes, func(a, b Change) int { return strings.Compare(a.ID, b.ID) })
	graph.Nodes = append([]Node{}, graph.Nodes...)
	graph.Edges = append([]Edge{}, graph.Edges...)
	slices.SortFunc(graph.Nodes, func(a, b Node) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(graph.Edges, func(a, b Edge) int { return strings.Compare(a.ID, b.ID) })
	return graph
}

func MarshalYAML(graph Graph) ([]byte, error) {
	if err := Validate(graph); err != nil {
		return nil, err
	}
	graph = orderedGraph(graph)
	for i, node := range graph.Nodes {
		if node.Properties != nil {
			graph.Nodes[i].Properties = yamlProperties(node.Properties)
		}
	}
	return yaml.Marshal(graph)
}

func yamlProperties(properties map[string]any) map[string]any {
	result := make(map[string]any, len(properties))
	for key, value := range properties {
		result[key] = yamlValue(value)
	}
	return result
}

func yamlValue(value any) any {
	switch value := value.(type) {
	case json.Number:
		tag := "!!int"
		if strings.ContainsAny(value.String(), ".eE") {
			tag = "!!float"
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value.String()}
	case map[string]any:
		return yamlProperties(value)
	case []any:
		result := make([]any, len(value))
		for i, child := range value {
			result[i] = yamlValue(child)
		}
		return result
	default:
		return value
	}
}

package ontology

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const DefaultBudget = 2.5

const (
	Domain       = "Domain"
	Metric       = "Metric"
	Concept      = "Concept"
	ArtifactNode = "Artifact"

	Active     = "active"
	Deprecated = "deprecated"

	FeishuMarkdown = "feishu_md"
	DuckDBTable    = "duckdb_table"
	TrackingAPI    = "tracking_api"
)

var ErrUnknownNode = errors.New("ontology node not found")

type Node struct {
	ID         string         `json:"id" yaml:"id"`
	DomainID   string         `json:"domain_id" yaml:"domain_id,omitempty"`
	NodeType   string         `json:"node_type" yaml:"node_type"`
	Label      string         `json:"label" yaml:"label"`
	Status     string         `json:"status" yaml:"status"`
	Properties map[string]any `json:"properties" yaml:"properties"`
	Axioms     []Axiom        `json:"axioms" yaml:"axioms"`
	UpdatedAt  string         `json:"updated_at,omitempty" yaml:"updated_at,omitempty"`
}

type Axiom struct {
	Type string `json:"type" yaml:"type"`
	Rule string `json:"rule" yaml:"rule"`
}

type Edge struct {
	ID        string         `json:"id,omitempty" yaml:"id,omitempty"`
	SrcID     string         `json:"src_id" yaml:"src_id"`
	DstID     string         `json:"dst_id" yaml:"dst_id"`
	Predicate string         `json:"predicate" yaml:"predicate"`
	LabelZH   string         `json:"label_zh" yaml:"label_zh"`
	Cost      float64        `json:"cost" yaml:"cost"`
	Metadata  map[string]any `json:"metadata" yaml:"metadata"`
}

type EdgeKey struct {
	SrcID     string `json:"src_id" yaml:"src_id"`
	DstID     string `json:"dst_id" yaml:"dst_id"`
	Predicate string `json:"predicate" yaml:"predicate"`
}

func (edge Edge) Key() EdgeKey {
	return EdgeKey{SrcID: edge.SrcID, DstID: edge.DstID, Predicate: edge.Predicate}
}

type Artifact struct {
	NodeID       string         `json:"node_id" yaml:"node_id"`
	Label        string         `json:"label,omitempty" yaml:"label,omitempty"`
	ArtifactType string         `json:"artifact_type" yaml:"artifact_type"`
	ReadMode     string         `json:"read_mode" yaml:"read_mode"`
	PhysicalRef  string         `json:"physical_ref" yaml:"physical_ref"`
	SourceURL    string         `json:"source_url" yaml:"source_url"`
	SchemaMeta   map[string]any `json:"schema_meta" yaml:"schema_meta"`
	SchemaHash   string         `json:"schema_hash,omitempty" yaml:"schema_hash,omitempty"`
	UpdatedAt    string         `json:"updated_at,omitempty" yaml:"updated_at,omitempty"`
}

type DraftPatch struct {
	Nodes        []Node     `json:"nodes" yaml:"nodes"`
	Edges        []Edge     `json:"edges" yaml:"edges"`
	Artifacts    []Artifact `json:"artifacts" yaml:"artifacts"`
	RemovedNodes []string   `json:"removed_nodes,omitempty" yaml:"removed_nodes,omitempty"`
	RemovedEdges []EdgeKey  `json:"removed_edges,omitempty" yaml:"removed_edges,omitempty"`
}

type Subgraph struct {
	Nodes []Node `json:"nodes"`
	Edges []Edge `json:"edges"`
}

type FrontierNode struct {
	NodeID   string `json:"node_id"`
	Label    string `json:"label"`
	ViaEdge  string `json:"via_edge"`
	FromNode string `json:"from_node"`
}

type NodeContext struct {
	SeedNodeID    string         `json:"seed_node_id"`
	Budget        float64        `json:"budget"`
	Subgraph      Subgraph       `json:"subgraph"`
	Artifacts     []Artifact     `json:"artifacts"`
	FrontierNodes []FrontierNode `json:"frontier_nodes"`
}

var nodeIDPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]{0,127}$`)
var tableNamePattern = regexp.MustCompile(`^cfg_[a-z0-9_]+$`)

func ValidNodeID(id string) bool {
	return nodeIDPattern.MatchString(id)
}

func NormalizeNode(node Node) (Node, error) {
	if !ValidNodeID(node.ID) || strings.TrimSpace(node.Label) == "" {
		return Node{}, fmt.Errorf("invalid node ID or label: %q", node.ID)
	}
	switch node.NodeType {
	case Domain:
		if node.DomainID != "" {
			return Node{}, fmt.Errorf("domain %s cannot belong to another domain", node.ID)
		}
	case Metric, Concept, ArtifactNode:
	default:
		return Node{}, fmt.Errorf("unknown node type %q", node.NodeType)
	}
	if node.Status == "" {
		node.Status = Active
	}
	if node.Status != Active && node.Status != Deprecated {
		return Node{}, fmt.Errorf("unknown node status %q", node.Status)
	}
	if node.Properties == nil {
		node.Properties = map[string]any{}
	}
	if node.Axioms == nil {
		node.Axioms = []Axiom{}
	}
	for _, axiom := range node.Axioms {
		switch axiom.Type {
		case "Mandatory_Check", "Pitfall", "Cross_Validation", "Pruning_Condition":
		default:
			return Node{}, fmt.Errorf("unknown axiom type %q", axiom.Type)
		}
		if strings.TrimSpace(axiom.Rule) == "" {
			return Node{}, errors.New("axiom rule is empty")
		}
	}
	return node, nil
}

func NormalizeEdge(edge Edge) (Edge, error) {
	if !ValidNodeID(edge.SrcID) || !ValidNodeID(edge.DstID) {
		return Edge{}, errors.New("invalid edge endpoints")
	}
	var label string
	cost := 0.5
	switch edge.Predicate {
	case "depends_on":
		label = "口径依赖"
	case "impacted_by":
		label, cost = "受影响于", 1
	case "defined_in":
		label = "定义于"
	case "configured_by":
		label = "配置于"
	case "foreign_key_to":
		label = "外键关联"
	default:
		return Edge{}, fmt.Errorf("unknown predicate %q", edge.Predicate)
	}
	if edge.Cost != 0 && edge.Cost != cost {
		return Edge{}, fmt.Errorf("predicate %s requires cost %g", edge.Predicate, cost)
	}
	edge.Cost = cost
	if strings.TrimSpace(edge.LabelZH) == "" {
		edge.LabelZH = label
	}
	if edge.ID == "" {
		edge.ID = edge.SrcID + ":" + edge.Predicate + ":" + edge.DstID
	}
	if edge.Metadata == nil {
		edge.Metadata = map[string]any{}
	}
	return edge, nil
}

func NormalizeArtifact(artifact Artifact) (Artifact, error) {
	if !ValidNodeID(artifact.NodeID) || strings.TrimSpace(artifact.PhysicalRef) == "" {
		return Artifact{}, errors.New("invalid artifact node or physical reference")
	}
	var mode string
	switch artifact.ArtifactType {
	case FeishuMarkdown:
		mode = "full_text"
		ref := "docs/" + artifact.NodeID + ".md"
		if artifact.PhysicalRef == artifact.NodeID+".md" {
			artifact.PhysicalRef = ref
		}
		if artifact.PhysicalRef != ref {
			return Artifact{}, fmt.Errorf("invalid Markdown reference %q", artifact.PhysicalRef)
		}
	case DuckDBTable:
		mode = "sql_on_demand"
		if !tableNamePattern.MatchString(artifact.PhysicalRef) {
			return Artifact{}, fmt.Errorf("invalid DuckDB table reference %q", artifact.PhysicalRef)
		}
	case TrackingAPI:
		mode = "api_filter"
	default:
		return Artifact{}, fmt.Errorf("unknown artifact type %q", artifact.ArtifactType)
	}
	if artifact.ReadMode == "" {
		artifact.ReadMode = mode
	}
	if artifact.ReadMode != mode {
		return Artifact{}, fmt.Errorf("artifact type %s requires read mode %s", artifact.ArtifactType, mode)
	}
	if artifact.SchemaMeta == nil {
		artifact.SchemaMeta = map[string]any{}
	}
	return artifact, nil
}

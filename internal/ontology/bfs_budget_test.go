package ontology

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
)

type parityExpectation struct {
	Nodes         []string       `json:"nodes"`
	Edges         []EdgeKey      `json:"edges"`
	FrontierNodes []FrontierNode `json:"frontier_nodes"`
}

func TestBudgetParityFixtures(t *testing.T) {
	data, err := os.ReadFile("../../testdata/budget-parity.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Graph DraftPatch `json:"graph"`
		Cases []struct {
			Name              string            `json:"name"`
			NodeID            string            `json:"node_id"`
			Budget            float64           `json:"budget"`
			IncludeDeprecated bool              `json:"include_deprecated"`
			Expected          parityExpectation `json:"expected"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	graph := NewGraph(fixture.Graph.Nodes, fixture.Graph.Edges, fixture.Graph.Artifacts)
	for _, test := range fixture.Cases {
		t.Run(test.Name, func(t *testing.T) {
			result, err := graph.GetNodeContext(test.NodeID, test.Budget, test.IncludeDeprecated)
			if err != nil {
				t.Fatal(err)
			}
			actual := parityExpectation{Nodes: []string{}, Edges: []EdgeKey{}, FrontierNodes: result.FrontierNodes}
			for _, node := range result.Subgraph.Nodes {
				actual.Nodes = append(actual.Nodes, node.ID)
			}
			for _, edge := range result.Subgraph.Edges {
				actual.Edges = append(actual.Edges, edge.Key())
			}
			if !reflect.DeepEqual(actual, test.Expected) {
				t.Fatalf("got %+v\nwant %+v", actual, test.Expected)
			}
			if test.Budget >= 0.5 && (len(result.Artifacts) != 1 || result.Artifacts[0].NodeID != "Artifact_Retention") {
				t.Fatalf("unexpected artifact bindings: %+v", result.Artifacts)
			}
		})
	}

	if _, err := graph.GetNodeContext("Concept_Deprecated", DefaultBudget, false); !errors.Is(err, ErrUnknownNode) {
		t.Fatalf("unexpected deprecated-seed error: %v", err)
	}
	if _, err := graph.GetNodeContext("Metric_Retention", -1, false); err == nil {
		t.Fatal("negative budget was accepted")
	}
}

func TestGraphSnapshotIsIndependentOfCaller(t *testing.T) {
	nodes := []Node{{ID: "Metric_Retention", Properties: map[string]any{"aliases": []string{"留存率"}}}}
	graph := NewGraph(nodes, nil, nil)
	nodes[0].Properties["aliases"].([]string)[0] = "错误"
	view := graph.Data()
	view.Nodes[0].Properties["aliases"].([]string)[0] = "再次改动"
	skeleton := graph.Skeleton("")
	if len(skeleton.Nodes) != 1 || !reflect.DeepEqual(skeleton.Nodes[0].Aliases, []string{"留存率"}) {
		t.Fatalf("graph escaped mutable data: %+v", skeleton)
	}
}

func TestNodeContextExposesQueryableConfigurationAlias(t *testing.T) {
	const id = "Artifact_Config"
	const revision = "cfg_rev_0123456789abcdef0123456789abcdef"
	graph := NewGraph([]Node{{ID: id, NodeType: ArtifactNode, Status: Active}}, nil, []Artifact{{
		NodeID: id, ArtifactType: DuckDBTable, PhysicalRef: revision,
		SchemaMeta: map[string]any{"table_name": "cfg_activity"},
	}})
	view, err := graph.GetNodeContext(id, DefaultBudget, false)
	if err != nil || len(view.Artifacts) != 1 || view.Artifacts[0].PhysicalRef != "cfg_activity" {
		t.Fatalf("configuration context must advertise its logical SQL name: %+v, %v", view, err)
	}
	stored, _ := graph.Artifact(id)
	if stored.PhysicalRef != revision || graph.Data().Artifacts[0].PhysicalRef != revision {
		t.Fatal("consumer projection changed the Worker binding")
	}
}

package index

import (
	"slices"
	"strings"
	"time"
)

// Axiom is a short operational rule. Source bodies remain in their repositories.
type Axiom struct {
	Kind string `json:"kind" yaml:"kind"`
	Text string `json:"text" yaml:"text"`
}

// Change binds one observed occurrence to stable graph nodes. Its time interval
// is half-open; a record without an end represents an instant.
type Change struct {
	ID       string     `json:"id" yaml:"id"`
	Kind     string     `json:"kind" yaml:"kind"`
	Label    string     `json:"label" yaml:"label"`
	NodeIDs  []string   `json:"node_ids" yaml:"node_ids"`
	StartsAt string     `json:"starts_at" yaml:"starts_at"`
	EndsAt   string     `json:"ends_at,omitempty" yaml:"ends_at,omitempty"`
	Scope    string     `json:"scope,omitempty" yaml:"scope,omitempty"`
	Ref      *Reference `json:"ref,omitempty" yaml:"ref,omitempty"`
}

func property(n Node, key string) string { value, _ := n.Properties[key].(string); return value }

func validateSemantic(g Graph, nodes map[string]Node) error {
	physical := map[[2]string]string{}
	for _, n := range g.Nodes {
		parent := nodes[n.ParentID]
		switch n.Kind {
		case KindDomain:
			if n.ParentID != "" && parent.Kind != KindDomain {
				return invalid("domain %s requires a domain parent", n.ID)
			}
		case KindConcept, KindMetric:
			if parent.Kind != KindDomain {
				return invalid("node %s requires a domain parent", n.ID)
			}
		case KindArtifact:
			if n.ParentID == "" {
				return invalid("artifact %s requires a parent", n.ID)
			}
			if !slices.Contains([]string{"document", "config_table", "event_spec", "tool", "skill"}, property(n, "artifact_type")) {
				return invalid("artifact %s requires properties.artifact_type", n.ID)
			}
			if err := ValidateReference(*n.Ref); err != nil {
				return invalid("artifact %s: %v", n.ID, err)
			}
			key := [2]string{n.Ref.Repository, n.Ref.Path}
			if other, ok := physical[key]; ok {
				return invalid("artifacts %s and %s address the same source", n.ID, other)
			}
			physical[key] = n.ID
		default:
			return invalid("node %s must use domain, concept, metric or artifact", n.ID)
		}
		if n.Ref != nil && n.Kind != KindArtifact {
			return invalid("only artifacts have source bindings (%s)", n.ID)
		}
		for _, a := range n.Axioms {
			if !slices.Contains([]string{"co_check", "pitfall", "cross_check", "prune"}, a.Kind) || strings.TrimSpace(a.Text) == "" || !textValid(a.Text, 2048, true) {
				return invalid("invalid axiom on %s", n.ID)
			}
		}
		for k, allowed := range map[string][]string{"hub_role": {"sink", "source"}, "lifecycle": {"active", "deprecated"}, "cognitive_layer": {"S0", "S1", "S2", "S3"}} {
			if n.Properties[k] != nil && !slices.Contains(allowed, property(n, k)) {
				return invalid("invalid %s on %s", k, n.ID)
			}
		}
		layer := property(n, "cognitive_layer")
		if layer != "" && (n.Kind == KindArtifact && layer != "S0" || n.Kind == KindMetric && layer != "S3" || n.Kind == KindConcept && layer != "S1" && layer != "S2") {
			return invalid("node %s is on the wrong cognitive layer", n.ID)
		}
	}
	type signature struct{ from, to, relation, field, target, anchor string }
	seen := map[signature]string{}
	adjacent := map[string][]string{}
	indegree := map[string]int{}
	for _, e := range g.Edges {
		a, b := nodes[e.From], nodes[e.To]
		if e.From == e.To || property(a, "hub_role") == "sink" {
			return invalid("edge %s leaves a terminal hub or refers to itself", e.ID)
		}
		valid := false
		switch e.Relation {
		case "depends_on":
			valid = a.Kind == KindMetric && (b.Kind == KindArtifact || b.Kind == KindMetric)
		case "impacted_by":
			valid = a.Kind == KindMetric && b.Kind == KindConcept
		case "defined_in":
			valid = a.Kind == KindConcept && b.Kind == KindArtifact
		case "configured_by":
			valid = a.Kind == KindConcept && b.Kind == KindArtifact && property(b, "artifact_type") == "config_table"
		case "foreign_key_to":
			valid = property(a, "artifact_type") == "config_table" && property(b, "artifact_type") == "config_table" && a.Kind == KindArtifact && b.Kind == KindArtifact && e.SourceField != "" && e.TargetField != ""
		case "collaborates_with":
			valid = a.Kind == KindConcept && b.Kind == KindConcept
		}
		if !valid {
			return invalid("edge %s violates the endpoint or field contract for %s", e.ID, e.Relation)
		}
		if e.Relation != "foreign_key_to" && (e.SourceField != "" || e.TargetField != "" || e.Separator != "") {
			return invalid("foreign key fields on non-FK edge %s", e.ID)
		}
		if !textValid(e.Anchor, 512, false) || !textValid(e.SourceField, 256, false) || !textValid(e.TargetField, 256, false) || !textValid(e.Separator, 8, false) || e.LineStart < 0 || e.LineEnd < 0 || (e.LineEnd > 0 && (e.LineStart == 0 || e.LineEnd < e.LineStart)) {
			return invalid("invalid locator or field metadata on edge %s", e.ID)
		}
		la, lb := property(a, "cognitive_layer"), property(b, "cognitive_layer")
		if la != "" && lb != "" && la < lb {
			return invalid("edge %s points upward from %s to %s", e.ID, la, lb)
		}
		key := signature{a.ID, b.ID, e.Relation, e.SourceField, e.TargetField, e.Anchor}
		if other, ok := seen[key]; ok {
			return invalid("duplicate edges %s and %s", e.ID, other)
		}
		seen[key] = e.ID
		adjacent[a.ID] = append(adjacent[a.ID], b.ID)
		indegree[b.ID]++
	}
	// Topological sort also rejects reversed relation pairs with different labels.
	queue := []string{}
	for id := range nodes {
		if indegree[id] == 0 {
			queue = append(queue, id)
		}
	}
	for i := 0; i < len(queue); i++ {
		for _, to := range adjacent[queue[i]] {
			indegree[to]--
			if indegree[to] == 0 {
				queue = append(queue, to)
			}
		}
	}
	if len(queue) != len(nodes) {
		for _, e := range g.Edges {
			if indegree[e.From] > 0 && indegree[e.To] > 0 {
				return invalid("directed cycle affects edge %s (%s → %s)", e.ID, e.From, e.To)
			}
		}
	}
	// A direct link to a shared asset hides its owning aggregate if the same
	// source already reaches that asset through an intermediate concept or table.
	for _, e := range g.Edges {
		if nodes[e.From].Kind != KindConcept || property(nodes[e.To], "hub_role") != "sink" {
			continue
		}
		reached := map[string]bool{}
		candidates := []string{}
		for _, id := range adjacent[e.From] {
			if id != e.To {
				candidates = append(candidates, id)
			}
		}
		for i := 0; i < len(candidates); i++ {
			id := candidates[i]
			if id == e.To {
				return invalid("edge %s bypasses the intermediate aggregate on the path to %s", e.ID, e.To)
			}
			if reached[id] {
				continue
			}
			reached[id] = true
			candidates = append(candidates, adjacent[id]...)
		}
	}
	ids := map[string]bool{}
	for _, c := range g.Changes {
		if !validID(c.ID) || ids[c.ID] || !slices.Contains([]string{"version", "activity", "experiment", "source"}, c.Kind) || strings.TrimSpace(c.Label) == "" || !textValid(c.Label, 256, false) || !textValid(c.Scope, 256, false) || len(c.NodeIDs) == 0 {
			return invalid("invalid temporal record %s", c.ID)
		}
		ids[c.ID] = true
		if _, _, err := changeInterval(c); err != nil {
			return err
		}
		if c.Ref != nil {
			if err := ValidateReference(*c.Ref); err != nil {
				return err
			}
		}
		for _, id := range c.NodeIDs {
			if _, ok := nodes[id]; !ok {
				return invalid("change %s has missing node %s", c.ID, id)
			}
		}
	}
	return nil
}

func changeInterval(c Change) (time.Time, time.Time, error) {
	start, err := time.Parse(time.RFC3339, c.StartsAt)
	if err != nil {
		return time.Time{}, time.Time{}, invalid("change %s needs an RFC3339 start time", c.ID)
	}
	if c.EndsAt == "" {
		return start, start, nil
	}
	end, err := time.Parse(time.RFC3339, c.EndsAt)
	if err != nil || !end.After(start) {
		return start, end, invalid("change %s has invalid end time", c.ID)
	}
	return start, end, nil
}

type Warning struct {
	NodeID  string `json:"node_id"`
	Message string `json:"message"`
}

func Warnings(g Graph) []Warning {
	count := map[string]int{}
	degree := map[string]int{}
	for _, n := range g.Nodes {
		if n.Kind == KindConcept || n.Kind == KindMetric {
			count[n.ParentID]++
		}
	}
	for _, e := range g.Edges {
		degree[e.From]++
		degree[e.To]++
	}
	out := []Warning{}
	for _, n := range g.Nodes {
		if n.Kind == KindDomain && count[n.ID] > 15 {
			out = append(out, Warning{n.ID, "域内核心概念和指标超过 15 个，请按业务职责拆分子域。"})
		}
		if (n.Kind == KindConcept || n.Kind == KindMetric) && property(n, "hub_role") == "" && degree[n.ID] > 10 {
			out = append(out, Warning{n.ID, "普通节点的业务连线超过 10 条，请核对职责和粒度。"})
		}
	}
	return out
}

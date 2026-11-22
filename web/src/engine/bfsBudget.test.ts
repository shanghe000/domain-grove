import { deepStrictEqual, equal, notEqual } from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { describe, it } from 'node:test';

import type { Graph, EdgeKey } from '../types.ts';
import { bfsBudget, createBudgetTraversal } from './bfsBudget.ts';
import type { FrontierNode } from './bfsBudget.ts';
import { adaptIndexGraph } from './indexPortal.ts';

const fixture: {
  graph: Graph;
  cases: {
    name: string;
    node_id: string;
    budget: number;
    include_deprecated: boolean;
    expected: { nodes: string[]; edges: EdgeKey[]; frontier_nodes: FrontierNode[] };
  }[];
} = JSON.parse(readFileSync(new URL('../../../testdata/budget-parity.json', import.meta.url), 'utf8'));

describe('Go and Web budget context parity', () => {
  for (const example of fixture.cases) {
    it(example.name, () => {
      const actual = bfsBudget(fixture.graph, example.node_id, example.budget, example.include_deprecated);
      notEqual(actual, null);
      deepStrictEqual(actual!.subgraph.nodes.map(node => node.id), example.expected.nodes);
      deepStrictEqual(actual!.subgraph.edges.map(({ src_id, dst_id, predicate }) => ({ src_id, dst_id, predicate })),
        example.expected.edges);
      deepStrictEqual(actual!.frontier_nodes, example.expected.frontier_nodes);
      if (example.budget >= 0.5) deepStrictEqual(actual!.artifacts.map(artifact => artifact.node_id), ['Artifact_Retention']);
    });
  }

  it('filters a deprecated seed and invalid budget', () => {
    equal(bfsBudget(fixture.graph, 'Concept_Deprecated'), null);
    equal(bfsBudget(fixture.graph, 'Metric_Retention', -1), null);
  });

  it('emits the same context fields as Go and uses the graph label for an artifact', () => {
    const graph = {
      nodes: fixture.graph.nodes.map(node => ({ ...node, updated_at: 'fixture' })),
      edges: fixture.graph.edges.map(edge => ({ ...edge, id: 'database-edge-id' })),
      artifacts: fixture.graph.artifacts.map(artifact => ({ ...artifact, schema_hash: 'fixture', updated_at: 'fixture' })),
    };
    const result = bfsBudget(graph, 'Metric_Retention')!;
    equal(result.subgraph.nodes.some(node => 'updated_at' in node), false);
    equal(result.subgraph.edges.some(edge => 'id' in edge), false);
    equal(result.artifacts[0].label, graph.nodes.find(node => node.id === result.artifacts[0].node_id)!.label);
    equal('updated_at' in result.artifacts[0] || 'schema_hash' in result.artifacts[0], false);
  });
});

it('retains the original IDs, directions and labels of parallel index relations', () => {
  const graph = adaptIndexGraph({
    schema_version: 1, revision: 1,
    nodes: [
      { id: 'metric', kind: 'metric', label: '次日留存' },
      { id: 'document', kind: 'document', label: '口径', parent_id: 'metric' },
    ],
    edges: [
      { id: 'definition', from: 'metric', to: 'document', relation: 'defined_in' },
      { id: 'audit', from: 'metric', to: 'document', relation: 'audited_by' },
      { id: 'ownership', from: 'metric', to: 'document', relation: 'owned_by' },
      { id: 'reference', from: 'document', to: 'metric', relation: 'references' },
    ],
  });
  deepStrictEqual(graph.nodes.map(node => [node.id, node.node_type, node.domain_id]), [
    ['metric', 'Metric', ''], ['document', 'Artifact', 'metric'],
  ]);
  const result = createBudgetTraversal(graph, edge => edge.id!)('metric')!;
  deepStrictEqual(result.subgraph.edges.map(edge => [edge.id, edge.src_id, edge.dst_id, edge.label_zh]), [
    ['reference', 'document', 'metric', 'references'],
    ['definition', 'metric', 'document', 'defined_in'],
    ['audit', 'metric', 'document', 'audited_by'],
    ['ownership', 'metric', 'document', 'owned_by'],
  ]);
});

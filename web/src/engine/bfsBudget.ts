import type { Artifact, Edge, Graph, Node } from '../types.ts';
import { edgeKey } from '../types.ts';

export const DEFAULT_BUDGET = 2.5;

export interface FrontierNode {
  node_id: string;
  label: string;
  via_edge: string;
  from_node: string;
}

export interface BudgetContext {
  seed_node_id: string;
  budget: number;
  subgraph: { nodes: Node[]; edges: Edge[] };
  artifacts: Artifact[];
  frontier_nodes: FrontierNode[];
}

type Candidate = { nodeId: string; cost: number };

const compare = (a: string, b: string): number => a < b ? -1 : a > b ? 1 : 0;
const compareEdges = (a: Edge, b: Edge): number =>
  compare(a.src_id, b.src_id) || compare(a.dst_id, b.dst_id) || compare(a.predicate, b.predicate);
const compareCost = (a: Candidate, b: Candidate): number => a.cost - b.cost || compare(a.nodeId, b.nodeId);

class VisitQueue {
  private items: Candidate[] = [];

  push(value: Candidate) {
    const array = this.items;
    let index = array.length;
    array.push(value);
    while (index > 0) {
      const parent = (index - 1) >> 1;
      if (compareCost(value, array[parent]) >= 0) break;
      array[index] = array[parent];
      index = parent;
    }
    array[index] = value;
  }

  pop(): Candidate | undefined {
    const array = this.items;
    if (!array.length) return undefined;
    const first = array[0];
    const last = array.pop()!;
    if (!array.length) return first;
    let index = 0;
    while (index * 2 + 1 < array.length) {
      let child = index * 2 + 1;
      if (child + 1 < array.length && compareCost(array[child + 1], array[child]) < 0) child++;
      if (compareCost(last, array[child]) <= 0) break;
      array[index] = array[child];
      index = child;
    }
    array[index] = last;
    return first;
  }
}

function appendEdge(index: Map<string, Edge[]>, id: string, edge: Edge) {
  const existing = index.get(id);
  if (existing) existing.push(edge);
  else index.set(id, [edge]);
}

/** Legacy traversal matches internal/ontology/bfs_budget.go. Index graphs can retain parallel relations by ID. */
export function createBudgetTraversal(graph: Graph, relationIdentity?: (edge: Edge) => string) {
  const nodes = new Map(graph.nodes.map(node => [node.id, node]));
  const identity = relationIdentity ?? edgeKey;
  const artifacts = new Map(graph.artifacts.map(artifact => [artifact.node_id, artifact]));
  const outgoing = new Map<string, Edge[]>();
  const incoming = new Map<string, Edge[]>();
  for (const edge of graph.edges) {
    appendEdge(outgoing, edge.src_id, edge);
    appendEdge(incoming, edge.dst_id, edge);
  }

  return (seed: string, budget = DEFAULT_BUDGET, includeDeprecated = false): BudgetContext | null => {
    const allowed = (id: string) => {
      const node = nodes.get(id);
      return !!node && (includeDeprecated || node.status !== 'deprecated');
    };
    if (!Number.isFinite(budget) || budget < 0 || !allowed(seed)) return null;

    const best = new Map([[seed, 0]]);
    const accepted = new Set([seed]);
    const edges = new Map<string, Edge>();
    const frontier = new Map<string, { edge: Edge; cost: number }>();
    const queue = new VisitQueue();
    queue.push({ nodeId: seed, cost: 0 });

    for (let current = queue.pop(); current; current = queue.pop()) {
      if (current.cost !== best.get(current.nodeId)) continue;
      for (const edge of outgoing.get(current.nodeId) ?? []) {
        if (!allowed(edge.dst_id)) continue;
        const cost = current.cost + edge.cost;
        const previous = best.get(edge.dst_id);
        if (cost > budget) {
          const boundary = frontier.get(edge.dst_id);
          if (!boundary || cost < boundary.cost || (cost === boundary.cost && compareEdges(edge, boundary.edge) < 0)) {
            frontier.set(edge.dst_id, { edge, cost });
          }
          continue;
        }
        accepted.add(edge.dst_id);
        edges.set(identity(edge), edge);
        if (previous === undefined || cost < previous) {
          best.set(edge.dst_id, cost);
          queue.push({ nodeId: edge.dst_id, cost });
        }
      }
    }

    // Incoming neighbors may be included, but never seed more traversal.
    if (budget >= 2) {
      for (const edge of incoming.get(seed) ?? []) {
        if (!allowed(edge.src_id)) continue;
        accepted.add(edge.src_id);
        edges.set(identity(edge), edge);
      }
    }

    const included = [...accepted].sort(compare);
    return {
      seed_node_id: seed,
      budget,
      subgraph: {
        nodes: included.map(id => {
          const { updated_at: _updatedAt, ...node } = nodes.get(id)!;
          return node;
        }),
        edges: [...edges.values()].sort(compareEdges).map(edge => {
          if (relationIdentity) return edge;
          const { id: _id, ...legacy } = edge;
          return legacy;
        }),
      },
      artifacts: included.flatMap(id => {
        const artifact = artifacts.get(id);
        if (!artifact) return [];
        const { schema_hash: _hash, updated_at: _updatedAt, ...binding } = artifact;
        return [{ ...binding, label: nodes.get(id)!.label }];
      }),
      frontier_nodes: [...frontier.keys()].filter(id => !accepted.has(id)).sort(compare).map(id => {
        const { edge } = frontier.get(id)!;
        return { node_id: id, label: nodes.get(id)!.label, via_edge: edge.predicate, from_node: edge.src_id };
      }),
    };
  };
}

export function bfsBudget(graph: Graph, seed: string, budget = DEFAULT_BUDGET, includeDeprecated = false) {
  return createBudgetTraversal(graph)(seed, budget, includeDeprecated);
}

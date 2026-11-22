import type { Graph as IndexGraph } from '../IndexApp';
import type { Graph, NodeType, Predicate } from '../types';

const kinds: Record<IndexGraph['nodes'][number]['kind'], NodeType> = {
  domain:'Domain', concept:'Concept', metric:'Metric', artifact:'Artifact', document:'Artifact', skill:'Artifact', tool:'Artifact',
};
export const relationNames: Record<string,string> = {
  depends_on:'口径依赖', impacted_by:'受影响于', defined_in:'定义于', configured_by:'配置于', foreign_key_to:'外键关联', collaborates_with:'业务协作',
};
export const axiomNames: Record<string,string> = {co_check:'连带排查', pitfall:'口径防坑', cross_check:'交叉核对', prune:'剪枝停止'};
const axiomTypes = {co_check:'Mandatory_Check', pitfall:'Pitfall', cross_check:'Cross_Validation', prune:'Pruning_Condition'} as const;

export function adaptIndexGraph(graph: IndexGraph): Graph {
  return {
    nodes: graph.nodes.map(node => ({
      id:node.id, domain_id:node.parent_id ?? '', node_type:kinds[node.kind], label:node.label,
      status:node.properties?.lifecycle === 'deprecated' ? 'deprecated' : 'active', properties:node.properties ?? {},
      axioms:(node.axioms ?? []).map(a => ({type:axiomTypes[a.kind as keyof typeof axiomTypes], rule:a.text})),
    })),
    edges: graph.edges.map(edge => {
      const predicate = Object.hasOwn(relationNames,edge.relation) ? edge.relation as Predicate : 'related';
      return {id:edge.id, src_id:edge.from, dst_id:edge.to, predicate, label_zh:graph.schema_version<2 ? edge.relation : relationNames[edge.relation] ?? edge.relation,
        cost:['impacted_by','collaborates_with','related'].includes(predicate) ? 1 : 0.5, metadata:{...edge,
          join_clause:edge.source_field && edge.target_field ? edge.source_field+' → '+edge.target_field : ''}};
    }),
    artifacts: [],
  };
}

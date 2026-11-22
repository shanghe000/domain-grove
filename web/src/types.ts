export type NodeType = 'Domain' | 'Metric' | 'Concept' | 'Artifact'
export type Predicate = 'depends_on' | 'impacted_by' | 'defined_in' | 'configured_by' | 'foreign_key_to' | 'collaborates_with' | 'aggregate' | 'membership' | 'related'
export type AxiomType = 'Mandatory_Check' | 'Pitfall' | 'Cross_Validation' | 'Pruning_Condition'

export interface Axiom {
  type: AxiomType
  rule: string
}

export interface Node {
  id: string
  domain_id: string
  node_type: NodeType
  label: string
  status: 'active' | 'deprecated'
  properties: Record<string, unknown>
  axioms: Axiom[]
  updated_at?: string
}

export interface EdgeKey {
  src_id: string
  dst_id: string
  predicate: Predicate
}

export interface Edge extends EdgeKey {
  id?: string
  label_zh: string
  cost: number
  metadata: Record<string, unknown>
}

export interface Artifact {
  node_id: string
  label?: string
  artifact_type: 'feishu_md' | 'duckdb_table' | 'tracking_api'
  read_mode: 'full_text' | 'sql_on_demand' | 'api_filter'
  physical_ref: string
  source_url: string
  schema_meta: Record<string, unknown>
  schema_hash?: string
  updated_at?: string
}

export interface Graph {
  nodes: Node[]
  edges: Edge[]
  artifacts: Artifact[]
}

export interface DraftPatch extends Graph {
  removed_nodes?: string[]
  removed_edges?: EdgeKey[]
}

export interface Draft {
  session_id: string
  created_by: string
  status: 'staging' | 'committed' | 'discarded'
  source_type: string
  draft_patch: DraftPatch
  focus_domain_id: string
  created_at: string
  updated_at: string
  undo_count: number
  base_version: string
}

export interface Identity { user_id: string; username: string; dept: string }
export interface Progress { tool: string; status: string; text: string }
export interface ChatMessage { role: 'user' | 'assistant' | 'error'; text: string }

export interface WorkbenchState {
  graph: Graph
  version: string
  draft: Draft | null
  user: Identity
  mock: boolean
}

export type Operation =
  | { op: 'upsert_node'; node: Node }
  | { op: 'upsert_edge'; edge: Edge }
  | ({ op: 'remove_edge' } & EdgeKey)
  | { op: 'move_node_domain'; node_id: string; target_domain: string }
  | { op: 'remove_node'; node_id: string }
  | { op: 'add_axiom'; node_id: string; axiom: Axiom }
  | { op: 'update_axiom'; node_id: string; axiom_index: number; axiom: Axiom }
  | { op: 'remove_axiom'; node_id: string; axiom_index: number }
  | { op: 'update_artifact_column'; node_id: string; column_name: string; comment?: string; confidence?: 'low' | 'high' }

export const edgeKey = (edge: EdgeKey): string => `${edge.src_id}/${edge.predicate}/${edge.dst_id}`

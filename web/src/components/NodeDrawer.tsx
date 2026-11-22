import { Fragment, useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import type { Artifact, Graph, Node, Operation } from "../types";
import { edgeKey } from "../types";
import { PanelIcon } from "./ChatSidebar";
import "./panels.css";

interface Props {
  node: Node;
  artifact?: Artifact;
  graph: Graph;
  drafted: boolean;
  publishedArtifact: boolean;
  busy: boolean;
  onPatch: (operations: Operation[]) => Promise<void>;
  onClose: () => void;
  onEnterDomain?: () => void;
  onInspectNode?: (nodeId: string) => void;
}

type Axiom = Node["axioms"][number];
type AxiomType = Axiom["type"];

const AXIOMS = [
  ["Mandatory_Check", "必查项"],
  ["Pitfall", "常见误区"],
  ["Cross_Validation", "交叉核验"],
  ["Pruning_Condition", "剪枝条件"],
] as const;
const NODE_LABELS = { Domain: "业务领域", Metric: "业务指标", Concept: "业务概念", Artifact: "来源资料" };
const DOC_TYPES = { feishu_md: "飞书文档", duckdb_table: "Excel 配置表", tracking_api: "埋点定义" };
const READ_MODES = { full_text: "读取正文", sql_on_demand: "按需 SQL 查询", api_filter: "按事件筛选" };
const NODE_TONES = { Domain: "domain", Metric: "metric", Concept: "concept", Artifact: "artifact" };
const MEMBER_TYPES = [["Metric", "指标"], ["Concept", "概念"], ["Artifact", "资料"]] as const;
const STANDARD_PROPERTIES = new Set(["aliases", "description", "formula", "evidence_quote", "mock"]);

interface Column {
  name: string;
  duckdb_type?: string;
  comment?: string;
  confidence?: string;
  foreign_key_ref?: { table?: string; column?: string } | null;
}
type Editing =
  | { kind: "details"; label: string; description: string; aliases: string; formula: string }
  | { kind: "axiom"; index?: number; type: AxiomType; rule: string }
  | { kind: "domain"; target: string }
  | { kind: "column"; name: string; comment: string; confidence: "high" | "low" };

function text(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function list(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((item): item is string => typeof item === "string") : [];
}

function columns(value: unknown): Column[] {
  return Array.isArray(value) ? value.filter((item): item is Column =>
    item && typeof item === "object" && typeof item.name === "string") : [];
}

function secureLink(value: string | undefined): string {
  if (!value) return "";
  try {
    const parsed = new URL(value);
    return parsed.protocol === "https:" && !parsed.username && !parsed.password ? parsed.href : "";
  } catch {
    return "";
  }
}

function dateLabel(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : new Intl.DateTimeFormat("zh-CN", {
    dateStyle: "medium", timeStyle: "short",
  }).format(date);
}

function displayProperty(value: unknown): string {
  if (typeof value === "boolean") return value ? "是" : "否";
  if (typeof value === "object" && value !== null) return JSON.stringify(value, null, 2);
  return value === null || value === undefined ? "未填写" : String(value);
}

interface RelatedNode {
  key: string;
  predicate: string;
  label: string;
  other: Node;
  member?: Node;
  join?: string;
}

function NodeLink({ node, onInspect }: { node: Node; onInspect?: (id: string) => void }) {
  return onInspect
    ? <button type="button" className="drawer-node-link" onClick={() => onInspect(node.id)}>{node.label}</button>
    : <span className="drawer-node-name">{node.label}</span>;
}

function RelationshipSection({ node, graph, members, onInspect }: {
  node: Node;
  graph: Graph;
  members: Node[];
  onInspect?: (id: string) => void;
}) {
  const nodes = new Map(graph.nodes.map(item => [item.id, item]));
  const inside = new Set([node.id, ...members.map(member => member.id)]);
  const isDomain = node.node_type === "Domain";
  const outgoing: RelatedNode[] = [];
  const incoming: RelatedNode[] = [];
  let internal = 0;
  for (const edge of graph.edges) {
    const source = nodes.get(edge.src_id);
    const destination = nodes.get(edge.dst_id);
    if (!source || !destination) continue;
    const startsInside = inside.has(source.id);
    const endsInside = inside.has(destination.id);
    if (isDomain && startsInside && endsInside) {
      internal++;
      continue;
    }
    const entry = { key: edgeKey(edge), predicate: edge.predicate, label: edge.label_zh,
      join: text(edge.metadata?.join_clause) };
    if (startsInside) outgoing.push({ ...entry, other: destination, member: isDomain ? source : undefined });
    if (endsInside) incoming.push({ ...entry, other: source, member: isDomain ? destination : undefined });
  }

  function row(relation: RelatedNode) {
    return (
      <li key={relation.key}>
        <div className="drawer-relation-row">
          <span className="drawer-relation-kind" data-predicate={relation.predicate}>{relation.label}</span>
          <NodeLink node={relation.other} onInspect={onInspect} />
        </div>
        {relation.member && <p className="drawer-relation-note">域内节点 {relation.member.label}</p>}
        {relation.join && <code className="drawer-relation-note">{relation.join}</code>}
      </li>
    );
  }

  return (
    <section className="drawer-section" aria-label={isDomain ? "领域关系" : "关联关系"}>
      <div className="drawer-section-heading"><h3>{isDomain ? "领域关系" : "关联关系"}</h3></div>
      {isDomain && <p className="drawer-secondary">域内 {internal} 条，跨域 {incoming.length + outgoing.length} 条</p>}
      {!outgoing.length && !incoming.length && !isDomain && <p className="drawer-secondary">暂无关联关系。</p>}
      {([[isDomain ? "跨域出边" : "出边", outgoing], [isDomain ? "跨域入边" : "入边", incoming]] as const)
        .filter(([, edges]) => edges.length > 0).map(([label, edges]) => (
          <div className="drawer-relation-group" key={label}>
            <h4>{label} <span>{edges.length} 条</span></h4>
            <ul>{edges.slice(0, 4).map(row)}</ul>
            {edges.length > 4 && (
              <details className="drawer-fields">
                <summary>其余 {edges.length - 4} 条</summary>
                <ul>{edges.slice(4).map(row)}</ul>
              </details>
            )}
          </div>
        ))}
    </section>
  );
}

export function NodeDrawer({ node, artifact, graph, drafted, publishedArtifact, busy, onPatch, onClose, onEnterDomain, onInspectNode }: Props) {
  const [editing, setEditing] = useState<Editing | null>(null);
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const [markdown, setMarkdown] = useState("");
  const [reading, setReading] = useState(false);
  const documentRequest = useRef<AbortController | null>(null);
  const closeControl = useRef<HTMLButtonElement>(null);
  const nodeID = node.id;
  const api = "/api/v1/artifacts/" + encodeURIComponent(nodeID);
  const disabled = pending || busy;
  const canEdit = node.node_type === "Metric" || node.node_type === "Concept";
  const properties = node.properties || {};
  const aliases = list(properties.aliases);
  const description = text(properties.description);
  const formula = text(properties.formula);
  const evidence = text(properties.evidence_quote);
  const hasBusinessDetails = Boolean(description || aliases.length || formula || evidence);
  const extraProperties = Object.entries(properties).filter(([key]) => !STANDARD_PROPERTIES.has(key));
  const meta = artifact?.schema_meta || {};
  const schemaColumns = columns(meta.columns);
  const lowColumns = schemaColumns.filter(column => column.confidence === "low");
  const highColumns = schemaColumns.filter(column => column.confidence !== "low");
  const source = secureLink(artifact?.source_url);
  const updatedAt = artifact?.updated_at || node.updated_at || text(meta.fetched_at);
  const domain = graph.nodes.find(value => value.id === node.domain_id);
  const targetDomains = node.node_type === "Domain" ? [] : graph.nodes.filter(
    candidate => candidate.node_type === "Domain" && candidate.status === "active" && candidate.id !== node.domain_id,
  );
  const members = node.node_type === "Domain" ? graph.nodes.filter(candidate => candidate.domain_id === nodeID) : [];
  const axiomCount = node.axioms?.length ?? 0;

  useEffect(() => {
    setEditing(null);
    setError("");
    setMarkdown("");
    setReading(false);
    const previousFocus = document.activeElement;
    closeControl.current?.focus({ preventScroll: true });
    return () => {
      documentRequest.current?.abort();
      if (previousFocus instanceof HTMLElement && previousFocus.isConnected && previousFocus !== document.body) {
        previousFocus.focus({ preventScroll: true });
      }
    };
  }, [nodeID]);

  function start(form: Editing) {
    setError("");
    setEditing(form);
  }

  async function patch(operation: Operation) {
    setPending(true);
    setError("");
    try {
      await onPatch([operation]);
      setEditing(null);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "无法保存此修改，请重试。");
    } finally {
      setPending(false);
    }
  }

  function submitDetails(event: FormEvent) {
    event.preventDefault();
    if (!editing || disabled) return;
    if (editing.kind === "details") {
      const label = editing.label.trim();
      if (!label) return setError("请填写节点名称。");
      const nextAliases = [...new Set(editing.aliases.split(/[,，\n]/).map(item => item.trim()).filter(Boolean))];
      const nextProperties: Record<string, unknown> = { ...properties, description: editing.description.trim(), aliases: nextAliases };
      if (node.node_type === "Metric") nextProperties.formula = editing.formula.trim();
      void patch({ op: "upsert_node", node: { ...node, label, properties: nextProperties } });
    } else if (editing.kind === "axiom") {
      const rule = editing.rule.trim();
      if (!rule) return setError("请填写具体规则。");
      const axiom = { type: editing.type, rule };
      void patch(editing.index === undefined ? { op: "add_axiom", node_id: nodeID, axiom }
        : { op: "update_axiom", node_id: nodeID, axiom_index: editing.index, axiom });
    } else if (editing.kind === "domain") {
      if (!targetDomains.some(candidate => candidate.id === editing.target)) return setError("请选择有效的目标领域。");
      void patch({ op: "move_node_domain", node_id: nodeID, target_domain: editing.target });
    } else {
      void patch({ op: "update_artifact_column", node_id: nodeID, column_name: editing.name,
        comment: editing.comment.trim(), confidence: editing.confidence });
    }
  }

  async function previewDoc() {
    if (markdown) {
      setMarkdown("");
      return;
    }
    setReading(true);
    setError("");
    const request = new AbortController();
    documentRequest.current = request;
    try {
      const response = await fetch(api + "/document", { signal: request.signal });
      const sourceDoc = await response.json();
      if (!response.ok) throw new Error(sourceDoc.error || "无法读取已发布正文。");
      if (!request.signal.aborted) setMarkdown(text(sourceDoc.markdown));
    } catch (cause) {
      if (!request.signal.aborted) setError(cause instanceof Error ? cause.message : "无法读取已发布正文。");
    } finally {
      if (!request.signal.aborted) setReading(false);
    }
  }

  function renderColumn(column: Column) {
    if (editing?.kind === "column" && editing.name === column.name) {
      return (
        <li className="drawer-column drawer-column--pending" key={column.name}>
          <form className="drawer-edit" onSubmit={submitDetails}>
          <strong><code>{column.name}</code></strong>
          <label>字段说明<textarea value={editing.comment} rows={2} maxLength={1000} autoFocus
            onChange={event => setEditing({ ...editing, comment: event.currentTarget.value })} /></label>
          <label>核对状态
            <select value={editing.confidence} onChange={event => setEditing({ ...editing, confidence: event.currentTarget.value as "high" | "low" })}>
              <option value="low">待核对</option>
              <option value="high">已核对</option>
            </select>
          </label>
          {editorActions()}
          </form>
        </li>
      );
    }
    const ref = column.foreign_key_ref;
    return (
      <li className={"drawer-column" + (column.confidence === "low" ? " drawer-column--pending" : "")} key={column.name}>
        <div className="drawer-column-heading">
          <code>{column.name}</code>
          {column.duckdb_type && <span>{column.duckdb_type}</span>}
        </div>
        {column.comment && <p>{column.comment}</p>}
        {ref?.table && ref.column && <p className="drawer-column-ref">外键 <code>{ref.table}.{ref.column}</code></p>}
        {(drafted || (publishedArtifact && column.confidence === "low")) && (
          <button type="button" className="panel-text-button drawer-edit-link" disabled={disabled}
            onClick={() => start({ kind: "column", name: column.name, comment: column.comment || "",
              confidence: column.confidence === "low" ? "low" : "high" })}>
            {column.confidence === "low" ? "核对字段" : "调整说明"}
          </button>
        )}
      </li>
    );
  }

  function editorActions() {
    return (
      <div className="drawer-edit-actions">
        <button type="button" className="panel-text-button" disabled={pending} onClick={() => setEditing(null)}>取消</button>
        <button type="submit" className="drawer-save" disabled={disabled}>{pending ? "保存中…" : "保存到草稿"}</button>
      </div>
    );
  }

  function editAxiom() {
    if (editing?.kind !== "axiom") return null;
    return (
      <form className="drawer-edit" onSubmit={submitDetails}>
        <label>公理类型
          <select value={editing.type} onChange={event => setEditing({ ...editing, type: event.currentTarget.value as AxiomType })}>
            {AXIOMS.map(([type, label]) => <option key={type} value={type}>{label}</option>)}
          </select>
        </label>
        <label>业务规则
          <textarea autoFocus placeholder="写明分析时必须满足的条件" value={editing.rule} rows={3} maxLength={2000} required
            onChange={event => setEditing({ ...editing, rule: event.currentTarget.value })} />
        </label>
        {editorActions()}
      </form>
    );
  }

  function propertyRows() {
    return (
      <>
        {aliases.length > 0 && <><dt>别名</dt><dd>{aliases.join("、")}</dd></>}
        {formula && <><dt>计算口径</dt><dd>{formula}</dd></>}
        {extraProperties.map(([key, value]) => (
          <Fragment key={key}>
            <dt>{key === "intrinsic_fields" ? "业务字段" : key}</dt>
            <dd className="drawer-property-value">{displayProperty(value)}</dd>
          </Fragment>
        ))}
        <dt>节点 ID</dt><dd><code>{nodeID}</code></dd>
        {updatedAt && <><dt>更新时间</dt><dd><time dateTime={updatedAt}>{dateLabel(updatedAt)}</time></dd></>}
      </>
    );
  }

  return (
    <aside className="node-drawer" aria-label="节点详情" aria-busy={pending}
      onKeyDown={event => {
        if (event.key === "Escape" && editing && !pending) {
          event.stopPropagation();
          setEditing(null);
          closeControl.current?.focus({ preventScroll: true });
        }
      }}>
      <header className="drawer-header">
        <div className="drawer-heading-type">
          <span className={"drawer-node-dot drawer-node-dot--" + NODE_TONES[node.node_type]} />
          <span>{artifact ? DOC_TYPES[artifact.artifact_type] : NODE_LABELS[node.node_type]}</span>
          {drafted ? <span className="panel-status panel-status--draft">草稿</span> :
            node.status === "active" && <span className="panel-status panel-status--muted">已发布</span>}
          {node.status === "deprecated" && <span className="panel-status panel-status--muted">已停用</span>}
        </div>
        <button className="panel-icon-button drawer-close" ref={closeControl} type="button" aria-label="关闭节点详情" onClick={onClose}>
          <PanelIcon name="close" />
        </button>
        <h2>{node.label}</h2>
        {domain && <p className="drawer-domain">{domain.label}</p>}
      </header>
      <div className="drawer-content">
        {error && <p className="panel-error drawer-error" role="alert">{error}</p>}
        {artifact?.artifact_type === "duckdb_table" && lowColumns.length > 0 && (
          <section className="drawer-section drawer-low-fields" aria-label="待核对字段">
            <div className="drawer-section-heading"><h3>待核对字段</h3><span>{lowColumns.length} 项</span></div>
            <ul>{lowColumns.map(renderColumn)}</ul>
          </section>
        )}
        {canEdit && (
          <>
            <section className="drawer-section" aria-label="基本属性">
              <div className="drawer-section-heading">
                <h3>基本属性</h3>
                {editing?.kind !== "details" && (
                  <button type="button" className="panel-text-button" disabled={disabled}
                    onClick={() => start({ kind: "details", label: node.label, formula, description, aliases: aliases.join("，") })}>
                    编辑
                  </button>
                )}
              </div>
              {editing?.kind === "details" ? (
                <form className="drawer-edit" onSubmit={submitDetails}>
                  <label>名称<input autoFocus required value={editing.label} maxLength={160}
                    onChange={event => setEditing({ ...editing, label: event.currentTarget.value })} /></label>
                  <label>别名<input value={editing.aliases} maxLength={1000} placeholder="用逗号分隔"
                    onChange={event => setEditing({ ...editing, aliases: event.currentTarget.value })} /></label>
                  <label>业务描述<textarea value={editing.description} rows={3} maxLength={2000}
                    onChange={event => setEditing({ ...editing, description: event.currentTarget.value })} /></label>
                  {node.node_type === "Metric" && (
                    <label>计算口径<input value={editing.formula} maxLength={1000}
                      onChange={event => setEditing({ ...editing, formula: event.currentTarget.value })} /></label>
                  )}
                  {editorActions()}
                </form>
              ) : (
                <div className="drawer-details">
                  {description && <p>{description}</p>}
                  <dl className="drawer-terms">{propertyRows()}</dl>
                  {evidence && <blockquote className="drawer-evidence">{evidence}</blockquote>}
                  {!hasBusinessDetails && !extraProperties.length && <p className="drawer-secondary">可补充业务描述与常用别名。</p>}
                </div>
              )}
            </section>
            <section className="drawer-section" aria-label="业务公理">
              <div className="drawer-section-heading"><h3>业务公理</h3><span>{axiomCount} 条</span></div>
              <div className="drawer-axiom-groups">
                {AXIOMS.map(([type, label]) => {
                  const group = (node.axioms || []).flatMap((value, index) => value.type === type ? [{ value, index }] : []);
                  const adding = editing?.kind === "axiom" && editing.index === undefined && editing.type === type;
                  return (
                    <div className="drawer-axiom-group" key={type}>
                      <div className="drawer-axiom-heading">
                        <h4>{label}</h4>
                        <button className="panel-icon-button" type="button" disabled={disabled} aria-label={"添加" + label}
                          onClick={() => start({ kind: "axiom", type, rule: "" })}><PanelIcon name="add" /></button>
                      </div>
                      {group.map(({ value, index }) => (
                        <div className="drawer-axiom" key={index}>
                          {editing?.kind === "axiom" && editing.index === index ? editAxiom() : (
                            <>
                              <p>{value.rule}</p>
                              <div className="drawer-axiom-actions">
                                <button type="button" className="panel-text-button" disabled={disabled}
                                  aria-label={"编辑公理 " + value.rule} onClick={() => start({ kind: "axiom", type, rule: value.rule, index })}>编辑</button>
                                <button type="button" className="panel-text-button drawer-remove" disabled={disabled}
                                  aria-label={"移除公理 " + value.rule}
                                  onClick={() => void patch({ op: "remove_axiom", node_id: nodeID, axiom_index: index })}>移除</button>
                              </div>
                            </>
                          )}
                        </div>
                      ))}
                      {adding && editAxiom()}
                    </div>
                  );
                })}
              </div>
            </section>
          </>
        )}
        {node.node_type === "Domain" && (
          <>
            <section className="drawer-section" aria-label="领域属性">
              <div className="drawer-section-heading">
                <h3>领域属性</h3>
                {onEnterDomain && (
                  <button type="button" className="drawer-enter" onClick={onEnterDomain}>
                    进入此领域<PanelIcon name="open" />
                  </button>
                )}
              </div>
              <div className="drawer-details">
                {description && <p>{description}</p>}
                <dl className="drawer-terms">{propertyRows()}</dl>
                {evidence && <blockquote className="drawer-evidence">{evidence}</blockquote>}
              </div>
            </section>
            <section className="drawer-section" aria-label="领域成员">
              <div className="drawer-section-heading"><h3>领域成员</h3><span>{members.length} 个节点</span></div>
              <div className="drawer-member-types">
                {MEMBER_TYPES.map(([type, label]) => (
                  <div className="drawer-member-kind" key={type}>
                    <span className={"drawer-node-dot drawer-node-dot--" + NODE_TONES[type]} aria-hidden="true" />
                    <span>{label}</span>
                    <strong>{members.filter(member => member.node_type === type).length}</strong>
                  </div>
                ))}
              </div>
              {members.length > 0 && (
                <ul className="drawer-domain-members">
                  {members.slice(0, 5).map(member => (
                    <li key={member.id}><NodeLink node={member} onInspect={onInspectNode} /></li>
                  ))}
                </ul>
              )}
              {members.length > 5 && (
                <details className="drawer-fields">
                  <summary>其余 {members.length - 5} 个节点</summary>
                  <ul className="drawer-domain-members">
                    {members.slice(5).map(member => (
                      <li key={member.id}><NodeLink node={member} onInspect={onInspectNode} /></li>
                    ))}
                  </ul>
                </details>
              )}
            </section>
          </>
        )}
        {artifact && (
          <section className="drawer-section" aria-label="资料属性">
            <div className="drawer-section-heading"><h3>资料属性</h3></div>
            <dl className="drawer-terms drawer-artifact-meta">
              <dt>读取方式</dt><dd>{READ_MODES[artifact.read_mode]}</dd>
              {artifact.artifact_type === "duckdb_table" && text(meta.table_name) && <><dt>SQL 表名</dt><dd><code>{text(meta.table_name)}</code></dd></>}
              {artifact.artifact_type === "duckdb_table" && text(meta.primary_key) && <><dt>主键</dt><dd><code>{text(meta.primary_key)}</code></dd></>}
              {artifact.artifact_type === "duckdb_table" && schemaColumns.length > 0 && <><dt>字段数量</dt><dd>{schemaColumns.length}</dd></>}
              {artifact.artifact_type === "tracking_api" && text(meta.version) && <><dt>埋点版本</dt><dd><code>{text(meta.version)}</code></dd></>}
              {artifact.artifact_type === "tracking_api" && typeof meta.event_count === "number" && <><dt>事件数量</dt><dd>{meta.event_count}</dd></>}
              {propertyRows()}
            </dl>
            <div className="drawer-source-actions">
              {source && (
                <a href={source} className="drawer-source-button" target="_blank" rel="noreferrer">
                  <PanelIcon name="open" />{artifact.artifact_type === "feishu_md" ? "打开飞书原文" : "打开来源链接"}
                </a>
              )}
              {publishedArtifact && artifact.artifact_type === "duckdb_table" &&
                <a href={api + "/download"} className="drawer-source-button" download><PanelIcon name="download" />下载配置表</a>}
              {publishedArtifact && artifact.artifact_type === "feishu_md" &&
                <button type="button" className="drawer-source-button" disabled={reading} onClick={() => void previewDoc()} aria-expanded={Boolean(markdown)}>
                  <PanelIcon name="file" />{reading ? "正在读取正文…" : markdown ? "收起入库正文" : "查看入库正文"}
                </button>}
            </div>
            {markdown && <pre className="drawer-document">{markdown}</pre>}
            {!publishedArtifact && artifact.artifact_type === "duckdb_table" && (
              <p className="drawer-secondary">发布后可下载配置表。</p>
            )}
          </section>
        )}
        {artifact?.artifact_type === "duckdb_table" && highColumns.length > 0 && (
          <section className="drawer-section">
            <details className="drawer-fields">
              <summary>{lowColumns.length > 0 ? "其余字段" : "表头字段"} <span>{highColumns.length} 项</span></summary>
              <ul>{highColumns.map(renderColumn)}</ul>
            </details>
          </section>
        )}
        {targetDomains.length > 0 && (
          <section className="drawer-section" aria-label="所属领域">
            <div className="drawer-section-heading">
              <h3>所属领域</h3>
              {editing?.kind !== "domain" && <button type="button" className="panel-text-button" disabled={disabled}
                onClick={() => start({ kind: "domain", target: "" })}>移动到其他领域</button>}
            </div>
            {editing?.kind === "domain" ? (
              <form className="drawer-edit" onSubmit={submitDetails}>
                <label>目标领域
                  <select required autoFocus value={editing.target} onChange={event => setEditing({ ...editing, target: event.currentTarget.value })}>
                    <option value="" disabled>选择领域</option>
                    {targetDomains.map(candidate => <option key={candidate.id} value={candidate.id}>{candidate.label}</option>)}
                  </select>
                </label>
                {editorActions()}
              </form>
            ) : <p className="drawer-secondary">{domain?.label ?? "全局域"}</p>}
          </section>
        )}
        <RelationshipSection node={node} graph={graph} members={members} onInspect={onInspectNode} />
      </div>
    </aside>
  );
}

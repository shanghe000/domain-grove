import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { explain, request, RequestError } from './api/client'
import { App, EngineMark } from './App'
import { PanelIcon } from './components/ChatSidebar'
import { ModelingPanel, type ModelingDraft } from './components/ModelingPanel'
import { PortalCanvas } from './components/PortalCanvas'
import { ProjectSpaces, projectIndexPath, type ProjectSpace } from './components/ProjectSpaces'
import {ProposalEditor,type Edit} from './components/ProposalEditor'
import {SourceIntake} from './components/SourceIntake'
import type {BudgetContext} from './engine/bfsBudget'
import { adaptIndexGraph,axiomNames,relationNames } from './engine/indexPortal'
import type { Graph as CanvasGraph } from './types'
import './index.css'

export type Reference = { repository: string; path: string; line_start?: number; line_end?: number; anchor?: string; url?:string; read_strategy?:string }
type NodeKind = 'domain' | 'concept' | 'metric' | 'artifact' | 'document' | 'skill' | 'tool'
export type IndexNode = { id: string; kind: NodeKind; label: string; parent_id?: string; description?: string; ref?: Reference; axioms?: {kind:string; text:string}[]; properties?: Record<string, unknown> }
export type IndexEdge = { id: string; from: string; to: string; relation: string; description?: string; anchor?:string; line_start?:number; line_end?:number; source_field?:string; target_field?:string; separator?:string }
export type Graph = { schema_version: number; revision: number; nodes: IndexNode[]; edges: IndexEdge[]; changes?: Record<string,unknown>[] }
export type Patch = { upsert_nodes?: IndexNode[]; remove_node_ids?: string[]; upsert_edges?: IndexEdge[]; remove_edge_ids?: string[]; upsert_changes?:Record<string,unknown>[]; remove_change_ids?:string[] }
type Status = 'pending' | 'approved' | 'rejected'
export type Proposal = { id: string; edit_version?:number; can_undo?:boolean; title: string; rationale?: string; base_revision: number; status: Status; patch: Patch; submitted_by: string; created_at: string; reviewed_by?: string; reviewed_at?: string; review_note?: string }
type Detail = { proposal: Proposal; preview?: Graph; preview_error?: string; graph_warnings?:{node_id:string;message:string}[] }
type Identity = { user_id: string; username: string }
type Decision = 'approve' | 'reject'

const defaultProject: ProjectSpace = { id: 'default', name: '默认项目', role: 'reviewer' }
const kinds: Record<NodeKind, string> = { domain: '领域', concept: '概念', metric: '指标', document: '文档', skill: '技能', tool: '工具', artifact:'资料载体' }
const statuses: Record<Status, string> = { pending: '待审核', approved: '已发布', rejected: '已驳回' }
const stamp = (value?: string) => value ? new Date(value).toLocaleString('zh-CN', { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' }) : ''
const changes = (patch: Patch) => (patch.upsert_nodes?.length ?? 0) + (patch.remove_node_ids?.length ?? 0) +
  (patch.upsert_edges?.length ?? 0) + (patch.remove_edge_ids?.length ?? 0) + (patch.upsert_changes?.length ?? 0) + (patch.remove_change_ids?.length ?? 0)

const emptyCanvas: CanvasGraph = { nodes: [], edges: [], artifacts: [] }

export function IndexApp() {
  const [session, setSession] = useState<Identity>()
  const [legacy, setLegacy] = useState(false)
  const [loginURL, setLoginURL] = useState('')
  const [error, setError] = useState('')
  const [retry, setRetry] = useState(0)
  const reauthenticate = useCallback((url: string) => {
    setSession(undefined)
    setLoginURL(url)
  }, [])

  useEffect(() => {
    let active = true
    request<{ user: Identity }>(projectIndexPath(defaultProject.id) + '/session').then(value => {
      if (active) setSession(value.user)
    }).catch(problem => {
      if (!active) return
      if (problem instanceof RequestError && problem.status === 404) setLegacy(true)
      else if (problem instanceof RequestError && problem.status === 401 && problem.loginURL) setLoginURL(problem.loginURL)
      else setError(explain(problem))
    })
    return () => { active = false }
  }, [retry])

  if (legacy) return <App />
  if (session) return <ProjectWorkspace session={session} onLogin={reauthenticate} />
  return <div className="idx login-screen">
    <section aria-live="polite">
      <EngineMark />
      <h1>游戏 BI 本体知识引擎</h1>
      {loginURL ? <>
        <p>登录后可浏览本体图谱、核对 AI 提案及其知识来源。</p>
        <a className="primary-button" href={loginURL}>登录</a>
      </> : error ? <>
        <p role="alert">{error}</p>
        <button className="idx-btn" onClick={() => { setError(''); setRetry(retry + 1) }}>重试</button>
      </> : <p role="status">正在连接工作台…</p>}
    </section>
  </div>
}

function ProjectWorkspace({ session, onLogin }: {session: Identity; onLogin: (url: string) => void}) {
  const [spaces, setSpaces] = useState<ProjectSpace[]>()
  const [selected, setSelected] = useState(() => new URLSearchParams(location.search).get('project') || defaultProject.id)
  const [error, setError] = useState('')
  const [reload, setReload] = useState(0)

  const select = useCallback((id: string) => {
    setSelected(id)
    const url = new URL(location.href)
    if (id === defaultProject.id) url.searchParams.delete('project')
    else url.searchParams.set('project', id)
    history.replaceState(null, '', url)
  }, [])

  useEffect(() => {
    let active = true
    request<{spaces: ProjectSpace[]}>('/api/v1/spaces').then(result => {
      if (active) setSpaces(result.spaces)
    }).catch(problem => {
      if (!active) return
      if (problem instanceof RequestError && problem.status === 401 && problem.loginURL) onLogin(problem.loginURL)
      else if (problem instanceof RequestError && problem.status === 403) setSpaces([defaultProject])
      else setError(explain(problem))
    })
    return () => { active = false }
  }, [onLogin, reload])

  const revoked = useCallback(() => {
    setSpaces(undefined)
    setReload(value => value + 1)
  }, [])

  const current = spaces?.find(space => space.id === selected)
  if (!spaces || !current) return <div className="idx login-screen">
    <section aria-live="polite">
      <EngineMark />
      <h1>游戏 BI 本体知识引擎</h1>
      {error ? <><p role="alert">{error}</p><button className="idx-btn" onClick={() => { setError(''); setReload(value => value + 1) }}>重试</button></> :
        spaces ? <><p role="alert">当前项目空间不可用。请选择有权限的项目。</p>
          <label className="idx-project-select"><span>游戏项目空间</span><select value="" onChange={event => select(event.target.value)}>
            <option value="" disabled>选择空间</option>{spaces.map(space => <option key={space.id} value={space.id}>{space.name}</option>)}
          </select></label></> : <p role="status">正在读取项目空间…</p>}
    </section>
  </div>

  return <Workspace key={current.id} session={session} onLogin={onLogin} space={current}
    spaces={spaces} onSelectSpace={select} onSpaceRevoked={revoked} onCreatedSpace={space => {
      setSpaces(existing => [...(existing ?? []).filter(item => item.id !== space.id), space])
      select(space.id)
    }} />
}

type WorkspaceProps = {
  session: Identity
  onLogin: (url: string) => void
  space: ProjectSpace
  spaces: ProjectSpace[]
  onSelectSpace: (id: string) => void
  onCreatedSpace: (space: ProjectSpace) => void
  onSpaceRevoked: () => void
}

function Workspace({ session, onLogin, space, spaces, onSelectSpace, onCreatedSpace, onSpaceRevoked }: WorkspaceProps) {
  const indexPath = projectIndexPath(space.id)
  const [graph, setGraph] = useState<Graph>()
  const [proposals, setProposals] = useState<Proposal[]>([])
  const [reviewAvailable, setReviewAvailable] = useState(true)
  const [pane, setPane] = useState<'proposals' | 'modeling'>('modeling')
  const [draft, setDraft] = useState<ModelingDraft>()
  const [filter, setFilter] = useState<Status | 'all'>('pending')
  const [selected, setSelected] = useState('')
  const [detail, setDetail] = useState<Detail>()
  const [isPreview, setIsPreview] = useState(true)
  const [nodeID, setNodeID] = useState('')
  const [domain, setDomain] = useState('')
  const [mobileGovernance, setMobileGovernance] = useState(false)
  const [hideDraftDetails, setHideDraftDetails] = useState(false)
  const [hideReviewDetails, setHideReviewDetails] = useState(false)
  const [decision, setDecision] = useState<Decision>()
  const [note, setNote] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [refresh, setRefresh] = useState(0)
  const [fetchError, setFetchError] = useState('')
  const [detailError, setDetailError] = useState('')
  const [reviewError, setReviewError] = useState('')
  const [notice, setNotice] = useState('')
  const [loadDetail, setLoadDetail] = useState(false)
  const [editAI,setEditAI]=useState('')
  useEffect(()=>{
    if(!notice) return
    const timer=window.setTimeout(()=>setNotice(''),5000)
    return ()=>window.clearTimeout(timer)
  },[notice])
  const seenPending=useRef<Set<string>>()
  const navigateProposal=useRef('')
  const mountedProposal=useRef(''); mountedProposal.current=selected

  useEffect(() => {
    let active = true
    async function read() {
      try {
        const [published, queue] = await Promise.all([
          request<Graph>(indexPath),
          request<{proposals: Proposal[]}>(indexPath + '/proposals').catch(error => {
            if (error instanceof RequestError && error.status === 403) return null
            throw error
          }),
        ])
        if (!active) return
        setGraph(current => current?.revision === published.revision ? current : published)
        setReviewAvailable(queue !== null)
        const nextPending=new Set(queue?.proposals.filter(p=>p.status==='pending').map(p=>p.id) ?? [])
        if(seenPending.current && [...nextPending].some(id=>!seenPending.current!.has(id))) setNotice('收到新的待审核提案。')
        seenPending.current=nextPending
        setProposals(queue?.proposals ?? [])
        setFetchError('')
      } catch (error) {
        if (!active) return
        if (error instanceof RequestError && error.status === 401 && error.loginURL) onLogin(error.loginURL)
        else if (error instanceof RequestError && error.status === 404 && space.id !== defaultProject.id) onSpaceRevoked()
        setFetchError(explain(error))
      }
    }
    void read()
    const timer = window.setInterval(() => { if (!document.hidden) void read() }, 7000)
    return () => { active = false; window.clearInterval(timer) }
  }, [indexPath, onLogin, onSpaceRevoked, refresh, space.id])

  useEffect(() => {
    setDetail(undefined)
    setDetailError('')
    setLoadDetail(false)
    if (!selected) return
    let active = true
    setLoadDetail(true)
    request<Detail>(indexPath + '/proposals/' + encodeURIComponent(selected)).then(value => {
      if (active) setDetail(value)
    }).catch(error => {
      if (active) setDetailError(explain(error))
    }).finally(() => { if (active) setLoadDetail(false) })
    return () => { active = false }
  }, [indexPath, selected, refresh, graph?.revision, proposals.find(p=>p.id===selected)?.edit_version, proposals.find(p=>p.id===selected)?.status])

  const pending = proposals.filter(item => item.status === 'pending').length
  const visible = proposals.filter(item => filter === 'all' || item.status === filter)
  const proposal = detail?.proposal
  const stale = Boolean(proposal?.status === 'pending' && graph && proposal.base_revision !== graph.revision)
  const drafted = pane === 'modeling' ? draft : undefined
  const aiPreview = drafted && graph?.revision === drafted.base_revision ? drafted.preview : undefined
  const preview = pane === 'modeling' ? aiPreview : (!stale && proposal?.status === 'pending' ? detail?.preview : undefined)
  const previewPatch = pane === 'modeling' ? drafted?.patch : proposal?.patch
  const showingPreview = isPreview && Boolean(preview)
  const shown = showingPreview ? preview : graph
  const nodes = useMemo(() => new Map(shown?.nodes.map(item => [item.id, item])), [shown])
  const inspected = nodes.get(nodeID)
  const canvasGraph = useMemo(() => shown ? adaptIndexGraph(shown) : emptyCanvas, [shown])
  const draftEdgeIDs=useMemo(()=>new Set((showingPreview ? previewPatch?.upsert_edges : previewPatch?.remove_edge_ids?.map(id=>({id})))?.map(e=>e.id) ?? []),[showingPreview,previewPatch])
  const loadContext=useCallback(async(id:string):Promise<BudgetContext>=>{
    const query='/traverse?node_id='+encodeURIComponent(id)+'&budget=2.5&revision='+shown?.revision
    const url=showingPreview ? drafted ? indexPath+'/preview'+query : indexPath+'/proposals/'+encodeURIComponent(selected)+query : indexPath+query
    const result=await request<{nodes:IndexNode[];edges:IndexEdge[];boundary:{node_id:string;edge_id:string}[];revision:number;budget:number}>(url,
      showingPreview && drafted ? {base_revision:drafted.base_revision,patch:drafted.patch}:undefined)
    if(result.revision!==shown?.revision) throw new Error('图谱版本已更新。')
    return {seed_node_id:id,budget:result.budget,subgraph:adaptIndexGraph({schema_version:2,revision:result.revision,nodes:result.nodes,edges:result.edges}),artifacts:[],
      frontier_nodes:(result.boundary ?? []).map(b=>({node_id:b.node_id,label:nodes.get(b.node_id)?.label ?? b.node_id,via_edge:b.edge_id,from_node:id}))}
  },[showingPreview,drafted,indexPath,selected,shown,nodes])
  useEffect(()=>{
    if(!detail?.preview || navigateProposal.current!==detail.proposal.id) return
    navigateProposal.current=''
    const patch=detail.proposal.patch
    const focus=patch.upsert_nodes?.find(n=>n.kind!=='domain')?.id ?? patch.upsert_edges?.[0]?.from ?? patch.upsert_nodes?.[0]?.id
    if((graph?.nodes.length ?? 0)===0 && (patch.upsert_nodes?.filter(n=>n.kind==='domain' && !n.parent_id).length ?? 0)>1) setDomain('')
    else if(focus) setDomain(detail.preview.nodes.find(n=>n.id===focus)?.parent_id ?? '')
    else if(patch.remove_node_ids?.length || patch.remove_edge_ids?.length) {
      const seed=patch.remove_node_ids?.[0] ?? graph?.edges.find(e=>e.id===patch.remove_edge_ids?.[0])?.from
      setIsPreview(false);setDomain(graph?.nodes.find(n=>n.id===seed)?.parent_id ?? '')
    }
  },[detail,graph])
  const draftNodeIDs = useMemo(() => {
    if (!showingPreview) return new Set(previewPatch?.remove_node_ids ?? [])
    return new Set(previewPatch?.upsert_nodes?.map(node => node.id))
  }, [graph, previewPatch, showingPreview])
  const activeDomain = nodes.has(domain) ? domain : ''
  const trail: IndexNode[] = []
  for (let item = nodes.get(activeDomain); item; item = item.parent_id ? nodes.get(item.parent_id) : undefined) {
    trail.unshift(item)
  }
  const draftInspector = drafted && !hideDraftDetails
  const hasDrawer = Boolean((selected && !hideReviewDetails) || inspected || draftInspector)

  useEffect(() => {
    function closeInspect(event: KeyboardEvent) {
      if (event.key !== 'Escape' || document.querySelector('dialog[open]') || event.defaultPrevented) return
      if (nodeID) setNodeID('')
      else if (mobileGovernance) setMobileGovernance(false)
    }
    window.addEventListener('keydown', closeInspect)
    return () => window.removeEventListener('keydown', closeInspect)
  }, [mobileGovernance, nodeID])

  const selectProposal = (id: string) => {
    navigateProposal.current=id
    setEditAI('')
    setSelected(id)
    setHideReviewDetails(false)
    setIsPreview(true)
    setNodeID('')
    setDecision(undefined)
    setNote('')
    setReviewError('')
    if (id) {
      setDomain('')
      setMobileGovernance(false)
    }
  }

  function selectNode(id: string) {
    setNodeID(id)
    setMobileGovernance(false)
  }

  function changeDomain(id: string) {
    if (id === activeDomain) return
    setDomain(id)
    setNodeID('')
    if (drafted) setHideDraftDetails(true)
  }

  function inspectProposalNode(id: string, candidate: Graph) {
    setIsPreview(true)
    setDomain(candidate.nodes.find(node => node.id === id)?.parent_id ?? '')
    selectNode(id)
  }

  function switchGraph(usePreview: boolean) {
    setIsPreview(usePreview)
    setNodeID('')
    setDomain('')
  }

  function showPane(next: 'proposals' | 'modeling') {
    if (next === pane) return
    setPane(next)
    selectProposal('')
  }

  async function editProposal(edit:Edit):Promise<boolean> {
    if(!proposal || submitting || stale || proposal.status!=='pending') return false
    const id=proposal.id
    setSubmitting(true);setReviewError('')
    try {
      const result=await request<Detail>(indexPath+'/proposals/'+encodeURIComponent(id)+'/edit',{...edit,version:proposal.edit_version ?? 0})
      if(mountedProposal.current===id) {
        setDetail(result);setDecision(undefined);setIsPreview(true);setEditAI('')
        setProposals(items=>items.map(item=>item.id===id ? result.proposal : item))
        setNotice('undo' in edit ? '已撤销上一步调整。' : '草案已更新。')
      }
      return true
    } catch(e) {setReviewError(explain(e));setNotice(explain(e));if(e instanceof RequestError && e.status===409)setRefresh(v=>v+1);return false}
    finally {setSubmitting(false)}
  }
  async function applyReview() {
    if (!proposal || !decision || (decision==='reject' && !note.trim())) return
    setSubmitting(true)
    setReviewError('')
    try {
      const result = await request<Proposal>(indexPath + '/proposals/' + encodeURIComponent(proposal.id) + '/review', { decision, note,version:proposal.edit_version ?? 0 })
      setNotice(result.status === 'approved' ? '已批准，图谱已发布。' : '已驳回提案。')
      setFilter('all')
      setDecision(undefined)
      setNote('')
      setRefresh(value => value + 1)
    } catch (error) {
      setReviewError(explain(error))
      if (error instanceof RequestError && error.status === 409) setRefresh(value => value + 1)
    } finally { setSubmitting(false) }
  }

  const eligible = proposal?.status === 'pending' && !submitting
  return <div className="idx engine-workbench index-workbench">
    <header className="engine-header">
      <div className="engine-brand"><EngineMark /><h1>游戏 BI <span>本体知识引擎</span></h1></div>
      <ProjectSpaces spaces={spaces} current={space} canCreate={Boolean(graph) && reviewAvailable} ownSubject={session.user_id}
        onSelect={onSelectSpace} onCreated={onCreatedSpace} />
      <div className="engine-toolbar">
        <button type="button" className="mobile-chat-toggle secondary-button" onClick={() => setMobileGovernance(value => !value)}
          aria-expanded={mobileGovernance} aria-controls="index-governance-pane">{mobileGovernance ? '收起治理' : '治理 AI'}</button>
        <span className={session.user_id === 'dev_user' ? 'engine-mode engine-mode--mock' : 'engine-mode'}><i />{session.user_id === 'dev_user' ? '演示模式' : '已连接'}</span>
        <span className="engine-user" title={'账号 ID ' + session.user_id}>{session.user_id === 'dev_user' ? '本地演示' : session.username}</span>
        {session.user_id !== 'dev_user' && <button className="quiet-button" onClick={async () => {
          try { await request('/auth/logout', {}); window.location.reload() }
          catch (error) { setNotice(explain(error)) }
        }}>退出</button>}
      </div>
    </header>
    <div className="engine-content">
      <aside id="index-governance-pane" className={'governance-pane' + (mobileGovernance ? ' governance-pane--mobile' : '')} aria-label="本体治理">
        <section className="governance-panel index-governance">
        <header className="governance-header">
          <span className="governance-mark"><PanelIcon name="signal" /></span><h2>治理对话</h2>
        </header>
        <div className="idx-tools-navigation" role="group" aria-label="治理入口">
          <button aria-pressed={pane === 'modeling'} onClick={() => showPane('modeling')}>AI 建模</button>
          <button aria-pressed={pane === 'proposals'} onClick={() => showPane('proposals')}>建模提案 {pending > 0 && <span>{pending}</span>}</button>
        </div>
        {pane==='modeling' && graph && reviewAvailable && <SourceIntake endpoint={indexPath+'/ingest'} onSubmitted={p=>{setPane('proposals');selectProposal(p.id);setRefresh(v=>v+1)}} />}
        <ModelingPanel active={pane === 'modeling'} endpoint={indexPath + '/modeling'} revision={graph?.revision} draft={draft} onLogin={onLogin}
          readOnly={Boolean(graph) && !reviewAvailable}
          onDraft={value => { setDraft(value); if (value) { switchGraph(true); setHideDraftDetails(false); setMobileGovernance(false) } }}
          onSubmitted={result => {
            setDraft(undefined)
            setPane('proposals')
            setFilter('pending')
            selectProposal(result.id)
            setRefresh(value => value + 1)
            setNotice('建模提案已提交，等待审核。')
          }} />
        <div className="index-governance-queue" hidden={pane === 'modeling'}>
        <div className="idx-queue-title">
          <h2>待办与记录</h2>
          <button className="idx-btn idx-quiet" onClick={() => setRefresh(value => value + 1)}>刷新</button>
        </div>
        <div className="idx-tabs idx-queue-filter" role="group" aria-label="筛选提案">
          <button aria-pressed={filter === 'pending'} onClick={() => setFilter('pending')}>待审核 <span>{pending}</span></button>
          <button aria-pressed={filter === 'all'} onClick={() => setFilter('all')}>全部</button>
        </div>
        {proposal?.status==='pending' && !stale && <form className="idx-adjust-composer" onSubmit={e=>{e.preventDefault();void editProposal({instruction:editAI.trim()})}}>
          <label htmlFor="adjust-instruction">用 AI 调整此提案</label><textarea id="adjust-instruction" value={editAI} rows={2} onChange={e=>setEditAI(e.target.value)} placeholder="描述要移动的节点、连线或规则" maxLength={2600}/>
          <button className="idx-btn" disabled={submitting || !editAI.trim()}>调整草案</button></form>}
        <div className="idx-queue-items">
          {!reviewAvailable ? <p className="idx-muted">登录已授权账号后可审核提案。</p> :
            graph && visible.length === 0 ? <p className="idx-muted">{filter === 'pending' ? '暂无待审核提案。' : '暂无建模提案。'}</p> :
              visible.map(item => <button key={item.id} className="idx-queue-item" aria-pressed={selected === item.id}
                disabled={submitting} onClick={() => selectProposal(item.id)}>
                <strong>{item.title}</strong>
                <span>{statuses[item.status]} <span aria-hidden="true">·</span> {changes(item.patch)} 项变更</span>
                <small>{item.submitted_by} · {stamp(item.created_at)}</small>
              </button>)}
        </div>
        </div>
        </section>
      </aside>

      <main className={'canvas-workspace' + (hasDrawer ? ' index-canvas--inspected' : '')} aria-label="业务本体图谱">
        <PortalCanvas graph={canvasGraph} hierarchical domainId={activeDomain} onDomainChange={changeDomain}
          selectedId={nodeID} onSelect={selectNode} draftNodeIds={draftNodeIDs} draftEdgeIds={draftEdgeIDs} layoutKey={space.id+'/'+session.user_id} loadContext={loadContext}
          onMoveNode={showingPreview && proposal?.status==='pending' ? (id,parent_id)=>{const node=nodes.get(id);if(node)void editProposal({patch:{upsert_nodes:[{...node,parent_id}]}})}:undefined}
          emptyText={showingPreview ? '提案将移除全部节点，请核对变更清单。' : graph ? '当前项目还没有已发布的本体节点' : '正在读取图谱…'} />

        <div className="canvas-toolbar index-canvas-toolbar">
          <nav className="graph-breadcrumb" aria-label="图谱层级">
            <button type="button" className="quiet-button" onClick={() => changeDomain('')} aria-current={!activeDomain ? 'page' : undefined}>全局域</button>
            {trail.map((node, i) => <span key={node.id} className="index-breadcrumb-item">
              <span aria-hidden="true">/</span>
              {i === trail.length - 1 ? <span aria-current="page" title={node.label}>{node.label}</span>
                : <button type="button" className="quiet-button" onClick={() => changeDomain(node.id)}>{node.label}</button>}
            </span>)}
          </nav>
          <div className="index-canvas-controls">
            {graph && <div className="graph-totals" aria-label="已发布图谱版本">
              <span>{shown?.nodes.filter(node => node.kind === 'domain').length ?? 0} 个领域</span>
              <span>{shown?.nodes.length ?? 0} 个节点</span><span>{shown?.edges.length ?? 0} 条关系</span>
              <span className="idx-revision">r{graph.revision}</span>
            </div>}
            {graph && (!showingPreview || proposal?.status==='pending') && <a className="secondary-button"
              href={showingPreview && proposal ? indexPath+'/proposals/'+encodeURIComponent(proposal.id)+'/preview.yaml' : indexPath+'.yaml'}>
              {showingPreview?'导出草案 YAML':'导出 YAML'}</a>}
          </div>
        </div>
        {preview ? <section className="draft-bar index-draft-bar" aria-label="草稿操作">
          <div className="draft-meta"><i /><span>{drafted ? 'AI 建模草案' : '待审核提案'}</span><small>{changes(previewPatch ?? {})} 项变更</small></div>
          <div className="idx-tabs" role="group" aria-label="图谱版本">
            <button aria-pressed={!isPreview} onClick={() => switchGraph(false)}>已发布</button>
            <button aria-pressed={isPreview} onClick={() => switchGraph(true)}>提案预览</button>
          </div>
          {proposal?.status==='pending' && proposal.can_undo && <button className="secondary-button" disabled={submitting} onClick={()=>void editProposal({undo:true})}>撤销调整</button>}
          {selected && hideReviewDetails && <button className="secondary-button" onClick={() => { setNodeID(''); setHideReviewDetails(false) }}>查看审核详情</button>}
          {drafted && hideDraftDetails && <button className="secondary-button" onClick={() => { setNodeID(''); setHideDraftDetails(false) }}>查看草稿变更</button>}
        </section> : <p className="portal-hint">
          <span>单击查看属性 · 双击钻取 · 右键拖动观察切面</span>
          <span>点按查看属性 · 从详情进入下层</span>
        </p>}
        <div className="graph-legend" aria-label="关系图例">
          <span><i className="legend-depends" />口径依赖</span>
          <span><i className="legend-impact" />受影响于</span>
          <span><i className="legend-definition" />定义或配置</span>
          <span><i className="legend-foreign" />外键关联</span><span><i className="legend-collaborates" />业务协作</span>
        </div>
        {fetchError && <p className="engine-toast engine-toast--error" role="alert">{fetchError}</p>}
      </main>

      {hasDrawer && <aside className="node-drawer idx-inspector" aria-label={inspected ? '节点详情' : drafted ? '草稿变更' : '审核详情'}>
        {inspected ? <header className="drawer-header">
          <div className="drawer-heading-type">
            <span className={'drawer-node-dot drawer-node-dot--' + inspected.kind} aria-hidden="true" />
            {kinds[inspected.kind]}
            {showingPreview && previewPatch?.upsert_nodes?.some(node => node.id === inspected.id)
              ? <span className="panel-status panel-status--draft">草稿</span>
              : <span className="panel-status panel-status--muted">已发布</span>}
          </div>
          <h2>{inspected.label}</h2>
          <button className="drawer-close panel-icon-button" type="button" aria-label={draftInspector ? '返回草稿' : selected && !hideReviewDetails ? '返回审核' : '关闭节点详情'}
            onClick={() => setNodeID('')}><PanelIcon name="close" /></button>
        </header> : <header className="idx-inspector-head"><h2>{drafted ? '草稿变更' : '审核详情'}</h2>
          <button className="panel-text-button" disabled={submitting} onClick={() => {
            if (drafted) setHideDraftDetails(true)
            else if (preview) setHideReviewDetails(true)
            else selectProposal('')
          }}><PanelIcon name="close" />{!drafted && preview ? '收起' : '关闭'}</button>
        </header>}
        {inspected && shown ? <div className="idx-inspector-scroll"><NodeDetails node={inspected} graph={shown} onSelect={selectNode}
          onEnter={inspected.id !== activeDomain && (shown.nodes.some(node => node.parent_id === inspected.id) || (['concept','metric'].includes(inspected.kind) && shown.edges.some(e=>e.from===inspected.id && nodes.get(e.to)?.kind==='artifact'))) ? () => changeDomain(inspected.id) : undefined} />
          {showingPreview && proposal?.status==='pending' && <ProposalEditor key={inspected.id+'/'+(proposal.edit_version ?? 0)} graph={shown} node={inspected} busy={submitting} onEdit={editProposal}/>}</div> :
          <div className="idx-inspector-scroll">
          {proposal?.status==='pending' && !stale && detail?.preview && <ProposalEditor graph={detail.preview} busy={submitting} onEdit={editProposal}/>}
          {drafted ? <>
            <span className="idx-status idx-status--pending">待提交</span>
            <h3>{drafted.title}</h3>
            <p className="idx-meta">基于 r{drafted.base_revision}</p>
            {drafted.rationale && <p className="idx-description">{drafted.rationale}</p>}
            <ProposalChanges patch={drafted.patch} base={graph} preview={aiPreview} compareBase={Boolean(aiPreview)}
              onNode={id => { if (aiPreview) inspectProposalNode(id, aiPreview) }} />
          </> : loadDetail && !proposal ? <p role="status">正在读取提案…</p> : detailError ? <p className="idx-error" role="alert">{detailError}</p> : proposal && <>
            <span className={'idx-status idx-status--' + proposal.status}>{statuses[proposal.status]}</span>
            <h3>{proposal.title}</h3>
            <p className="idx-meta">{proposal.submitted_by} · {stamp(proposal.created_at)} · 基于 r{proposal.base_revision}</p>
            {proposal.rationale && <p className="idx-description">{proposal.rationale}</p>}
            {(stale || detail?.preview_error) && proposal.status === 'pending' && <p className="idx-warning">
              {stale ? '发布版本已变化。请让提交方基于最新版重新提交，或驳回此提案。' : detail?.preview_error}
            </p>}
            {detail?.graph_warnings?.map(w=><p className="idx-warning" key={w.node_id+w.message}>{nodes.get(w.node_id)?.label ?? w.node_id} · {w.message}</p>)}
            <ProposalChanges patch={proposal.patch} base={graph} preview={proposal.status === 'pending' && !stale ? detail?.preview : undefined}
              compareBase={proposal.status === 'pending' && !stale} onNode={id => { if (detail?.preview) inspectProposalNode(id, detail.preview) }} />
            {proposal.reviewed_by && <section className="idx-inspector-section"><h4>审核记录</h4>
              <p>{proposal.reviewed_by} · {stamp(proposal.reviewed_at)}</p>{proposal.review_note && <p className="idx-description">{proposal.review_note}</p>}
            </section>}
            {proposal.status === 'pending' && <section className="idx-inspector-section idx-review">
              {decision ? <>
                <label htmlFor="idx-review-note">{decision === 'approve' ? '发布说明（可选）' : '驳回理由'}</label>
                <textarea id="idx-review-note" required={decision==='reject'} maxLength={1000} value={note} onChange={event => setNote(event.target.value)} rows={3}
                  placeholder={decision === 'approve' ? '记录本次审核的依据' : '说明提交方需要调整的内容'} />
                <div className="idx-review-actions">
                  <button className="idx-btn" disabled={submitting} onClick={() => setDecision(undefined)}>取消</button>
                  <button className={'idx-btn ' + (decision === 'approve' ? 'idx-primary' : 'idx-danger')} onClick={() => void applyReview()}
                    disabled={!eligible || (decision === 'approve' ? stale || !detail?.preview : !note.trim())}>
                    {submitting ? '正在提交…' : decision === 'approve' ? '确认发布' : '确认驳回'}
                  </button>
                </div>
              </> : <div className="idx-review-actions">
                <button className="idx-btn" disabled={!eligible} onClick={() => setDecision('reject')}>驳回</button>
                <button className="idx-btn idx-primary" disabled={!eligible || stale || !detail?.preview} onClick={() => setDecision('approve')}>批准并发布</button>
              </div>}
              {reviewError && <p className="idx-error" role="alert">{reviewError}</p>}
            </section>}
          </>}
          </div>}
      </aside>}
    </div>
    {notice && <div className="idx-toast" role="status">{notice}<button className="idx-btn idx-quiet" onClick={() => setNotice('')}>关闭</button></div>}
  </div>
}

function ProposalChanges({ patch, base, preview, compareBase, onNode }: {patch: Patch; base?: Graph; preview?: Graph; compareBase: boolean; onNode: (id: string) => void}) {
  const names = new Map([...(base?.nodes ?? []), ...(preview?.nodes ?? []), ...(patch.upsert_nodes ?? [])].map(node => [node.id, node.label]))
  const baseNodes = new Map(base?.nodes.map(node => [node.id, node]))
  const baseEdges = new Map(base?.edges.map(edge => [edge.id, edge]))
  const edgeLabel = (edge: IndexEdge) => <>{names.get(edge.from) ?? edge.from} <span className="idx-arrow" aria-label="指向">→</span> {names.get(edge.to) ?? edge.to}</>
  return <section className="idx-inspector-section">
    <h4>变更清单 <span>{changes(patch)}</span></h4>
    {!!patch.upsert_changes?.length && <details className="idx-change-group"><summary>时态变更 · {patch.upsert_changes.length} 条</summary><ul className="idx-change-list">{patch.upsert_changes.map((c,i)=><li key={i}><span>{String(c.label ?? c.title ?? c.id)}</span><small>{String(c.starts_at ?? '')} {c.ends_at ? '至 '+String(c.ends_at):''}</small></li>)}</ul></details>}
    <details className="idx-change-group" open={changes(patch)<=15}><summary>节点与连线 · {changes(patch)-(patch.upsert_changes?.length ?? 0)-(patch.remove_change_ids?.length ?? 0)} 项</summary><ul className="idx-change-list">
      {(patch.remove_change_ids ?? []).map(id=><li key={'rm-time'+id}><span className="idx-change-kind idx-change-kind--remove">移除时态记录</span>{id}</li>)}
      {(patch.upsert_nodes ?? []).map(node => <li key={'node-' + node.id}>
        <span className="idx-change-kind">{compareBase ? baseNodes.has(node.id) ? '更新' : '新增' : '写入'}{kinds[node.kind]}</span>
        <span>{preview ? <button className="idx-text-btn" onClick={() => onNode(node.id)}>{node.label}</button> : node.label}</span>
        {node.parent_id && <small>上级 {names.get(node.parent_id) ?? node.parent_id}</small>}
        {node.ref && <code title={node.ref.path}>{formatReference(node.ref)}</code>}
      </li>)}
      {(patch.remove_node_ids ?? []).map(id => <li key={'rm-node-' + id}>
        <span className="idx-change-kind idx-change-kind--remove">移除节点</span><span>{names.get(id) ?? id}</span>
      </li>)}
      {(patch.upsert_edges ?? []).map(edge => {
        const previous = compareBase ? baseEdges.get(edge.id) : undefined
        const directionChanged = previous && (previous.from !== edge.from || previous.to !== edge.to || previous.relation !== edge.relation)
        return <li key={'edge-' + edge.id}>
          <span className="idx-change-kind">{compareBase ? previous ? '更新关系' : '新增关系' : '写入关系'}</span><span>{edgeLabel(edge)}</span>
          <small>{relationNames[edge.relation] ?? edge.relation}{edge.description && ' · ' + edge.description}</small>
          {directionChanged && <small>原关系 {edgeLabel(previous)} · {previous.relation}</small>}
        </li>
      })}
      {(patch.remove_edge_ids ?? []).map(id => <li key={'rm-edge-' + id}>
        <span className="idx-change-kind idx-change-kind--remove">移除关系</span>
        <span>{baseEdges.has(id) ? edgeLabel(baseEdges.get(id)!) : id}</span>
      </li>)}
    </ul></details>
  </section>
}

function formatReference(ref: Reference) {
  const lines = ref.line_start ? ':' + ref.line_start + (ref.line_end ? '–' + ref.line_end : '') : ''
  return ref.repository + '/' + ref.path + lines + (ref.anchor ? ' #' + ref.anchor : '')
}

function NodeDetails({ node, graph, onSelect, onEnter }: {node: IndexNode; graph: Graph; onSelect: (id: string) => void; onEnter?: () => void}) {
  const byID = new Map(graph.nodes.map(item => [item.id, item]))
  const connected = graph.edges.filter(edge => edge.from === node.id || edge.to === node.id)
  const children = graph.nodes.filter(item => item.parent_id === node.id)
  const events=(graph.changes ?? []).filter(change=>Array.isArray(change.node_ids) && change.node_ids.includes(node.id))
  return <div className="drawer-content">
    <section className="drawer-section drawer-details">
      <div className="drawer-section-heading"><h3>{node.kind === 'domain' ? '领域属性' : '节点属性'}</h3>
        {onEnter && <button className="secondary-button idx-enter-node" onClick={onEnter}>
          {node.kind === 'domain' ? '进入此领域' : '进入下层'} <PanelIcon name="open" />
        </button>}
      </div>
      {node.description && <p className="idx-description">{node.description}</p>}
      <dl className="drawer-terms"><dt>状态</dt><dd>{node.properties?.lifecycle==='deprecated'?'已废弃':'生效中'}</dd>
        <dt>连线健康度</dt><dd>{connected.length} 条{connected.length>10 && node.properties?.hub_role!=='sink'?' · 建议检查概念粒度':''}</dd>
        {typeof node.properties?.owner==='string' && <><dt>负责人</dt><dd>{node.properties.owner}</dd></>}
        {node.parent_id && <><dt>上级</dt><dd><button className="idx-text-btn" onClick={() => onSelect(node.parent_id!)}>{byID.get(node.parent_id)?.label}</button></dd></>}
      </dl>
    </section>
    {node.axioms?.length ? <section className="drawer-section"><h3>业务公理</h3><ul className="idx-axioms">{node.axioms.map((a,i)=><li key={i}><strong>{axiomNames[a.kind] ?? a.kind}</strong><p>{a.text}</p></li>)}</ul></section> : null}
    {node.ref && <section className="drawer-section"><div className="drawer-section-heading"><h3>知识定位</h3></div>
      {node.ref.url && /^https?:\/\//i.test(node.ref.url) && <a className="idx-text-btn" target="_blank" rel="noopener noreferrer" href={node.ref.url}>打开原始资料</a>}
      <dl className="idx-fields">{node.ref.read_strategy && <div><dt>读取方式</dt><dd>{node.ref.read_strategy}</dd></div>}<div><dt>仓库</dt><dd>{node.ref.repository}</dd></div>
        <div><dt>路径</dt><dd><code>{node.ref.path}</code></dd></div>
        {Boolean(node.ref.line_start) && <div><dt>行号</dt><dd>{node.ref.line_start}{node.ref.line_end ? '–' + node.ref.line_end : ''}</dd></div>}
        {node.ref.anchor && <div><dt>锚点</dt><dd>{node.ref.anchor}</dd></div>}
      </dl></section>}
    {children.length > 0 && <section className="drawer-section"><div className="drawer-section-heading"><h3>{node.kind === 'domain' ? '领域成员' : '下级节点'}</h3><span>{children.length} 个节点</span></div>
      <ul className="idx-relations">{children.map(child => <li key={child.id}>
        <button className="idx-text-btn" onClick={() => onSelect(child.id)}>{child.label}</button><small>{kinds[child.kind]}</small>
      </li>)}</ul>
    </section>}
    {connected.length > 0 && <section className="drawer-section"><div className="drawer-section-heading"><h3>有向关系</h3></div>
      <ul className="idx-relations">{connected.map(edge => {
        const outgoing = edge.from === node.id
        const other = outgoing ? edge.to : edge.from
        return <li key={edge.id}>
          <span>{outgoing ? '出边' : '入边'} · {relationNames[edge.relation] ?? edge.relation}</span>
          <button className="idx-text-btn" onClick={() => onSelect(other)}>{byID.get(other)?.label ?? other}</button>
          {edge.anchor && <small>锚点 · {edge.anchor}{edge.line_start ? ' · 行 '+edge.line_start+(edge.line_end?'–'+edge.line_end:''):''}</small>}
          {edge.source_field && <small>外键 · {edge.source_field} → {edge.target_field}{edge.separator?' · 按 '+edge.separator+' 拆分':''}</small>}
          {edge.description && <small>{edge.description}</small>}
        </li>
      })}</ul></section>}
    <IndexProperties node={node}/>
    {events.length>0 && <details className="drawer-section idx-tech-details"><summary>关联时态记录 · {events.length} 条</summary><ul className="idx-relations">{events.map((e,i)=><li key={i}><span>{String(e.label)}</span><small>{String(e.starts_at)}{e.ends_at?' 至 '+String(e.ends_at):''} {String(e.scope ?? '')}</small></li>)}</ul></details>}
  </div>
}

const propLabels:Record<string,string>={event:'事件',distinct_key:'去重键',validation_status:'核验状态',default_timezone:'统计时区',grain:'统计粒度',lookup_key:'查询键',environment:'数据环境',date_bucket:'时间粒度',aliases:'别名',summary:'摘要',domain_boundary:'领域边界',boundary:'领域边界',formula:'计算口径',definition:'统计定义',dimensions:'下钻维度',owner:'负责人',lifecycle:'状态',cognitive_layer:'认知分层',hub_role:'枢纽性质',artifact_type:'资料类型',status_flow:'状态流转',attributes:'内聚属性',pending_confirmation:'待确认',read_strategy:'读取方式'}
function IndexProperties({node}:{node:IndexNode}){
  const rows=Object.entries(node.properties ?? {})
  const value=(v:unknown)=> v==null?'待确认':typeof v==='string'?v:Array.isArray(v)?v.map(x=>typeof x==='object'?JSON.stringify(x):String(x)).join('、'):typeof v==='object'?JSON.stringify(v,null,2):String(v)
  const words:Record<string,string>={verified_with_open_checks:'已核对，仍有待确认项',active:'生效中',deprecated:'已废弃',document:'业务文档',event_spec:'埋点规范',config_table:'配置表',tool:'工具',skill:'技能',S0:'底层资料',S1:'稳态玩法',S2:'版本与活动干预',S3:'分析指标',sink:'公共汇点',source:'核心源点',user:'用户',day:'自然日'}
  function row([k,v]:[string,unknown]) {return <div key={k} className={/uncertain|low_confidence|pending_confirmation/.test(k)?'idx-warning':''}><dt>{propLabels[k] ?? k}</dt><dd>{typeof v==='string' && words[v] ? words[v]:value(v)}</dd></div>}
  return <section className="drawer-section"><dl className="idx-fields">{rows.filter(([k])=>k in propLabels && k!=='owner' && k!=='lifecycle').map(row)}</dl>
    <details className="idx-tech-details"><summary>字段与技术索引</summary><dl className="idx-fields"><div><dt>索引 ID</dt><dd>{node.id}</dd></div>{rows.filter(([k])=>!(k in propLabels)).map(row)}</dl></details></section>
}

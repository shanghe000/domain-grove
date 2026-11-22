import { useCallback, useEffect, useMemo, useRef, useState } from 'react'

import { encodeWorkbook, explain, request, RequestError } from './api/client'
import { ChatSidebar } from './components/ChatSidebar'
import { NodeDrawer } from './components/NodeDrawer'
import { PortalCanvas } from './components/PortalCanvas'
import { edgeKey, type ChatMessage, type Draft, type DraftPatch, type Graph, type Operation, type Progress, type WorkbenchState } from './types'

function stagedGraph(graph: Graph, patch?: DraftPatch): Graph {
  if (!patch) return graph
  const deletedNodes = new Set(patch.removed_nodes)
  const deletedEdges = new Set(patch.removed_edges?.map(edgeKey))
  const nodes = new Map(graph.nodes.filter((node) => !deletedNodes.has(node.id)).map((node) => [node.id, node]))
  const edges = new Map(graph.edges.filter((edge) => !deletedEdges.has(edgeKey(edge))).map((edge) => [edgeKey(edge), edge]))
  const artifacts = new Map(graph.artifacts.filter((a) => !deletedNodes.has(a.node_id)).map((a) => [a.node_id, a]))
  patch.nodes.forEach((node) => nodes.set(node.id, node))
  patch.edges.forEach((edge) => edges.set(edgeKey(edge), edge))
  patch.artifacts.forEach((a) => artifacts.set(a.node_id, a))
  return {
    nodes: [...nodes.values()],
    edges: [...edges.values()].filter((edge) => nodes.has(edge.src_id) && nodes.has(edge.dst_id)),
    artifacts: [...artifacts.values()],
  }
}

function validLoginURL(value?: string) {
  if (!value) return ''
  try {
    const url = new URL(value, window.location.href)
    return url.protocol === 'https:' || (url.origin === window.location.origin && value.startsWith('/')) ? url.href : ''
  } catch {
    return ''
  }
}

export function EngineMark() {
  return <span className="engine-mark" aria-hidden="true"><i /><i /><i /></span>
}

const visibleChanges = (p?: DraftPatch) => p ?
  p.nodes.length + p.edges.length + p.artifacts.length + (p.removed_nodes?.length ?? 0) + (p.removed_edges?.length ?? 0) : 0

interface DraftUpdate {
  session_id: string
  focus_domain_id?: string
  draft_patch: DraftPatch
  draft?: Draft
}

export function App() {
  const [state, setState] = useState<WorkbenchState>()
  const [failed, setFailed] = useState<{ message: string; login: string }>()
  const [domainId, setDomainId] = useState('')
  const [selectedId, setSelectedId] = useState('')
  const [messages, setMessages] = useState<ChatMessage[]>([])
  const [progress, setProgress] = useState<Progress[]>([])
  const [working, setWorking] = useState('')
  const [notice, setNotice] = useState<{ text: string; error?: boolean }>()
  const [confirmDiscard, setConfirmDiscard] = useState(false)
  const [mobileChat, setMobileChat] = useState(false)
  const discardDialog = useRef<HTMLDialogElement>(null)
  const activeDomain = useRef('')
  const changeDomain = useCallback((id: string) => {
    if (activeDomain.current === id) return
    activeDomain.current = id
    setDomainId(id)
    setSelectedId('')
  }, [])

  const load = useCallback(async () => {
    try {
      const value = await request<WorkbenchState>('/api/v1/state')
      setState(value)
      setFailed(undefined)
    } catch (error) {
      setFailed({ message: explain(error), login: error instanceof RequestError ? validLoginURL(error.loginURL) : '' })
      if (error instanceof RequestError && [401, 403].includes(error.status)) setState(undefined)
    }
  }, [])

  useEffect(() => { void load() }, [load])

  const signedIn = Boolean(state)
  useEffect(() => {
    if (!signedIn) return
    const live = new EventSource('/api/v1/events/stream')
    let connected = false
    live.onopen = () => {
      if (connected) void load()
      connected = true
    }
    live.addEventListener('draft_updated', (event) => {
      const { session_id, focus_domain_id, draft_patch, draft } = JSON.parse(event.data) as DraftUpdate
      if (!draft) {
        void load()
      } else {
        setState((state) => {
          if (!state) return state
          if (draft.status === 'staging') return { ...state, draft }
          return state.draft?.session_id === session_id ? { ...state, draft: null } : state
        })
      }
      if (focus_domain_id && (!draft || draft.status === 'staging') && visibleChanges(draft_patch) > 0) {
        changeDomain(focus_domain_id)
      }
    })
    live.addEventListener('chat_progress', (event) => {
      const data = JSON.parse(event.data) as Progress
      setProgress((previous) => [...previous.filter((item) => item.tool !== data.tool), data])
    })
    live.addEventListener('graph_committed', () => { void load() })
    let inactivityTimer: number | undefined
    live.onerror = () => {
      window.clearTimeout(inactivityTimer)
      inactivityTimer = window.setTimeout(() => void load(), 3000)
    }
    return () => {
      live.close()
      window.clearTimeout(inactivityTimer)
    }
  }, [signedIn, load, changeDomain])

  useEffect(() => {
    if (confirmDiscard && !discardDialog.current?.open) discardDialog.current?.showModal()
    if (!confirmDiscard && discardDialog.current?.open) discardDialog.current?.close()
  }, [confirmDiscard])

  useEffect(() => {
    if (!notice) return
    const timeout = window.setTimeout(() => setNotice(undefined), 5200)
    return () => window.clearTimeout(timeout)
  }, [notice])

  useEffect(() => {
    const escape = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && !discardDialog.current?.open) {
        setSelectedId('')
        setMobileChat(false)
      }
    }
    window.addEventListener('keydown', escape)
    return () => window.removeEventListener('keydown', escape)
  }, [])

  const draft = state?.draft
  const patch = draft?.draft_patch
  const merged = useMemo(() => state ? stagedGraph(state.graph, patch) : undefined, [state, patch])
  const draftedNodeIds = useMemo(() => new Set(patch ? [
    ...patch.nodes.map((item) => item.id), ...patch.artifacts.map((item) => item.node_id),
  ] : []), [patch])
  const publishedNodes = state?.graph.nodes
  const newDraftNodeIds = useMemo(() => {
    const published = new Set(publishedNodes?.map((node) => node.id))
    return new Set(patch?.nodes.filter((node) => !published.has(node.id)).map((node) => node.id))
  }, [patch, publishedNodes])
  const selected = merged?.nodes.find((node) => node.id === selectedId)
  const selectedArtifact = merged?.artifacts.find((a) => a.node_id === selectedId)
  const currentDomain = merged?.nodes.find((node) => node.id === domainId && node.node_type === 'Domain')
  const conflicted = Boolean(draft && draft.base_version !== state?.version)

  const applyDraft = (item: Draft | null, navigate = false) => {
    setState((prev) => prev ? { ...prev, draft: item?.status === 'staging' ? item : null } : prev)
    if (navigate && item?.focus_domain_id) changeDomain(item.focus_domain_id)
  }

  const send = async (message: string, files: File[]) => {
    setWorking('chat')
    setProgress([])
    const detail = files.length ? [message, ...files.map((file) => file.name)].filter(Boolean).join('\n') : message
    setMessages((previous) => [...previous, { role: 'user', text: detail }])
    try {
      const uploaded_files = await Promise.all(files.map(encodeWorkbook))
      const result = await request<{ message: string; draft: Draft; events?: Progress[] }>('/api/v1/chat/message', {
        ...(draft ? {session_id: draft.session_id} : {}), message, uploaded_files,
      })
      setMessages((previous) => [...previous, {role: 'assistant', text: result.message}])
      applyDraft(result.draft, visibleChanges(result.draft.draft_patch) > 0)
      if (visibleChanges(result.draft.draft_patch) > 0) setMobileChat(false)
    } catch (error) {
      setMessages((previous) => [...previous, {role: 'error', text: explain(error)}])
      if (error instanceof RequestError && error.status === 401) void load()
      throw error
    } finally {
      setWorking('')
    }
  }

  const patchGraph = async (operations: Operation[]) => {
    setWorking('patch')
    try {
      const item = await request<Draft>('/api/v1/actions/patch_draft_graph', {
        ...(draft ? {session_id: draft.session_id} : {}), actor: 'human', operations,
      })
      applyDraft(item)
    } catch (error) {
      setNotice({text: explain(error), error: true})
      if (error instanceof RequestError && error.status === 401) void load()
      throw error
    } finally {
      setWorking('')
    }
  }

  const perform = async (action: 'commit' | 'undo' | 'discard') => {
    if (!draft) return
    setWorking(action)
    setConfirmDiscard(false)
    try {
      const path = `/api/v1/actions/${action}_draft`
      if (action === 'commit') {
        const result = await request<{snapshot_pending: boolean}>(path, {session_id: draft.session_id})
        applyDraft(null)
        await load()
        setNotice({text: result.snapshot_pending ? '图谱已发布，导出文件等待修复。' : '图谱已入库，BI Agent 可以查询本次发布。'})
      } else {
        const item = await request<Draft>(path, {session_id: draft.session_id})
        applyDraft(item)
        setNotice({text: action === 'undo' ? '已撤销上一步。' : '草稿已放弃。'})
      }
    } catch (error) {
      setNotice({text: explain(error), error: true})
      if (error instanceof RequestError && error.status === 401) void load()
    } finally {
      setWorking('')
    }
  }

  const logout = async () => {
    const result = await fetch('/auth/logout', {method: 'POST'})
    if (result.ok) window.location.reload()
    else setNotice({text: '退出失败，请稍后重试。', error: true})
  }

  if (!state || !merged) {
    return <main className="login-screen">
      <section aria-live="polite">
        <EngineMark />
        <h1>知域 · Domain Grove</h1>
        <p>{failed?.message ?? '正在读取工作台…'}</p>
        {failed?.login ? <a className="primary-button" href={failed.login}>登录</a>
          : failed && <button className="secondary-button" onClick={() => void load()}>重新加载</button>}
      </section>
    </main>
  }

  const domains = merged.nodes.filter((node) => node.node_type === 'Domain' && node.status !== 'deprecated').length
  const nodeCount = merged.nodes.filter((node) => node.node_type !== 'Domain' && node.status !== 'deprecated').length
  const hasDraft = visibleChanges(patch) > 0
  return <div className="engine-workbench">
    <header className="engine-header">
      <div className="engine-brand"><EngineMark /><h1>知域 <span>Domain Grove</span></h1></div>
      <div className="engine-toolbar">
        <button type="button" className="mobile-chat-toggle secondary-button" onClick={() => setMobileChat((visible) => !visible)}
          aria-expanded={mobileChat} aria-controls="governance-pane">{mobileChat ? '收起治理' : '治理 AI'}</button>
        <span className={state.mock ? 'engine-mode engine-mode--mock' : 'engine-mode'}>
          <i aria-hidden="true" />{state.mock ? '演示模式' : '已连接'}
        </span>
        <span className="engine-user">{state.user.user_id === 'dev_user' ? '本地演示' : state.user.username}</span>
        {state.user.user_id !== 'dev_user' && <button className="quiet-button" type="button" onClick={() => void logout()}>退出</button>}
      </div>
    </header>

    <div className="engine-content">
      <div id="governance-pane" className={`governance-pane ${mobileChat ? 'governance-pane--mobile' : ''}`}>
        <ChatSidebar mock={state.mock} identity={state.user} messages={messages} progress={progress} busy={Boolean(working)} onSend={send} />
      </div>

      <main className="canvas-workspace" aria-label="业务本体图谱">
        <PortalCanvas graph={merged} domainId={currentDomain?.id ?? ''} onDomainChange={changeDomain}
          selectedId={selectedId} onSelect={setSelectedId} draftNodeIds={newDraftNodeIds}
          onMoveNode={!working && !conflicted ? (node_id, target_domain) => {
            void patchGraph([{op: 'move_node_domain', node_id, target_domain}]).catch(() => undefined)
          } : undefined} />

        <div className="canvas-toolbar">
          <nav className="graph-breadcrumb" aria-label="图谱层级">
            <button type="button" className="quiet-button" onClick={() => changeDomain('')} aria-current={currentDomain ? undefined : 'page'}>全局域</button>
            {currentDomain && <><span aria-hidden="true">/</span><span aria-current="page">{currentDomain.label}</span></>}
          </nav>
          <div className="graph-totals"><span>{domains} 个领域</span><span>{nodeCount} 个业务节点</span><span>{merged.edges.length} 条关系</span></div>
        </div>
        {!draft && <p className="portal-hint">
          <span>单击节点查看属性 · 双击领域进入</span>
          <span>点按查看属性 · 从详情进入领域</span>
        </p>}

        {draft &&
          <section className="draft-bar" aria-label="草稿操作">
            <div className="draft-meta"><i aria-hidden="true" /><span>待发布草稿</span>{hasDraft && <small>{visibleChanges(patch)} 项图谱变更</small>}</div>
            {conflicted && <p className="draft-conflict">图谱已更新，请放弃旧草稿后重新建模。</p>}
            <div className="draft-controls">
              <button type="button" className="quiet-button" disabled={Boolean(working)} onClick={() => setConfirmDiscard(true)}>放弃</button>
              <button type="button" className="secondary-button" disabled={Boolean(working) || draft.undo_count < 1} onClick={() => void perform('undo')}>撤销上一步</button>
              <button type="button" className="primary-button" disabled={Boolean(working) || !hasDraft || conflicted} onClick={() => void perform('commit')}>
                {working === 'commit' ? '正在入库…' : '确认应用并入库'}
              </button>
            </div>
          </section>}

        <div className="graph-legend" aria-label="关系图例">
          <span><i className="legend-depends" />口径依赖</span>
          <span><i className="legend-impact" />受影响于</span>
          <span><i className="legend-definition" />定义或配置</span>
          <span><i className="legend-foreign" />外键关联</span>
        </div>
        {notice && <div className={notice.error ? 'engine-toast engine-toast--error' : 'engine-toast'} role="status">{notice.text}</div>}
      </main>

      {selected && <div className="drawer-pane">
        <NodeDrawer key={selected.id} node={selected} artifact={selectedArtifact} graph={merged} drafted={draftedNodeIds.has(selected.id)}
          publishedArtifact={state.graph.artifacts.some((artifact) => artifact.node_id === selected.id)}
          busy={Boolean(working) || conflicted} onClose={() => setSelectedId('')} onPatch={patchGraph}
          onInspectNode={setSelectedId}
          onEnterDomain={selected.node_type === 'Domain' && selected.id !== currentDomain?.id ? () => changeDomain(selected.id) : undefined} />
      </div>}
    </div>

    <dialog ref={discardDialog} className="engine-dialog" onCancel={() => setConfirmDiscard(false)}>
      <h2>放弃这份草稿？</h2><p>草稿中的节点、关系及字段修改将被丢弃。</p>
      <div><button type="button" className="secondary-button" onClick={() => setConfirmDiscard(false)}>继续编辑</button>
        <button type="button" className="danger-button" onClick={() => void perform('discard')}>放弃草稿</button></div>
    </dialog>
  </div>
}

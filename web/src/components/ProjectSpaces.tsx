import { useEffect, useRef, useState } from 'react'
import { explain, request } from '../api/client'

export type ProjectSpace = { id: string; name: string; role: 'owner' | 'reviewer' }
type Member = { subject: string; role: ProjectSpace['role'] }
type ListState<T> = { items: T[]; error?: never } | { items?: never; error: string }
type Scope = 'read' | 'submit'
type Credential = { scope: Scope; token: string }

const spaceAPI = '/api/v1/spaces'
const roleLabel: Record<ProjectSpace['role'], string> = { owner: '负责人', reviewer: '审核员' }
const scopeLabel: Record<Scope, string> = { read: '读取', submit: '提交' }
const validID = /^[a-z0-9]+(?:-[a-z0-9]+)*$/

export function projectIndexPath(id: string) {
  return id === 'default' ? '/api/v1/index' : spaceAPI + '/' + encodeURIComponent(id) + '/index'
}

type Props = {
  spaces: ProjectSpace[]
  current: ProjectSpace
  canCreate: boolean
  ownSubject: string
  onSelect: (id: string) => void
  onCreated: (space: ProjectSpace) => void
}

export function ProjectSpaces({ spaces, current, canCreate, ownSubject, onSelect, onCreated }: Props) {
  const [dialog, setDialog] = useState<'create' | 'manage'>()
  return <div className="idx-projects">
    <label className="idx-project-select">
      <span>知识空间</span>
      <select value={current.id} onChange={event => onSelect(event.target.value)}>
        {spaces.map(space => <option key={space.id} value={space.id}>{space.name}</option>)}
      </select>
    </label>
    {canCreate && <button type="button" className="idx-btn idx-quiet" onClick={() => setDialog('create')}>新建空间</button>}
    {current.role === 'owner' && <button type="button" className="idx-btn idx-quiet" onClick={() => setDialog('manage')}>空间设置</button>}
    {dialog === 'create' && <CreateSpace onClose={() => setDialog(undefined)} onCreated={space => {
      setDialog(undefined)
      onCreated(space)
    }} />}
    {dialog === 'manage' && <ManageSpace space={current} ownSubject={ownSubject} onClose={() => setDialog(undefined)} />}
  </div>
}

function useDialog() {
  const node = useRef<HTMLDialogElement>(null)
  useEffect(() => { node.current?.showModal() }, [])
  return node
}

function CreateSpace({ onClose, onCreated }: {onClose: () => void; onCreated: (space: ProjectSpace) => void}) {
  const modal = useDialog()
  const [name, setName] = useState('')
  const [id, setID] = useState('')
  const [running, setRunning] = useState(false)
  const [error, setError] = useState('')
  const valid = validID.test(id) && id !== 'default' && id !== 'sse' && id.length <= 48

  async function create() {
    if (running || !name.trim() || !valid) return
    setRunning(true)
    setError('')
    try { onCreated(await request<ProjectSpace>(spaceAPI, { id, name: name.trim() })) }
    catch (problem) { setError(explain(problem)); setRunning(false) }
  }

  return <dialog ref={modal} className="idx-space-dialog" aria-labelledby="idx-space-create-title"
    onCancel={event => { event.preventDefault(); if (!running) onClose() }}>
    <header><h2 id="idx-space-create-title">新建知识空间</h2><button type="button" className="idx-btn idx-quiet" disabled={running} onClick={onClose}>关闭</button></header>
    <form onSubmit={event => { event.preventDefault(); void create() }}>
      <p className="idx-muted">新空间从空图谱开始，拥有自己的审核队列和发布版本。</p>
      <label>项目名称<input value={name} maxLength={100} required autoFocus
        onChange={event => setName(event.target.value)} placeholder="例如 阅读社区" /></label>
      <label>空间标识<input value={id} maxLength={48} required
        onChange={event => setID(event.target.value)} placeholder="例如 reading-club" /></label>
      <p className="idx-model-help">小写字母、数字和连字符。空间标识用于 MCP 地址。</p>
      {error && <p className="idx-error" role="alert">{error}</p>}
      <footer>
        <button type="button" className="idx-btn" disabled={running} onClick={onClose}>取消</button>
        <button type="submit" className="idx-btn idx-primary" disabled={running || !name.trim() || !valid}>{running ? '正在创建…' : '创建空间'}</button>
      </footer>
    </form>
  </dialog>
}

function ManageSpace({ space, ownSubject, onClose }: {space: ProjectSpace; ownSubject: string; onClose: () => void}) {
  const modal = useDialog()
  const base = spaceAPI + '/' + encodeURIComponent(space.id)
  const mcpEndpoint = window.location.origin + '/mcp/' + encodeURIComponent(space.id)
  const [members, setMembers] = useState<ListState<Member>>()
  const [membersVersion, setMembersVersion] = useState(0)
  const [credential, setCredential] = useState<Credential>()
  const [scope, setScope] = useState<Scope>()
  const [removal, setRemoval] = useState('')
  const [working, setWorking] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')

  useEffect(() => {
    let current = true
    request<{ members: Member[] }>(base + '/members').then(data => {
      if (current) setMembers({ items: data.members ?? [] })
    }).catch(problem => { if (current) setMembers({ error: explain(problem) }) })
    return () => { current = false }
  }, [base, membersVersion])

  async function change(action: () => Promise<void>) {
    if (working) return
    setWorking(true)
    setError('')
    setNotice('')
    try { await action() }
    catch (problem) { setError(explain(problem)) }
    finally { setWorking(false) }
  }

  async function removeReviewer(id: string) {
    await change(async () => {
      await request<void>(base + '/members/remove', { subject: id })
      setMembers(existing => ({ items: existing?.items?.filter(item => item.subject !== id) ?? [] }))
      setRemoval('')
      setNotice('审核员已移除。')
    })
  }

  async function issue(next: Scope) {
    await change(async () => {
      setCredential(undefined)
      setCredential(await request<Credential>(base + '/credentials', { scope: next }))
      setScope(undefined)
    })
  }

  async function copy(value: string) {
    try { await navigator.clipboard.writeText(value); setNotice('已复制。') }
    catch { setNotice('请选中文本后手动复制。') }
  }

  return <dialog ref={modal} className="idx-space-dialog" aria-labelledby="idx-space-manage-title"
    onCancel={event => { event.preventDefault(); if (!working) onClose() }}>
    <header><h2 id="idx-space-manage-title">{space.name} · 空间设置</h2>
      <button type="button" className="idx-btn idx-quiet" disabled={working} onClick={onClose}>关闭</button></header>
    <div className="idx-space-settings">
      <section>
        <h3>MCP 接入</h3>
        <label>MCP 地址<input value={mcpEndpoint} readOnly onFocus={event => event.target.select()} /></label>
        <p className="idx-model-help">读取凭据可读取本项目发布图谱，提交凭据还可提交提案。</p>
        <div className="idx-space-actions">
          <button type="button" className="idx-btn" disabled={working} onClick={() => { setScope('read'); setRemoval('') }}>签发读取凭据</button>
          <button type="button" className="idx-btn" disabled={working} onClick={() => { setScope('submit'); setRemoval('') }}>签发提交凭据</button>
        </div>
        {scope && <div className="idx-space-confirm">
          <p>新凭据会替换当前{scopeLabel[scope]}凭据。</p>
          <button type="button" className="idx-btn" disabled={working} onClick={() => setScope(undefined)}>取消</button>
          <button type="button" className="idx-btn idx-primary" disabled={working} onClick={() => void issue(scope)}>确认签发</button>
        </div>}
        {credential && <div className="idx-space-credential">
          <label>{scopeLabel[credential.scope]}凭据（仅此次显示）
            <input value={credential.token} readOnly autoComplete="off" spellCheck={false} onFocus={event => event.target.select()} />
          </label>
          <button type="button" className="idx-btn" onClick={() => void copy(credential.token)}>复制凭据</button>
        </div>}
      </section>
      <section>
        <h3>项目成员</h3>
        {!members && <p role="status" className="idx-muted">正在读取成员…</p>}
        {members?.error && <div className="idx-space-actions">
          <p role="alert" className="idx-error">无法读取项目成员。{members.error}</p>
          <button type="button" className="idx-btn" onClick={() => { setMembers(undefined); setMembersVersion(version => version + 1) }}>重试读取成员</button>
        </div>}
        {!!members?.items?.length && <ul className="idx-space-members">
          {members.items.map(member => <li key={member.subject}>
              <code>{member.subject}</code>
              <small>{roleLabel[member.role]}{member.subject === ownSubject ? ' · 你' : ''}</small>
              {member.role === 'reviewer' &&
                (removal === member.subject ? <div className="idx-space-actions">
                  <button type="button" className="idx-btn idx-quiet" onClick={() => setRemoval('')}>取消</button>
                  <button type="button" className="idx-btn idx-danger" disabled={working} onClick={() => void removeReviewer(member.subject)}>确认移除</button>
                </div> : <button type="button" className="idx-btn idx-quiet" disabled={working} onClick={() => { setRemoval(member.subject); setScope(undefined) }}>移除</button>)}
            </li>)}
        </ul>}
      </section>
      {error && <p className="idx-error" role="alert">{error}</p>}
      {notice && <p className="idx-model-help" role="status">{notice}</p>}
    </div>
  </dialog>
}

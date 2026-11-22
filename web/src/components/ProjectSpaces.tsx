import { useEffect, useRef, useState } from 'react'
import { explain, request } from '../api/client'

export type ProjectSpace = { id: string; name: string; role: 'owner' | 'reviewer' }
type Member = { subject: string; role: ProjectSpace['role']; display_name?: string }
type Candidate = { subject_id: string; display_name: string }
type ListState<T> = { items: T[]; error?: never } | { items?: never; error: string }
type Scope = 'read' | 'submit'
type Credential = { scope: Scope; token: string }

const spaceAPI = '/api/v1/spaces'
const roleLabel: Record<ProjectSpace['role'], string> = { owner: '负责人', reviewer: '审核员' }
const scopeLabel: Record<Scope, string> = { read: '读取', submit: '提交' }
const validID = /^[a-z0-9]+(?:-[a-z0-9]+)*$/

function countNames(names: string[]) {
  const counts = new Map<string, number>()
  for (const name of names) counts.set(name, (counts.get(name) ?? 0) + 1)
  return counts
}

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
  const [candidates, setCandidates] = useState<ListState<Candidate>>()
  const [directoryNames, setDirectoryNames] = useState<Record<string, string>>({})
  const [membersVersion, setMembersVersion] = useState(0)
  const [candidatesVersion, setCandidatesVersion] = useState(0)
  const [subject, setSubject] = useState('')
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

  useEffect(() => {
    let current = true
    request<{ candidates: Candidate[]; member_names?: Record<string, string> }>(base + '/member-candidates').then(data => {
      if (current) {
        setCandidates({ items: data.candidates ?? [] })
        setDirectoryNames(data.member_names ?? {})
      }
    }).catch(problem => { if (current) setCandidates({ error: explain(problem) }) })
    return () => { current = false }
  }, [base, candidatesVersion])

  const memberIDs = new Set(members?.items?.map(member => member.subject))
  const namesBySubject = new Map(candidates?.items?.map(candidate => [candidate.subject_id, candidate.display_name?.trim() ?? '']))
  const eligible = [...namesBySubject].filter(([id]) => id !== ownSubject && !memberIDs.has(id))
    .map(([id, name]) => ({ id, name: name || '未设置姓名' }))
    .sort((a, b) => a.name.localeCompare(b.name, 'zh-CN') || a.id.localeCompare(b.id))
  const nameCounts = countNames(eligible.map(person => person.name))
  const memberRows = (members?.items ?? []).map(member => ({ ...member,
    name: member.display_name?.trim() || directoryNames[member.subject]?.trim() || namesBySubject.get(member.subject) || '',
  }))
  const memberNameCounts = countNames(memberRows.map(member => member.name))
  const selected = eligible.find(person => person.id === subject)
  const canAdd = !!members?.items && !!candidates?.items

  async function change(action: () => Promise<void>) {
    if (working) return
    setWorking(true)
    setError('')
    setNotice('')
    try { await action() }
    catch (problem) { setError(explain(problem)) }
    finally { setWorking(false) }
  }

  async function addReviewer() {
    if (!canAdd || !selected) return
    await change(async () => {
      const member = await request<Member>(base + '/members', { subject: selected.id })
      setMembers(existing => ({ items: [...(existing?.items ?? []).filter(item => item.subject !== member.subject),
        { ...member, display_name: member.display_name || namesBySubject.get(member.subject) }] }))
      setSubject('')
      setNotice('审核员已添加。')
    })
  }

  async function removeReviewer(id: string) {
    await change(async () => {
      await request<void>(base + '/members/remove', { subject: id })
      setMembers(existing => ({ items: existing?.items?.filter(item => item.subject !== id) ?? [] }))
      setCandidates(undefined)
      setCandidatesVersion(version => version + 1)
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
        <p className="idx-model-help">添加后，此人可审核本项目的提案。</p>
        {!members && <p role="status" className="idx-muted">正在读取成员…</p>}
        {members?.error && <div className="idx-space-actions">
          <p role="alert" className="idx-error">无法读取项目成员。{members.error}</p>
          <button type="button" className="idx-btn" onClick={() => { setMembers(undefined); setMembersVersion(version => version + 1) }}>重试读取成员</button>
        </div>}
        {!!memberRows.length && <ul className="idx-space-members">
          {memberRows.map(member => <li key={member.subject}>
              {member.name ? <span className="idx-space-member-name">{member.name}</span> : <code>{member.subject}</code>}
              <small>{roleLabel[member.role]}{member.subject === ownSubject ? ' · 你' : ''}
                {member.name && (memberNameCounts.get(member.name) ?? 0) > 1 && <> · <code>{member.subject}</code></>}
              </small>
              {member.role === 'reviewer' &&
                (removal === member.subject ? <div className="idx-space-actions">
                  <button type="button" className="idx-btn idx-quiet" onClick={() => setRemoval('')}>取消</button>
                  <button type="button" className="idx-btn idx-danger" disabled={working} onClick={() => void removeReviewer(member.subject)}>确认移除</button>
                </div> : <button type="button" className="idx-btn idx-quiet" disabled={working} onClick={() => { setRemoval(member.subject); setScope(undefined) }}>移除</button>)}
            </li>)}
        </ul>}
        <form className="idx-add-member" onSubmit={event => { event.preventDefault(); void addReviewer() }}>
          <label>可添加的审核员
            <select value={selected?.id ?? ''} required disabled={working || !canAdd || !eligible.length}
              onChange={event => setSubject(event.target.value)}>
              <option value="" disabled>请选择姓名</option>
              {canAdd && eligible.map(person => <option key={person.id} value={person.id}>
                {person.name}{(nameCounts.get(person.name) ?? 0) > 1 ? `（${person.id}）` : ''}
              </option>)}
            </select>
          </label>
          <button type="submit" className="idx-btn" disabled={working || !canAdd || !selected}>添加审核员</button>
        </form>
        {!candidates && <p role="status" className="idx-muted">正在读取可添加人员…</p>}
        {candidates?.error && <div className="idx-space-actions">
          <p role="alert" className="idx-error">无法读取可添加人员。{candidates.error}</p>
          <button type="button" className="idx-btn" onClick={() => { setCandidates(undefined); setCandidatesVersion(version => version + 1) }}>重试读取人员</button>
        </div>}
        {canAdd && !eligible.length && <p role="status" className="idx-muted">当前没有可添加的审核员。</p>}
      </section>
      {error && <p className="idx-error" role="alert">{error}</p>}
      {notice && <p className="idx-model-help" role="status">{notice}</p>}
    </div>
  </dialog>
}

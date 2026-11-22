import { useEffect, useState } from 'react'
import { explain, request, RequestError } from '../api/client'
import type { Graph, Patch, Proposal, Reference } from '../IndexApp'
import { PanelIcon } from './ChatSidebar'

export type ModelingDraft = {
  title: string
  rationale?: string
  base_revision: number
  patch: Patch
  preview: Graph
  request_id: string
}

type ReferenceInput = { key: number; repository: string; path: string; start: string; end: string; anchor: string }
const freshReference = (key: number): ReferenceInput => ({ key, repository: '', path: '', start: '', end: '', anchor: '' })
type Props = {
  active: boolean
  readOnly: boolean
  endpoint: string
  revision?: number
  draft?: ModelingDraft
  onDraft: (draft?: ModelingDraft) => void
  onSubmitted: (proposal: Proposal) => void
  onLogin: (url: string) => void
}

export function ModelingPanel({ active, readOnly, endpoint, revision, draft, onDraft, onSubmitted, onLogin }: Props) {
  const [enabled, setEnabled] = useState<boolean>()
  const [instruction, setInstruction] = useState('')
  const [refs, setRefs] = useState<ReferenceInput[]>([])
  const [working, setWorking] = useState<'' | 'plan' | 'submit'>('')
  const [error, setError] = useState('')
  const [reloadStatus, setReloadStatus] = useState(0)
  const [intakeMessage,setIntakeMessage]=useState('')
  const trimmed=instruction.trim()
  const source=/^https?:\/\/[^\s]+$/i.test(trimmed) ? {url:trimmed} : /^(同步|拉取)(最新)?(数仓)?埋点(版本)?$/.test(trimmed) ? {source:'tracking'} : null
  const [nextRef, setNextRef] = useState(0)

  useEffect(() => {
    if (!active || readOnly) return
    let current = true
    request<{ enabled: boolean }>(endpoint).then(status => {
      if (!current) return
      setEnabled(status.enabled)
    }).catch(problem => {
      if (!current) return
      if (problem instanceof RequestError && problem.status === 401 && problem.loginURL) onLogin(problem.loginURL)
      else setError(explain(problem))
    })
    return () => { current = false }
  }, [active, endpoint, onLogin, readOnly, reloadStatus])

  function report(problem: unknown) {
    if (problem instanceof RequestError && problem.status === 401 && problem.loginURL) onLogin(problem.loginURL)
    else setError(explain(problem))
  }

  async function plan() {
    if (readOnly || (!enabled && !source) || working || !trimmed) return
    setError('')
    setIntakeMessage('')
    onDraft()
    setWorking('plan')
    const references: Reference[] = refs.map(item => ({
      repository: item.repository.trim(),
      path: item.path.trim(),
      ...(item.start && { line_start: Number(item.start) }),
      ...(item.end && { line_end: Number(item.end) }),
      ...(item.anchor.trim() && { anchor: item.anchor.trim() }),
    }))
    try {
      if(source){
        const result=await request<{proposal?:Proposal;message:string}>(endpoint.replace(/\/modeling$/, '/ingest'),source)
        if(result.proposal){setInstruction('');onSubmitted(result.proposal)} else setIntakeMessage(result.message)
        return
      }
      const result = await request<Omit<ModelingDraft, 'request_id'>>(endpoint, { instruction: instruction.trim(), references })
      onDraft({ ...result, request_id: crypto.randomUUID() })
    } catch (problem) { report(problem) }
    finally { setWorking('') }
  }

  async function submit() {
    if (!draft || working || revision !== draft.base_revision) return
    setError('')
    setWorking('submit')
    const { request_id, title, rationale, base_revision, patch } = draft
    try {
      const proposal = await request<Proposal>(endpoint + '/submit', { request_id, title, rationale, base_revision, patch })
      setInstruction('')
      setRefs([])
      onSubmitted(proposal)
    } catch (problem) { report(problem) }
    finally { setWorking('') }
  }

  function editReference(key: number, field: Exclude<keyof ReferenceInput, 'key'>, value: string) {
    setRefs(items => items.map(item => item.key === key ? { ...item, [field]: value } : item))
    onDraft()
  }

  const stale = draft && revision !== draft.base_revision
  const composerEnabled = !readOnly && enabled === true
  return <div className="idx-modeling" hidden={!active}>
    <div className="governance-scroll">
      <div className="governance-start">
        <h3>从一条建模指令开始</h3>
        <p>描述要调整的业务节点或有向关系，可附知识定位。草稿会在画布上预览，经审核后发布。</p>
      </div>
      {readOnly ? <p className="idx-muted">当前为只读预览。请在 HTTPS 工作台登录后建模。</p> : <>
        {enabled === undefined && !error && <p role="status" className="idx-muted">正在检查 AI 建模配置…</p>}
        {enabled === false && <p className="idx-warning">尚未配置 DeepSeek 服务凭据。BI Agent 仍可通过 MCP 提交提案。</p>}
        {error && <p className="idx-error" role="alert">{error}</p>}
        {enabled === undefined && error && <button type="button" className="idx-btn idx-quiet" onClick={() => {
          setError('')
          setReloadStatus(value => value + 1)
        }}>重试</button>}
      </>}
    </div>
    {intakeMessage && <p role="status" className="idx-muted">{intakeMessage}</p>}
    <form onSubmit={event => { event.preventDefault(); void plan() }} className="index-composer" aria-label="建模指令">
      <label className="idx-sr-only" htmlFor="idx-instruction">建模指令</label>
      <textarea id="idx-instruction" value={instruction} rows={3} maxLength={2600} required
        onChange={event => { setInstruction(event.target.value); onDraft() }}
        onKeyDown={event => {
          if (event.key === 'Enter' && !event.shiftKey && !event.altKey && !event.nativeEvent.isComposing) {
            event.preventDefault()
            event.currentTarget.form?.requestSubmit()
          }
        }}
        placeholder={composerEnabled ? '描述节点、关系或粘贴资料链接…' : '粘贴飞书或配置链接，或输入「同步埋点」'}
        disabled={readOnly || Boolean(working)} />
      {composerEnabled && <details className="idx-reference-set" open={refs.length > 0 ? true : undefined}>
        <summary>知识定位（可选）{refs.length > 0 ? ' · ' + refs.length : ''}</summary>
        {refs.map((ref, position) => <fieldset key={ref.key} disabled={Boolean(working)}>
          <legend>来源 {position + 1}</legend>
          <label>仓库别名<input required value={ref.repository} maxLength={160} placeholder="game-bi-kb" onChange={event => editReference(ref.key, 'repository', event.target.value)} /></label>
          <label>相对路径<input required value={ref.path} maxLength={1024} placeholder="metrics/retention.md" onChange={event => editReference(ref.key, 'path', event.target.value)} /></label>
          <div className="idx-reference-lines">
            <label>起始行<input type="number" inputMode="numeric" min={1} required={Boolean(ref.end)} value={ref.start} onChange={event => editReference(ref.key, 'start', event.target.value)} /></label>
            <label>结束行<input type="number" inputMode="numeric" min={ref.start || 1} value={ref.end} onChange={event => editReference(ref.key, 'end', event.target.value)} /></label>
          </div>
          <label>标题锚点<input value={ref.anchor} maxLength={384} onChange={event => editReference(ref.key, 'anchor', event.target.value)} /></label>
          <button className="idx-btn idx-quiet" type="button" onClick={() => { setRefs(items => items.filter(item => item.key !== ref.key)); onDraft() }}>移除来源</button>
        </fieldset>)}
        <button className="idx-btn idx-quiet" type="button" disabled={refs.length >= 12 || Boolean(working)}
          onClick={() => { setRefs(items => [...items, freshReference(nextRef)]); setNextRef(nextRef + 1); onDraft() }}>添加知识定位</button>
      </details>}
      <button className="primary-button" type="submit" disabled={readOnly || (!composerEnabled && !source) || Boolean(working) || !trimmed}>
        <PanelIcon name="send" />{working === 'plan' ? source?'正在读取资料…':'正在生成草稿…' : source?'导入资料':draft ? '重新生成草稿' : '生成建模草稿'}
      </button>
      {working === 'plan' && <span role="status" className="idx-sr-only">正在生成建模草稿</span>}
    </form>
    {draft && <div className="idx-model-result">
      {stale && <p className="idx-warning">已发布图谱有更新，请重新生成草稿。</p>}
      <button className="idx-btn idx-primary" type="button" onClick={() => void submit()} disabled={Boolean(working || stale)}>
        {working === 'submit' ? '正在提交…' : '提交审核'}
      </button>
    </div>}
  </div>
}

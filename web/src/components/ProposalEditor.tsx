import {useState} from 'react';
import type {Graph,IndexNode,IndexEdge,Patch} from '../IndexApp';
import {relationNames,axiomNames} from '../engine/indexPortal';

export type Edit = {patch:Patch}|{instruction:string}|{undo:true};
type Props={graph:Graph;node?:IndexNode; busy:boolean; onEdit:(edit:Edit)=>Promise<boolean>};
export function ProposalEditor({graph,node,busy,onEdit}:Props) {
  const [mode,setMode]=useState<'node'|'edge'|''>('');
  const [source,setSource]=useState(node?.id ?? graph.nodes[0]?.id ?? '');
  const [dest,setDest]=useState('');
  const [relation,setRelation]=useState('depends_on');
  const [kind,setKind]=useState<IndexNode['kind']>(node?.kind ?? 'concept');
  const [name,setName]=useState(node?.label ?? '');
  const [parent,setParent]=useState(node?.parent_id ?? '');
  const [desc,setDesc]=useState(node?.description ?? '');
  const [rules,setRules]=useState(node?.axioms ?? []);
  const [properties,setProperties]=useState(JSON.stringify(node?.properties ?? {},null,2));
  const [repo,setRepo]=useState(node?.ref?.repository ?? '');
  const [path,setPath]=useState(node?.ref?.path ?? '');
  const [anchor,setAnchor]=useState(node?.ref?.anchor ?? '');
  const [artifactType,setArtifactType]=useState(String(node?.properties?.artifact_type ?? 'document'));
  const [readStrategy,setReadStrategy]=useState(node?.ref?.read_strategy ?? '');
  const [edgeAnchor,setEdgeAnchor]=useState('');
  const [field,setField]=useState(''),[targetField,setTargetField]=useState(''),[separator,setSeparator]=useState('');
  const [error,setError]=useState('');
  const options=graph.nodes.map(n=><option key={n.id} value={n.id}>{n.label}</option>);
  async function save(){
    setError('');
    try {
      let patch:Patch;
      if(mode==='node'){
        const props=JSON.parse(properties);
        if(!props || typeof props!=='object' || Array.isArray(props)) throw new Error('属性须为键值对象。');
        const item:IndexNode={...node,id:node?.id ?? kind+'.'+crypto.randomUUID(),kind,label:name.trim(),description:desc.trim(),
          parent_id:parent,properties:props,axioms:rules.filter(r=>r.text.trim())};
        if(kind==='artifact') {props.artifact_type=artifactType;item.ref={...node?.ref,repository:repo.trim(),path:path.trim(),anchor:anchor.trim(),read_strategy:readStrategy.trim()};}
        patch={upsert_nodes:[item]};
      } else {
        const edge:IndexEdge={id:'edge.'+crypto.randomUUID(),from:source,to:dest,relation,anchor:edgeAnchor.trim()};
        if(relation==='foreign_key_to') Object.assign(edge,{source_field:field,target_field:targetField,separator});
        patch={upsert_edges:[edge]};
      }
      if(await onEdit({patch})) setMode('');
    } catch(e){setError(e instanceof Error ? e.message:'保存失败');}
  }
  return <section className="drawer-section idx-edit"><h3>调整草案</h3>
    <div className="idx-review-actions"><button className="idx-btn" aria-expanded={mode==='node'} disabled={busy} onClick={()=>setMode(mode==='node'?'':'node')}>{node?'编辑节点':'新增节点'}</button>
      <button className="idx-btn" disabled={busy} aria-expanded={mode==='edge'} onClick={()=>{setMode(mode==='edge'?'':'edge');}}>新增连线</button></div>
    {mode && <form onSubmit={e=>{e.preventDefault();void save();}}><fieldset disabled={busy}>
      {mode==='node'?<>
        {!node && <label>节点类型<select value={kind} onChange={e=>setKind(e.target.value as IndexNode['kind'])}><option value="domain">领域</option><option value="concept">业务概念</option><option value="metric">指标</option><option value="artifact">资料载体</option></select></label>}
        <label>名称<input required value={name} onChange={e=>setName(e.target.value)} /></label>
        <label>归属<select value={parent} onChange={e=>setParent(e.target.value)}><option value="">全局</option>
          {graph.nodes.filter(n=>(kind==='artifact' ? ['domain','concept','metric'].includes(n.kind) : n.kind==='domain') && n.id!==node?.id).map(n=><option key={n.id} value={n.id}>{n.label}</option>)}</select></label>
        <label>一句话摘要<textarea value={desc} onChange={e=>setDesc(e.target.value)} /></label>
        {kind==='artifact' && <><label>载体类型<select value={artifactType} onChange={e=>setArtifactType(e.target.value)}><option value="document">业务文档</option><option value="event_spec">埋点规范</option><option value="config_table">配置表</option><option value="tool">工具</option><option value="skill">技能</option></select></label>
          <label>读取策略<input value={readStrategy} onChange={e=>setReadStrategy(e.target.value)}/></label><label>资料库<input required value={repo} onChange={e=>setRepo(e.target.value)} /></label>
          <label>资料路径<input required value={path} onChange={e=>setPath(e.target.value)} /></label><label>逻辑锚点<input value={anchor} onChange={e=>setAnchor(e.target.value)} /></label></>}
        {rules.map((r,i)=><div key={i}><label>公理类别<select value={r.kind} onChange={e=>setRules(rules.map((a,j)=>j===i?{...a,kind:e.target.value}:a))}>{Object.entries(axiomNames).map(([k,v])=><option key={k} value={k}>{v}</option>)}</select></label>
          <label>排查规则<textarea required value={r.text} onChange={e=>setRules(rules.map((a,j)=>i===j?{...a,text:e.target.value}:a))}/></label><button className="idx-text-btn" type="button" onClick={()=>setRules(rules.filter((_,j)=>j!==i))}>移除此规则</button></div>)}
        <button type="button" className="idx-btn" onClick={()=>setRules([...rules,{kind:'pitfall',text:''}])}>添加公理</button>
        <details><summary>结构化属性</summary><textarea aria-label="结构化属性 JSON" rows={6} value={properties} onChange={e=>setProperties(e.target.value)}/></details>
      </>:<>
        <label>起点<select required value={source} onChange={e=>setSource(e.target.value)}>{options}</select></label>
        <label>关系<select value={relation} onChange={e=>setRelation(e.target.value)}>{Object.entries(relationNames).map(([k,v])=><option key={k} value={k}>{v}</option>)}</select></label>
        <label>终点<select required value={dest} onChange={e=>setDest(e.target.value)}><option value="">选择节点</option>{options}</select></label>
        <label>证据锚点<input value={edgeAnchor} onChange={e=>setEdgeAnchor(e.target.value)} /></label>
        {relation==='foreign_key_to' && <><label>引用字段<input value={field} onChange={e=>setField(e.target.value)} /></label><label>目标字段<input value={targetField} onChange={e=>setTargetField(e.target.value)} /></label><label>多值分隔符<input value={separator} onChange={e=>setSeparator(e.target.value)}/></label></>}
      </>}
      <button type="submit" className="idx-btn idx-primary">{busy?'正在保存…':'保存到草案'}</button>
    </fieldset></form>}
    {node && <details><summary>移除节点或连线</summary>
      {graph.edges.filter(e=>e.from===node.id || e.to===node.id).map(e=><button type="button" key={e.id} className="idx-text-btn" disabled={busy} onClick={()=>void onEdit({patch:{remove_edge_ids:[e.id]}})}>移除 {graph.nodes.find(n=>n.id===e.from)?.label} → {graph.nodes.find(n=>n.id===e.to)?.label} · {relationNames[e.relation] ?? e.relation}</button>)}
      <button type="button" className="idx-btn" disabled={busy} onClick={()=>void onEdit({patch:{remove_node_ids:[node.id],remove_edge_ids:graph.edges.filter(e=>e.from===node.id||e.to===node.id).map(e=>e.id)}})}>从草案移除此节点</button></details>}
    {error && <p role="alert" className="idx-error">{error}</p>}
  </section>;
}

import {useState} from 'react';
import type {Proposal} from '../IndexApp';
import {explain,request} from '../api/client';
type Result={status:string;proposal?:Proposal;message:string};
export function SourceIntake({endpoint,onSubmitted}:{endpoint:string;onSubmitted:(p:Proposal)=>void}) {
  const [mode,setMode]=useState('url'),[url,setUrl]=useState(''),[file,setFile]=useState<File>(),[repo,setRepo]=useState(''),[path,setPath]=useState('');
  const [busy,setBusy]=useState(false),[message,setMessage]=useState('');
  async function ingest(){
    setBusy(true);setMessage('');
    try{
      let result:Result;
      if(mode==='file' && file) {
        const data=new FormData(); data.append('file',file);data.append('repository',repo.trim());data.append('path',path.trim());
        const response=await fetch(endpoint,{method:'POST',body:data}),json=await response.json();
        if(!response.ok) throw new Error(json.error ?? '文件解析失败。');
        result=json;
      } else result=await request<Result>(endpoint,mode==='url'?{url:url.trim()}:{source:'tracking'});
      if(result.proposal)onSubmitted(result.proposal);else setMessage(result.message);
    }catch(e){setMessage(explain(e));}finally{setBusy(false);}
  }
  return <details className="idx-intake"><summary>导入业务资料</summary><form onSubmit={e=>{e.preventDefault();void ingest();}} onDragOver={e=>e.preventDefault()} onDrop={e=>{
    e.preventDefault();if(!busy && e.dataTransfer.files.length){setMode('file');setFile(e.dataTransfer.files[0]);}
  }}><fieldset disabled={busy}><label>来源<select value={mode} onChange={e=>{setMode(e.target.value);setMessage('');}}><option value="url">飞书或配置下载链接</option><option value="file">上传配置表</option><option value="tracking">同步埋点版本</option></select></label>
    {mode==='url' && <label>资料链接<input required type="url" value={url} onChange={e=>setUrl(e.target.value)}/></label>}
    {mode==='file' && <><label>配置文件<input type="file" onChange={e=>setFile(e.target.files?.[0])} /></label>{file && <small>{file.name}</small>}<label>原资料库<input required value={repo} onChange={e=>setRepo(e.target.value)} /></label><label>原相对路径<input required value={path} onChange={e=>setPath(e.target.value)}/></label></>}
    <button type="submit" disabled={mode==='file' && !file} className="idx-btn">{busy?'正在解析…':'读取并比对'}</button>{message && <p className="idx-muted" role="status">{message}</p>}</fieldset></form></details>;
}

import type { Edge, Graph, Node } from '../types';

export type Cut = 'structure' | 'causal';
export const planeNames = {structure:['资料与工具','业务概念与指标','领域'],causal:['底层资料','稳态玩法','版本与活动干预','分析指标']};
export function plane(node:Node, cut:Cut):number {
  if(cut === 'structure') return node.node_type === 'Domain' ? 2 : node.node_type === 'Artifact' ? 0 : 1;
  if(node.node_type === 'Artifact') return 0;
  if(node.node_type === 'Metric') return 3;
  return node.properties.cognitive_layer === 'S2' ? 2 : 1;
}

export function projectIndex(graph:Graph, scope:string, depth:boolean, selected:string, cut:Cut='structure') {
  const byId = new Map(graph.nodes.map(n=>[n.id,n]));
  const owner = byId.get(scope);
  const shown = new Set<string>(), remote = new Set<string>();
  const memberships:Edge[] = [];
  const artifacts = new Map<string,Set<string>>();
  function attach(root:string, leaf:string) {
    if (!artifacts.has(root)) artifacts.set(root,new Set());
    artifacts.get(root)!.add(leaf);
  }
  for (const n of graph.nodes) if (n.node_type === 'Artifact' && n.domain_id) attach(n.domain_id,n.id);
  for (const e of graph.edges) if (byId.get(e.dst_id)?.node_type === 'Artifact' && byId.get(e.src_id)?.node_type !== 'Artifact') attach(e.src_id,e.dst_id);
  function nearest(id:string, accept:(n:Node)=>boolean):string {
    const seen=new Set<string>();
    for (let n=byId.get(id); n && !seen.has(n.id); n=byId.get(n.domain_id)) {
      if(accept(n)) return n.id;
      seen.add(n.id);
    }
    return '';
  }
  if (!owner) {
    for(const n of graph.nodes) if(!n.domain_id) shown.add(n.id);
  } else if (owner.node_type === 'Domain') {
    for(const n of graph.nodes) if(n.domain_id === scope && n.node_type !== 'Artifact') shown.add(n.id);
    // An artifact-only domain still needs an entry point.
    if(!shown.size) for (const id of artifacts.get(scope) ?? []) shown.add(id);
  } else {
    for(const id of artifacts.get(scope) ?? []) shown.add(id);
    for(const n of graph.nodes) if(n.domain_id === scope) shown.add(n.id);
    const leaves=[...shown];
    for(const e of graph.edges) if(e.predicate === 'foreign_key_to' && leaves.includes(e.src_id)) shown.add(e.dst_id);
  }
  if(depth) {
    if(owner) {
      shown.add(scope);
      if(owner.domain_id) shown.add(owner.domain_id);
      for(const id of [...shown]) if(id!==scope && byId.get(id)?.node_type !== 'Domain')
        for(const leaf of artifacts.get(id) ?? []) shown.add(leaf);
    } else {
      // Overview cross section follows the selected domain (or the first populated one).
      const branch = nearest(selected,n=>!n.domain_id) || graph.nodes.find(n=>shown.has(n.id) && graph.nodes.some(c=>c.domain_id===n.id))?.id;
      for(const n of graph.nodes) if(branch && n.domain_id===branch && n.node_type!=='Artifact'){
        shown.add(n.id);
        for(const leaf of artifacts.get(n.id) ?? []) shown.add(leaf);
      }
    }
    // Include the actual adjacent concepts in a cut so a cross-domain dependency
    // lands on its business layer rather than on its domain's portal.
    const anchors=new Set([...shown].filter(id=>['Metric','Concept'].includes(byId.get(id)?.node_type ?? '')));
    for (const e of graph.edges) {
      if(anchors.has(e.src_id)) shown.add(e.dst_id);
      if(anchors.has(e.dst_id)) shown.add(e.src_id);
    }
    if(cut==='causal') for(const id of [...shown]) {
      if(byId.get(id)?.node_type==='Domain') shown.delete(id);
    }
    for(const id of shown) {
      const n=byId.get(id);
      if(n && shown.has(n.domain_id) && n.node_type !== 'Artifact') memberships.push({
        id:'@parent/'+id, src_id:n.domain_id,dst_id:id,predicate:'membership',label_zh:'归属',cost:0,metadata:{},
      });
    }
  }
  const projected:Edge[]=[];
  const aggregates=new Map<string,Edge>();
  for (const e of graph.edges) {
    if(shown.has(e.src_id) && shown.has(e.dst_id)) { projected.push(e); continue; }
    if(depth && cut==='causal') continue;
    let from='',to='';
    if(!owner) {
      from=nearest(e.src_id,n=>shown.has(n.id));to=nearest(e.dst_id,n=>shown.has(n.id));
    } else if(owner.node_type==='Domain'){
      const local=(id:string)=>nearest(id,n=>shown.has(n.id) && n.id!==scope && n.domain_id===scope);
      from=local(e.src_id);to=local(e.dst_id);
      if(!from && !to) continue;
      const foreign=(id:string)=>nearest(id,n=>n.node_type==='Domain' && n.id!==scope && nearest(n.id,v=>v.id===scope)==='');
      if(!from) {from=foreign(e.src_id); if(from) remote.add(from);}
      if(!to) {to=foreign(e.dst_id); if(to) remote.add(to);}
    }
    if(!from || !to || from===to) continue;
    const key=JSON.stringify([from,to]);
    let edge=aggregates.get(key);
    if(!edge){edge={id:'@group/'+key,src_id:from,dst_id:to,predicate:'aggregate',label_zh:'',cost:0,metadata:{members:[]}};aggregates.set(key,edge);}
    const members=edge.metadata.members as string[];
    members.push(e.id!);edge.label_zh=members.length+' 条关系';
  }
  return {nodes:graph.nodes.filter(n=>shown.has(n.id)||remote.has(n.id)),edges:[...projected,...aggregates.values(),...memberships],
    remote,artifacts,enterable:new Set([...graph.nodes.filter(n=>n.node_type!=='Artifact' && (n.node_type==='Domain' || artifacts.has(n.id) || graph.nodes.some(c=>c.domain_id===n.id))).map(n=>n.id)])};
}

type Area={x:number;y:number;width:number;height:number};
type SliceNode={id:string;x:number;y:number;model:Node};

// Allocate height by occupied rows, including only a narrow caption strip for
// empty levels. All coordinates here are home-screen pixels, before pan/zoom.
export function layoutSlices(nodes:SliceNode[],area:Area,cut:Cut,widths:Map<string,number>){
  const groups=planeNames[cut].map((_,i)=>nodes.filter(n=>plane(n.model,cut)===i)
    .sort((a,b)=>a.x-b.x || a.y-b.y || a.id.localeCompare(b.id)));
  const padding=Math.min(22,area.width*.055);
  const usable=Math.max(40,area.width-2*padding-30);
  const cellWidth=Math.max(96,Math.min(152,Math.max(...nodes.map(n=>widths.get(n.id) ?? 90),90)+8));
  const usableHeight=Math.max(80,area.height-12);
  let scale=1,cols=1,rows:number[]=[],natural=0;
  for(let attempt=0;attempt<22;attempt++){
    cols=Math.max(1,Math.floor(usable/(cellWidth*scale)));
    rows=groups.map(group=>Math.ceil(group.length/cols));
    natural=rows.reduce((sum,row)=>sum+21+row*84*scale,0)+8*(groups.length-1);
    if(natural<=usableHeight || scale<=.44)break;
    scale*=.94;
  }
  const ratio=Math.min(1,usableHeight/natural);
  const gap=(8+Math.min(18,Math.max(0,usableHeight-natural)/Math.max(1,groups.length-1)))*ratio;
  const extent=rows.reduce((sum,r)=>sum+(21+r*84*scale)*ratio,0)+gap*(groups.length-1);
  let y=area.y+(area.height-extent)/2;
  const points=new Map<string,{x:number;y:number}>();
  const plates:Area[]=Array(groups.length);
  for(let i=groups.length-1;i>=0;i--){
    const h=(21+rows[i]*84*scale)*ratio;
    plates[i]={x:area.x+padding,y,width:area.width-2*padding,height:h};
    const group=groups[i];
    for(let j=0;j<group.length;j++){
      const row=Math.floor(j/cols), count=Math.min(cols,group.length-row*cols);
      const step=Math.min(usable/count,cellWidth*scale*1.20);
      points.set(group[j].id,{x:area.x+area.width/2+(j%cols-(count-1)/2)*step,y:y+(19+(row+.43)*84*scale)*ratio});
    }
    y+=h+gap;
  }
  return {points,plates,scale:scale*ratio};
}

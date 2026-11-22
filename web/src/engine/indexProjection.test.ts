import {deepStrictEqual,equal} from 'node:assert/strict';
import {it} from 'node:test';
import {adaptIndexGraph} from './indexPortal.ts';
import {projectIndex,plane} from './indexProjection.ts';

const graph=adaptIndexGraph({schema_version:2,revision:1,nodes:[
  {id:'bi',label:'分析',kind:'domain'},{id:'ops',label:'运营',kind:'domain'},
  {id:'dau',label:'日活',kind:'metric',parent_id:'bi'},
  {id:'ab',label:'实验',kind:'concept',parent_id:'ops',properties:{cognitive_layer:'S2'}},
  {id:'events',label:'字典',kind:'artifact',parent_id:'ops'},
],edges:[
  {id:'a',from:'dau',to:'events',relation:'depends_on'},
  {id:'b',from:'ab',to:'events',relation:'defined_in'},
  {id:'c',from:'dau',to:'ab',relation:'impacted_by'},
]});
it('collapses cross-domain routes into a counted direction and projects one physical source into two leaf views',()=>{
  const top=projectIndex(graph,'',false,'');
  deepStrictEqual(top.nodes.map(n=>n.id),['bi','ops']);
  deepStrictEqual(top.edges.map(e=>[e.src_id,e.dst_id,e.label_zh]),[['bi','ops','2 条关系']]);
  const room=projectIndex(graph,'bi',false,'');
  deepStrictEqual([...room.remote],['ops']);equal(room.artifacts.get('dau')?.size,1);
  for(const id of ['dau','ab']) deepStrictEqual(projectIndex(graph,id,false,'').nodes.map(n=>n.id),['events']);
});
it('causal cut preserves semantic edges and displays their actual business layers',()=>{
  const cut=projectIndex(graph,'bi',true,'','causal');
  deepStrictEqual(cut.nodes.map(n=>[n.id,plane(n,'causal')]),[['dau',3],['ab',2],['events',0]]);
  deepStrictEqual(cut.edges.map(e=>e.id),['a','b','c']);
});

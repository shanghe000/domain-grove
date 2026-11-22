import { equal, ok } from 'node:assert/strict';
import { it } from 'node:test';
import { fitNodeName } from './portalNames.ts';
import type { Node } from '../types.ts';

const node: Node = { id: 'doc', label: '跨平台成功支付收入及留存口径', domain_id: '', node_type: 'Artifact', status: 'active', properties: {}, axioms: [] };
function metrics() {
  return { font: '', textBaseline: 'top' as CanvasTextBaseline, measureText(text: string) {
    const size = Number(this.font.match(/([\d.]+)px/)![1]);
    return { width: Array.from(text).length * size, actualBoundingBoxAscent: size * 0.8, actualBoundingBoxDescent: size * 0.2 } as TextMetrics;
  } };
}

it('fits a dense node name at a readable size within the geometry used by the router', () => {
  const name = fitNodeName(metrics(), node, 146, 0.5);
  ok(name.font.includes('10px'));
  ok(name.text.endsWith('…'));
  ok(name.width <= 73);
  ok(name.offset + name.height <= 28 * 0.5);
});

it('restores the full label when zooming into its allotted space', () => {
  const name = fitNodeName(metrics(), node, 146, 2);
  equal(name.text, node.label);
  ok(name.font.includes('12px'));
});

it('drops a caption across another node while preserving the first of two label positions', async () => {
  const { visibleOrbitNames } = await import('./portalNames.ts');
  const name = {text:'资料',font:'12px sans',width:48,height:12,ascent:9,offset:5};
  const result = visibleOrbitNames([
    {id:'a', x:40, y:50,radius:7,name},
    {id:'b', x:73, y:50,radius:7,name},
    {id:'c', x:49, y:32,radius:7,name},
  ], {x:0,y:0,width:150,height:100});
  equal([...result].join(','),'a');
});

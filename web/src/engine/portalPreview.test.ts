import { deepStrictEqual, equal, ok } from 'node:assert/strict';
import { it } from 'node:test';
import { layoutPortalPreview } from './portalPreview.ts';
import { nodeFootprint } from './portalLayout.ts';
import type { BudgetContext } from './bfsBudget.ts';
import type { Node } from '../types.ts';

function node(id: string, parent = '', domain = false): Node {
  return { id, label: id, domain_id: parent, node_type: domain ? 'Domain' : 'Concept', status: 'active', properties: {}, axioms: [] };
}
const seed = node('seed'), portal = node('remote', '', true);
const visible = [{ model: seed, x: 170, y: 130 }, { model: portal, x: 490, y: 200 }];
const area = { x: 32, y: 40, width: 600, height: 360 };
function context(nodes: Node[]): BudgetContext {
  return { seed_node_id: seed.id, budget: 2.5, subgraph: { nodes: [seed, ...nodes], edges: nodes.map(n => ({
    src_id: seed.id, dst_id: n.id, predicate: 'depends_on', cost: 1, label_zh: '引用', metadata: {},
  })) }, artifacts: [], frontier_nodes: [] };
}
function checkGaps(placed: typeof visible) {
  for (const a of placed) for (const b of placed) {
    if (a.model.id >= b.model.id) continue;
    const aa = nodeFootprint(a.model, 124), bb = nodeFootprint(b.model, 124);
    ok(Math.abs(a.x - b.x) >= aa.halfWidth + bb.halfWidth + 11.9 ||
      b.y - bb.top >= a.y + aa.bottom + 11.9 || a.y - aa.top >= b.y + bb.bottom + 11.9);
  }
}

it('keeps a cross-domain preview near its linked nodes with room for names', () => {
  const ghosts = [node('doc-a', portal.id), node('doc-b', portal.id)];
  const graph = new Map([seed, portal, ...ghosts].map(n => [n.id, n]));
  const preview = layoutPortalPreview(context(ghosts), visible, graph, '', area, () => 124);
  equal(preview.size, 2);
  checkGaps([...visible, ...preview.values()]);
  for (const p of preview.values()) {
    ok(p.x > 170 && p.x < 495 && p.y > 90 && p.y < 300);
  }
  deepStrictEqual(preview, layoutPortalPreview(context([...ghosts].reverse()), visible, graph, '', area, () => 124));
});

it('keeps all densely connected previews distinct and includes a missing remote parent once', () => {
  const children = Array.from({ length: 24 }, (_, i) => node('child-' + i, portal.id));
  const graph = new Map([seed, portal, ...children].map(n => [n.id, n]));
  const preview = layoutPortalPreview(context(children), visible.slice(0, 1), graph, '', { x: 0, y: 0, width: 290, height: 190 }, () => 124);
  equal(preview.size, children.length + 1);
  ok(preview.has(portal.id));
  ok(!preview.has(seed.id));
  checkGaps([...visible.slice(0, 1), ...preview.values()]);
});

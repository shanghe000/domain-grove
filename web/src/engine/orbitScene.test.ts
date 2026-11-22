import assert from 'node:assert/strict';
import { test } from 'node:test';
import type { Node, NodeType } from '../types.ts';
import { buildOrbitScene, projectOrbitScene } from './orbitScene.ts';

const area = { x: 25, y: 80, width: 980, height: 600 };
const camera = { tilt: 1.08, yaw: .32 };
function node(id: string, node_type: NodeType, order: number) {
  const model = { id, node_type, properties: {} } as Node;
  return { id, x: order, y: 0, model };
}
const nodes = [node('d', 'Domain', 0), node('m', 'Metric', 1), ...Array.from({length: 8}, (_, i) => node('a'+i, 'Artifact', i))];

test('nodes and their supporting surfaces use perspective and positive depth towards the observer', () => {
  const scene = buildOrbitScene(nodes, area, 'structure', new Map());
  const f = projectOrbitScene(scene, area, camera);
  assert.ok(f.plates[2].center.y < f.plates[0].center.y);
  const [backLeft, backRight, frontRight, frontLeft] = f.plates[0].corners;
  assert.ok(frontLeft.depth > backLeft.depth && frontRight.depth > backRight.depth);
  const length = (p: typeof backLeft, q: typeof backLeft) => Math.hypot(q.x - p.x, q.y - p.y);
  assert.ok(length(frontLeft,frontRight) > length(backLeft,backRight));
  const sorted = [...f.points.values()].sort((a, b) => a.depth - b.depth);
  assert.ok(sorted.at(-1)!.scale > sorted[0].scale);
  for (const point of f.points.values()) assert.ok(point.y < point.foot.y);
  const reversed = projectOrbitScene(scene, area, {...camera, yaw: -camera.yaw});
  assert.ok(Math.sign(backLeft.y - backRight.y) !== Math.sign(reversed.plates[0].corners[0].y - reversed.plates[0].corners[1].y));
});

test('both cuts fit narrow, wide and shallow canvases throughout a rotation', () => {
  for (const width of [260, 860, 1450]) for (const height of [240, 680]) {
    const view = { ...area, width, height };
    for (const cut of ['causal', 'structure'] as const) {
      const group = cut === 'causal' ? nodes.filter(n=>n.model.node_type!=='Domain') : nodes;
      const scene = buildOrbitScene(group, view, cut, new Map());
      for (const yaw of [-1.1, -.38, 0, .5, 1.1]) {
        const f = projectOrbitScene(scene, view, { ...camera, yaw });
        assert.equal(f.points.size, group.length);
        const corners = f.plates.flatMap(p => [...p.corners,...p.lower]);
        for (const p of [...corners, ...f.points.values()]) {
          assert.ok(p.x >= view.x && p.x <= view.x + width && p.y >= view.y && p.y <= view.y + height);
        }
      }
    }
  }
});

test('a dense portrait stack fills the available height and keeps neighbouring nodes apart', () => {
  const portrait = { x: 15, y: 178, width: 350, height: 425 };
  const dense = [node('domain', 'Domain', 0),
    ...Array.from({ length: 14 }, (_, i) => node('m' + i, 'Metric', i)),
    ...Array.from({ length: 8 }, (_, i) => node('leaf' + i, 'Artifact', i)),
  ];
  const widths = new Map(dense.map(n => [n.id, 140]));
  for (const cut of ['structure', 'causal'] as const) {
    const visible = cut === 'structure' ? dense : dense.filter(n => n.model.node_type !== 'Domain');
    const frame = projectOrbitScene(buildOrbitScene(visible, portrait, cut, widths), portrait, camera);
    const all = frame.plates.flatMap(p => p.corners);
    const span = Math.max(...all.map(p => p.y)) - Math.min(...all.map(p => p.y));
    assert.ok(span > portrait.height * .79, 'use the available height for a tall stack');
    const p = [...frame.points.values()];
    for (let i = 0; i < p.length; i++) for (const q of p.slice(i + 1)) {
      assert.ok(Math.hypot(p[i].x - q.x, p[i].y - q.y) > 23, 'separate pointer targets');
    }
  }
});

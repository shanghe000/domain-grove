import { deepStrictEqual, equal, ok } from 'node:assert/strict';
import { it } from 'node:test';

import { placeCaptions, pointOnEdge, routeEdges } from './portalRoutes.ts';
import type { RouteEdge, RouteNode } from './portalRoutes.ts';

const surface = { width: 660, height: 410 };
function node(x: number, y: number, labelWidth = 62): RouteNode {
  return { x, y, radius: 20, ring: 25, labelWidth, labelOffset: 10, labelHeight: 16 };
}
const ab = { key: 'ab', source: 'a', target: 'b' };
const caption = (key: string, hot = false) => ({ key, width: 72, clauseWidth: 0, hot });

it('retains a straight connection and a centered caption in open space', () => {
  const nodes = new Map([['a', node(130, 160)], ['b', node(500, 160)]]);
  const paths = routeEdges(nodes, [ab], surface);
  const path = paths.get('ab')!;
  equal(path.start.y, 160);
  equal(path.end.y, 160);
  equal(path.control.y, 160);
  ok(path.start.x > 155 && path.end.x < 475);
  deepStrictEqual(placeCaptions(nodes, paths, [caption('ab')], surface).get('ab'), pointOnEdge(path, 0.5));
});

it('detours around an intervening circle and around a label even when its circle is clear', () => {
  for (const obstacleY of [175, 136]) {
    const nodes = new Map([['a', node(100, 175)], ['b', node(520, 175)], ['middle', node(310, obstacleY, 108)]]);
    const path = routeEdges(nodes, [ab], surface).get('ab')!;
    for (let i = 0; i <= 100; i++) {
      const p = pointOnEdge(path, i / 100);
      ok(Math.hypot(p.x - 310, p.y - obstacleY) > 25);
      ok(Math.abs(p.x - 310) > 54 || p.y < obstacleY + 28 || p.y > obstacleY + 48);
    }
  }
});

it('keeps stable, separate lanes for parallel and reciprocal relations', () => {
  const nodes = new Map([['a', node(120, 180)], ['b', node(480, 180)]]);
  const edges: RouteEdge[] = [ab, { ...ab, key: 'aa' }, { key: 'ba', source: 'b', target: 'a' }];
  const paths = routeEdges(nodes, edges, surface);
  deepStrictEqual(paths, routeEdges(nodes, [...edges].reverse(), surface));
  const midpoints = [...paths.values()].map(path => pointOnEdge(path, 0.5).y).sort((a, b) => a - b);
  ok(midpoints[1] - midpoints[0] > 20 && midpoints[2] - midpoints[1] > 20);
  ok(paths.get('ba')!.start.x > paths.get('ba')!.end.x);
});

it('separates intersecting relation captions and gives the highlighted relation the midpoint', () => {
  const nodes = new Map([['a', node(100, 150)], ['b', node(510, 150)]]);
  const path = routeEdges(nodes, [ab], surface).get('ab')!;
  const paths = new Map([['a', path], ['b', path], ['c', path]]);
  const captions = [caption('a'), caption('b'), caption('c', true)];
  const result = placeCaptions(nodes, paths, captions, surface);
  deepStrictEqual(result.get('c'), pointOnEdge(path, 0.5));
  for (const a of captions) for (const b of captions) {
    if (a.key >= b.key) continue;
    const p = result.get(a.key)!, q = result.get(b.key)!;
    ok(Math.abs(p.x - q.x) >= 72 || Math.abs(p.y - q.y) >= 20);
  }
});

it('keeps self relations finite and drops links without visible endpoints', () => {
  const nodes = new Map([['a', node(120, 120)]]);
  const paths = routeEdges(nodes, [ab, { key: 'loop', source: 'a', target: 'a' }], surface);
  equal(paths.size, 1);
  const path = paths.get('loop')!;
  ok(path.control.y < path.start.y && path.start.x < path.end.x);
});

import { deepStrictEqual, equal, ok } from 'node:assert/strict';
import { it } from 'node:test';
import type { Node, NodeType } from '../types.ts';
import { layoutPortalRoom, nodeFootprint } from './portalLayout.ts';

function model(id: string, node_type: NodeType = 'Concept'): Node {
  return { id, node_type, label: id, domain_id: '', status: 'active', properties: {}, axioms: [] };
}
const area = { x: 32, y: 94, width: 680, height: 440 };
const link = (src_id: string, dst_id: string) => ({ src_id, dst_id });
const coords = (result: ReturnType<typeof layoutPortalRoom>) =>
  result.positions.map(({ id, x, y }) => ({ id, x, y }));

function assertClear(result: ReturnType<typeof layoutPortalRoom>, labels: Map<string, number>, frame = area) {
  const { positions, bounds, fitScale } = result;
  ok(Number.isFinite(fitScale) && fitScale > 0 && fitScale <= 1);
  ok(bounds.width * fitScale <= frame.width + 0.01);
  ok(bounds.height * fitScale <= frame.height + 0.01);
  ok(Math.abs(bounds.x + bounds.width / 2 - frame.x - frame.width / 2) < 0.01);
  ok(Math.abs(bounds.y + bounds.height / 2 - frame.y - frame.height / 2) < 0.01);
  for (let i = 0; i < positions.length; i++) {
    const a = positions[i];
    const footprint = nodeFootprint(a.model, labels.get(a.id)!);
    for (let j = 0; j < i; j++) {
      const b = positions[j];
      const other = nodeFootprint(b.model, labels.get(b.id)!);
      const gap = Math.max(Math.abs(a.x - b.x) - footprint.halfWidth - other.halfWidth,
        b.y - other.top - a.y - footprint.bottom, a.y - footprint.top - b.y - other.bottom);
      ok(gap >= 9.9, `${a.id} and ${b.id} overlap or touch (${gap})`);
    }
  }
}

it('centers small rooms within their usable area and preserves actual circle and title clearance', () => {
  const empty = layoutPortalRoom([], [], new Map(), area);
  equal(empty.positions.length, 0);
  equal(empty.bounds.x, area.x + area.width / 2);
  const nodes = Array.from({ length: 7 }, (_, i) => model('domain-' + i, 'Domain'));
  const labels = new Map(nodes.map((node, i) => [node.id, i % 2 ? 151 : 64]));
  const solo = layoutPortalRoom(nodes.slice(0, 1), [], labels, area);
  assertClear(solo, labels);
  const room = layoutPortalRoom(nodes, [], labels, area);
  assertClear(room, labels);
  equal(room.fitScale, 1);
  ok(room.bounds.width < 640 && room.bounds.height < 430);
});

it('places semantic neighbors together independently of arrival order, relation direction, or multiplicity', () => {
  const nodes = Array.from({ length: 8 }, (_, i) => model(String(i)));
  const labels = new Map(nodes.map(n => [n.id, 96]));
  const edges = [link('0', '1'), link('0', '2'), link('0', '3'), link('2', '3'), link('4', '5')];
  const room = layoutPortalRoom(nodes, edges, labels, area);
  assertClear(room, labels);
  const shuffled = layoutPortalRoom([...nodes].reverse(), [
    ...[...edges].reverse(), link('1', '0'), link('2', '2'), link('2', 'missing'),
  ], labels, area);
  deepStrictEqual(coords(room), coords(shuffled));
  const byId = new Map(room.positions.map(node => [node.id, node]));
  const distance = (a: string, b: string) =>
    Math.hypot(byId.get(a)!.x - byId.get(b)!.x, byId.get(a)!.y - byId.get(b)!.y);
  ok(distance('4', '5') < distance('0', '5'));
});

it('folds long chains and fits crowded long-label rooms at both mobile and desktop widths', () => {
  const nodes = Array.from({ length: 48 }, (_, i) => model(String(i), i % 7 === 0 ? 'Domain' : 'Metric'));
  const labels = new Map(nodes.map(n => [n.id, 146]));
  const chain = nodes.slice(1).map((node, i) => link(nodes[i].id, node.id));
  const hub = nodes.slice(1).flatMap((node, i) => {
    const links = [link(nodes[0].id, node.id)];
    if (i % 2 && i > 3) links.push(link(nodes[i - 2].id, node.id));
    return links;
  });
  for (const edges of [chain, hub]) {
    for (const frame of [area, { x: 12, y: 64, width: 360, height: 560 }]) {
      const room = layoutPortalRoom(nodes, edges, labels, frame);
      assertClear(room, labels, frame);
      ok(room.fitScale > 0.30);
      ok(room.bounds.width / room.bounds.height < 2.6 && room.bounds.height / room.bounds.width < 2.6);
    }
  }
});

it('keeps a domain directory aligned even when descendant relationships provide hidden layout links', () => {
  const nodes = Array.from({ length: 7 }, (_, i) => model('domain-' + i, 'Domain'));
  const edges = nodes.slice(1).map(node => link(nodes[0].id, node.id));
  const frame = { x: 24, y: 82, width: 1018, height: 640 };
  const labels = new Map(nodes.map(n => [n.id, 136]));
  const room = layoutPortalRoom(nodes, edges, labels, frame, undefined, true);
  assertClear(room, labels, frame);
  equal(new Set(room.positions.map(node => node.y)).size, 2);
  ok(room.bounds.width / frame.width > 0.68);
  ok(room.bounds.width / frame.width < 0.85);
});

it('gives multiple large connected groups a share of the available long axis before auto-fit', () => {
  const nodes = Array.from({ length: 42 }, (_, i) => model('m.' + i, i % 3 === 0 ? 'Concept' : 'Metric'));
  const edges = nodes.flatMap((node, i) => {
    const offsets = i % 3 === 0 ? [1, 12] : [i % 3 + 1];
    return offsets.filter(d => i + d < nodes.length).map(d => link(node.id, nodes[i + d].id));
  });
  const frame = { x: 24, y: 110, width: 840, height: 551 };
  const labels = new Map(nodes.map(n => [n.id, 146]));
  const room = layoutPortalRoom(nodes, edges, labels, frame);
  assertClear(room, labels, frame);
  ok(room.fitScale > 0.65);
  ok(room.bounds.height * room.fitScale / frame.height > 0.9);
});

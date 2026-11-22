import type { Node } from '../types.ts';
import type { BudgetContext } from './bfsBudget.ts';
import { nodeFootprint } from './portalLayout.ts';

type Point = { x: number; y: number };
type Bounds = { left: number; top: number; right: number; bottom: number };
type Area = { x: number; y: number; width: number; height: number };
type Placed = Point & { model: Node };

function bounds(point: Point, size: ReturnType<typeof nodeFootprint>): Bounds {
  return { left: point.x - size.halfWidth, right: point.x + size.halfWidth, top: point.y - size.top, bottom: point.y + size.bottom };
}

// Work in room coordinates. Zoom and pan then apply exactly the same transform to fixed and preview nodes.
export function layoutPortalPreview(context: BudgetContext, visible: readonly Placed[], models: ReadonlyMap<string, Node>,
  domain: string, area: Area, labelWidth: (model: Node) => number): Map<string, Placed> {
  const result = new Map<string, Placed>();
  const placed = new Map(visible.map(node => [node.model.id, node]));
  const pending = new Map<string, Node>();
  for (const model of context.subgraph.nodes) {
    if (placed.has(model.id)) continue;
    pending.set(model.id, model);
    const parent = models.get(model.domain_id);
    if (parent && parent.id !== domain && !placed.has(parent.id)) pending.set(parent.id, parent);
  }
  const neighbors = new Map([...pending.keys()].map(id => [id, new Set<string>()]));
  const connect = (a: string, b: string) => { neighbors.get(a)?.add(b); neighbors.get(b)?.add(a); };
  for (const edge of context.subgraph.edges) connect(edge.src_id, edge.dst_id);
  for (const item of pending.values()) connect(item.id, item.domain_id);
  const occupied = visible.map(node => bounds(node, nodeFootprint(node.model, labelWidth(node.model))));
  const center = { x: area.x + area.width / 2, y: area.y + area.height / 2 };
  const fallback = placed.get(context.seed_node_id) ?? center;

  while (pending.size) {
    // Place a node with known neighbours first so a cross-domain branch grows from its actual endpoints.
    let chosen: Node | undefined;
    let anchors: Placed[] = [];
    for (const node of pending.values()) {
      const adjacent = [...neighbors.get(node.id)!].flatMap(id => placed.get(id) ?? []);
      if (!chosen || adjacent.length > anchors.length || (adjacent.length === anchors.length && node.id < chosen.id)) {
        chosen = node; anchors = adjacent;
      }
    }
    const model = chosen!;
    pending.delete(model.id);
    const origin = anchors.length ? {
      x: anchors.reduce((sum, p) => sum + p.x, 0) / anchors.length,
      y: anchors.reduce((sum, p) => sum + p.y, 0) / anchors.length,
    } : fallback;
    const size = nodeFootprint(model, labelWidth(model));
    let best: Point | undefined;
    let bestCost = Infinity;
    // An empty corridor near the anchor wins over a distant fixed slot. Allow overflow when the room fills.
    const inward = Math.atan2(center.y - origin.y, center.x - origin.x);
    for (let ring = 0; ring < 18; ring++) {
      const reach = ring * 36, count = Math.max(1, ring * 5);
      if (reach > bestCost) break;
      for (let i = 0; i < count; i++) {
        const angle = inward + (i + (ring % 2) * 0.5) * Math.PI * 2 / count;
        const p = { x: origin.x + reach * Math.cos(angle), y: origin.y + reach * Math.sin(angle) };
        const box = bounds(p, size);
        if (occupied.some(b => box.left < b.right + 12 && box.right > b.left - 12 && box.top < b.bottom + 12 && box.bottom > b.top - 12)) continue;
        const overflow = Math.max(0, area.x - box.left) + Math.max(0, box.right - area.x - area.width) +
          Math.max(0, area.y - box.top) + Math.max(0, box.bottom - area.y - area.height);
        const cost = reach + overflow * 8 + Math.hypot(p.x - center.x, p.y - center.y) * 0.04;
        if (cost < bestCost) { bestCost = cost; best = p; }
      }
    }
    if (!best) {
      const right = occupied.reduce((max, item) => Math.max(max, item.right), origin.x);
      best = { x: right + size.halfWidth + 12, y: origin.y };
    }
    const item = { ...best, model };
    placed.set(model.id, item);
    result.set(model.id, item);
    occupied.push(bounds(best, size));
  }
  return result;
}

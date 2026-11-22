import { forceCollide, forceLink, forceManyBody, forceSimulation, forceX, forceY } from 'd3-force';
import type { SimulationNodeDatum } from 'd3-force';
import type { Edge, Node, NodeType } from '../types';

export const nodeRadii: Record<NodeType, number> = { Domain: 32, Metric: 22, Concept: 20, Artifact: 14 };
type Area = { x: number; y: number; width: number; height: number };
type LayoutNode = SimulationNodeDatum & {
  id: string; model: Node; x: number; y: number; halfWidth: number; top: number; bottom: number;
};
type Island = { nodes: LayoutNode[]; bounds: Area; contextOnly: boolean };
type Link = { source: LayoutNode; target: LayoutNode };
const compareId = (a: LayoutNode, b: LayoutNode) => a.id < b.id ? -1 : a.id > b.id ? 1 : 0;

export function nodeFootprint(model: Node, labelWidth: number) {
  const domain = model.node_type === 'Domain';
  const radius = nodeRadii[model.node_type];
  const rim = Math.max(22, radius + (domain ? 15 : 9));
  return { halfWidth: Math.max(rim, labelWidth / 2), top: rim, bottom: radius + (domain ? 34 : 28) };
}

function extent(nodes: LayoutNode[]): Area {
  let left = Infinity, right = -Infinity, top = Infinity, bottom = -Infinity;
  for (const node of nodes) {
    left = Math.min(left, node.x - node.halfWidth);
    right = Math.max(right, node.x + node.halfWidth);
    top = Math.min(top, node.y - node.top);
    bottom = Math.max(bottom, node.y + node.bottom);
  }
  return { x: left, y: top, width: right - left, height: bottom - top };
}

// A label sits below its circle, so two circles that clear each other can still hide a name.
function separateLabels(nodes: LayoutNode[]) {
  const gap = 12;
  for (let pass = 0; pass < 12; pass++) {
    let moved = false;
    const ordered = [...nodes].sort((a, b) => a.x - a.halfWidth - (b.x - b.halfWidth));
    for (let i = 0; i < ordered.length; i++) {
      const a = ordered[i];
      for (let j = i + 1; j < ordered.length; j++) {
        const b = ordered[j];
        if (b.x - b.halfWidth >= a.x + a.halfWidth + gap) break;
        const dx = a.halfWidth + b.halfWidth + gap - Math.abs(a.x - b.x);
        const up = a.y + a.bottom + gap - (b.y - b.top);
        const down = b.y + b.bottom + gap - (a.y - a.top);
        if (Math.min(dx, up, down) <= 0.01) continue;
        if (dx <= Math.min(up, down)) {
          const step = Math.sign(b.x - a.x || 1) * (dx / 2 + 0.1);
          a.x -= step; b.x += step;
        } else {
          const step = up < down ? up / 2 + 0.1 : -down / 2 - 0.1;
          a.y -= step; b.y += step;
        }
        moved = true;
      }
    }
    if (!moved) return;
  }

  // Complete any remaining constraints in one direction, without clamping nodes to a viewport edge.
  const ordered = [...nodes].sort((a, b) => a.x - b.x || compareId(a, b));
  for (let j = 1; j < ordered.length; j++) {
    const b = ordered[j];
    for (let i = 0; i < j; i++) {
      const a = ordered[i];
      if (a.y - a.top < b.y + b.bottom + gap && b.y - b.top < a.y + a.bottom + gap) {
        b.x = Math.max(b.x, a.x + a.halfWidth + b.halfWidth + gap);
      }
    }
  }
}

function compactRows(nodes: LayoutNode[], aspect: number, followPath: boolean, crowded: boolean) {
  // Fold long paths back across the next row so consecutive nodes stay adjacent.
  const stepX = nodes.reduce((size, node) => Math.max(size, node.halfWidth * 2), 0) + (crowded ? 24 : 42);
  const stepY = nodes.reduce((size, node) => Math.max(size, node.top + node.bottom), 0) + (crowded ? 32 : 48);
  const columns = Math.min(nodes.length, Math.max(1, Math.round(Math.sqrt(nodes.length * aspect * stepY / stepX))));
  const rows = Math.ceil(nodes.length / columns);
  const rowOrder = followPath ? nodes : [...nodes].sort((a, b) => a.y - b.y || compareId(a, b));
  let first = 0;
  for (let row = 0; row < rows; row++) {
    const count = Math.ceil((nodes.length - first) / (rows - row));
    const items = rowOrder.slice(first, first + count);
    if (!followPath) items.sort((a, b) => a.x - b.x || compareId(a, b));
    items.forEach((node, column) => {
      const along = followPath && row % 2 ? count - 1 - column : column;
      node.x = (along - (count - 1) / 2) * stepX;
      node.y = row * stepY;
    });
    first += count;
  }
}

function arrangeConnected(nodes: LayoutNode[], neighbors: Map<LayoutNode, LayoutNode[]>, aspect: number, crowded: boolean) {
  if (nodes.length === 1) return;
  const end = nodes.find(node => neighbors.get(node)!.length === 1);
  if (end && nodes.length >= 4 && nodes.every(node => neighbors.get(node)!.length <= 2)) {
    const path = [end];
    for (let i = 0; i < path.length; i++) {
      const next = neighbors.get(path[i])!.find(node => node !== path[i - 1]);
      if (next) path.push(next);
    }
    compactRows(path, aspect, true, crowded);
    return;
  }
  const members = new Set(nodes);
  const links: Link[] = [];
  for (const source of nodes) {
    for (const target of neighbors.get(source)!) {
      if (source.id < target.id) links.push({ source, target });
    }
  }

  // Start a hub centrally and give each branch its own sector before relaxing the real links.
  const root = nodes.reduce((best, node) => neighbors.get(node)!.length > neighbors.get(best)!.length ? node : best);
  const queue = [root];
  const children = new Map<LayoutNode, LayoutNode[]>();
  members.delete(root);
  for (let i = 0; i < queue.length; i++) {
    const next = neighbors.get(queue[i])!.filter(node => members.delete(node));
    children.set(queue[i], next);
    queue.push(...next);
  }
  const weight = new Map<LayoutNode, number>();
  for (let i = queue.length - 1; i >= 0; i--) {
    weight.set(queue[i], Math.max(1, children.get(queue[i])!.reduce((sum, node) => sum + weight.get(node)!, 0)));
  }
  const sectors = new Map([[root, { angle: -Math.PI, span: Math.PI * 2, depth: 0 }]]);
  const linkSpacing = 116;
  for (const parent of queue) {
    const sector = sectors.get(parent)!;
    let angle = sector.angle;
    for (const node of children.get(parent)!) {
      const span = sector.span * weight.get(node)! / weight.get(parent)!;
      const depth = sector.depth + 1;
      const bearing = angle + span / 2;
      const reach = linkSpacing * depth;
      node.x = reach * Math.cos(bearing);
      node.y = reach * Math.sin(bearing);
      sectors.set(node, { angle, span, depth });
      angle += span;
    }
  }

  const simulation = forceSimulation(nodes).stop()
    .force('link', forceLink<LayoutNode, Link>(links)
      .distance(({ source, target }) => Math.max(linkSpacing, source.halfWidth + target.halfWidth + 22)))
    .force('charge', forceManyBody().strength(-190).distanceMax(350))
    .force('collision', forceCollide<LayoutNode>(node => Math.max(node.halfWidth * 0.76, node.bottom * 0.72) + 12))
    .force('x', forceX(0).strength(0.025))
    .force('y', forceY(0).strength(0.025))
    .alphaDecay(0.065);
  simulation.tick(64);

  // Pick the rotation that leaves most room to read at this viewport aspect.
  const bounds = extent(nodes);
  let angle = 0, score = Math.max(bounds.width / aspect, bounds.height);
  const original = nodes.map(({ x, y }) => ({ x, y }));
  const rotate = (theta: number) => {
    const c = Math.cos(theta), s = Math.sin(theta);
    nodes.forEach((node, i) => {
      node.x = original[i].x * c - original[i].y * s;
      node.y = original[i].x * s + original[i].y * c;
    });
  };
  for (const candidate of [-Math.PI / 2, -Math.PI / 3, -Math.PI / 6, Math.PI / 6, Math.PI / 3]) {
    rotate(candidate);
    const box = extent(nodes);
    const cost = Math.max(box.width / aspect, box.height);
    if (cost < score * 0.95) { score = cost; angle = candidate; }
  }
  rotate(angle);
  separateLabels(nodes);
  // In very dense hubs, repeated collision pushes can create a long tail. Repack by
  // the relaxed coordinates to retain neighborhoods within a readable rectangle.
  if (crowded && nodes.length >= 16) {
    const free = extent(nodes);
    const relaxed = nodes.map(({ x, y }) => ({ x, y }));
    compactRows(nodes, aspect, false, true);
    const packed = extent(nodes);
    if (Math.max(free.width / aspect, free.height) < 1.15 * Math.max(packed.width / aspect, packed.height)) {
      nodes.forEach((node, i) => Object.assign(node, relaxed[i]));
    }
  }
}

// A domain overview without visible relations reads as a directory. Use evenly
// spaced rows with neighboring domains following each other across each turn.
function arrangeDomains(nodes: LayoutNode[], neighbors: Map<LayoutNode, LayoutNode[]>, width: number, height: number) {
  const remaining = new Set(nodes);
  const order: LayoutNode[] = [];
  let next: LayoutNode | undefined = nodes[0];
  while (next) {
    order.push(next);
    remaining.delete(next);
    next = neighbors.get(next)!.find(node => remaining.has(node)) ?? remaining.values().next().value;
  }
  const n = nodes.length;
  const columns = Math.min(n, Math.max(1, Math.ceil(Math.sqrt(n * width / height * 0.88))));
  const rows = Math.ceil(n / columns);
  const cell = nodes.reduce((size, node) => Math.max(size, node.halfWidth * 2), 0);
  const tall = nodes[0].top + nodes[0].bottom;
  const stepX = Math.max(cell + 22, Math.min(216, (width - cell) * 0.72 / Math.max(1, columns - 1)));
  const stepY = Math.max(tall + 20, Math.min(196, (height - tall) * 0.68 / Math.max(1, rows - 1)));
  let used = 0;
  for (let row = 0; row < rows; row++) {
    const count = Math.ceil((n - used) / (rows - row));
    for (let col = 0; col < count; col++) {
      const along = row % 2 ? count - 1 - col : col;
      const node = order[used++];
      node.x = (along - (count - 1) / 2) * stepX;
      node.y = row * stepY;
    }
  }
}

// Use centered rows for disconnected islands, trying widths around the available aspect.
// A short final row stays attached to the group instead of leaving a lone node at a corner.
function pack(islands: Island[], aspect: number, gap: number) {
  const widest = Math.max(...islands.map(group => group.bounds.width));
  const area = islands.reduce((sum, { bounds }) => sum + (bounds.width + gap) * (bounds.height + gap), 0);
  const ideal = Math.sqrt(area * aspect);
  const allInRow = islands.reduce((sum, { bounds }) => sum + bounds.width + gap, -gap);
  const targets = [widest, ideal * 0.75, ideal * 0.9, ideal, ideal * 1.15, ideal * 1.35, allInRow];
  let bestScore = Infinity;
  let bestOffsets: { x: number; y: number }[] = [];
  for (const target of targets) {
    const rows: { start: number; width: number; height: number }[] = [];
    for (let i = 0; i < islands.length; i++) {
      const { width, height } = islands[i].bounds;
      let row = rows[rows.length - 1];
      if (!row || row.width + gap + width > target + 0.01) {
        row = { start: i, width: 0, height: 0 };
        rows.push(row);
      }
      row.width += (row.width ? gap : 0) + width;
      row.height = Math.max(row.height, height);
    }
    const width = Math.max(...rows.map(row => row.width));
    const height = rows.reduce((sum, row) => sum + row.height + gap, -gap);
    const score = Math.max(width / aspect, height) + 0.12 * Math.min(width / aspect, height);
    if (score >= bestScore) continue;
    bestScore = score;
    bestOffsets = [];
    let y = 0;
    for (let i = 0; i < rows.length; i++) {
      const row = rows[i];
      let x = (width - row.width) / 2;
      for (let j = row.start; j < (rows[i + 1]?.start ?? islands.length); j++) {
        const bounds = islands[j].bounds;
        bestOffsets.push({ x: x - bounds.x, y: y + (row.height - bounds.height) / 2 - bounds.y });
        x += bounds.width + gap;
      }
      y += row.height + gap;
    }
  }
  return bestOffsets;
}

export function layoutPortalRoom(
  models: readonly Node[], edges: readonly Pick<Edge, 'src_id' | 'dst_id'>[],
  labelWidths: ReadonlyMap<string, number>, area: Area, contextIds?: ReadonlySet<string>, overview = false,
) {
  const width = Math.max(1, area.width), height = Math.max(1, area.height);
  const centerX = area.x + width / 2, centerY = area.y + height / 2;
  const nodes: LayoutNode[] = models.map(model => ({
    id: model.id, model, x: 0, y: 0, ...nodeFootprint(model, labelWidths.get(model.id) ?? 44),
  })).sort(compareId);
  if (!nodes.length) return { positions: nodes, bounds: { x: centerX, y: centerY, width: 0, height: 0 }, fitScale: 1 };

  const byId = new Map(nodes.map(node => [node.id, node]));
  const adjacent = new Map(nodes.map(node => [node, new Set<LayoutNode>()]));
  for (const edge of edges) {
    const source = byId.get(edge.src_id), target = byId.get(edge.dst_id);
    if (!source || !target || source === target) continue;
    adjacent.get(source)!.add(target);
    adjacent.get(target)!.add(source);
  }
  const neighbors = new Map(Array.from(adjacent, ([node, group]) => [node, [...group].sort(compareId)]));
  const visited = new Set<LayoutNode>();
  const islands: Island[] = [];
  for (const seed of nodes) {
    if (visited.has(seed)) continue;
    const group = [seed];
    visited.add(seed);
    for (let i = 0; i < group.length; i++) {
      for (const next of neighbors.get(group[i])!) {
        if (visited.has(next)) continue;
        visited.add(next);
        group.push(next);
      }
    }
    islands.push({ nodes: group, bounds: extent(group), contextOnly: group.every(node => contextIds?.has(node.id)) });
  }
  if (overview && nodes.length > 2) {
    arrangeDomains(nodes, neighbors, width, height);
  } else {
    islands.sort((a, b) => Number(a.contextOnly) - Number(b.contextOnly) || b.nodes.length - a.nodes.length || compareId(a.nodes[0], b.nodes[0]));
    const crowded = nodes.length >= 24;
    const substantial = islands.filter(group => group.nodes.length >= Math.max(4, nodes.length * 0.14));
    for (const island of islands) {
      let aspect = width / height;
      // Two large islands cannot both occupy the full width. Give each one
      // its share of the longer axis before folding chains and arranging hubs.
      if (crowded && substantial.length > 1 && substantial.includes(island)) {
        const weight = island.nodes.length / nodes.length;
        aspect *= aspect >= 1 ? weight : 1 / weight;
      }
      arrangeConnected(island.nodes, neighbors, aspect, crowded);
      island.bounds = extent(island.nodes);
    }
    const gap = Math.max(26, Math.min(76, Math.sqrt(width * height / nodes.length) * 0.24));
    const offsets = pack(islands, width / height, gap);
    islands.forEach((island, i) => {
      for (const node of island.nodes) {
        node.x += offsets[i].x;
        node.y += offsets[i].y;
      }
    });
  }
  const bounds = extent(nodes);
  const dx = centerX - bounds.width / 2 - bounds.x, dy = centerY - bounds.height / 2 - bounds.y;
  for (const node of nodes) {
    node.x += dx;
    node.y += dy;
  }
  bounds.x += dx;
  bounds.y += dy;
  return { positions: nodes, bounds, fitScale: Math.min(1, width / bounds.width, height / bounds.height) };
}

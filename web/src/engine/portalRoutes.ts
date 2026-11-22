type Point = { x: number; y: number };
export type Curve = { start: Point; control: Point; end: Point };
export type RouteNode = Point & { radius: number; ring: number; labelWidth: number; labelOffset: number; labelHeight: number };
export type RouteEdge = { key: string; source: string; target: string };
export type Caption = { key: string; width: number; clauseWidth: number; hot: boolean };
export type CaptionPosition = Point & { anchor?: Point };

type Rect = { left: number; right: number; top: number; bottom: number };
type Trail = { points: Point[]; bounds: Rect };
type DrawnEdge = RouteEdge & { trail: Trail };
type Surface = { x?: number; y?: number; width: number; height: number };

function frameBounds(surface: Surface): Rect {
  const { x = 0, y = 0, width, height } = surface;
  return { left: x, right: x + width, top: y, bottom: y + height };
}

export function pointOnEdge(curve: Curve, t: number): Point {
  const { start, control, end } = curve;
  const m = 1 - t;
  return { x: m * m * start.x + 2 * m * t * control.x + t * t * end.x,
    y: m * m * start.y + 2 * m * t * control.y + t * t * end.y };
}

function geometry(source: RouteNode, target: RouteNode, bend: number): Curve {
  const dx = target.x - source.x, dy = target.y - source.y;
  const distance = Math.hypot(dx, dy);
  if (distance < 1) {
    const r = source.ring;
    return {
      start: { x: source.x - 0.7 * r, y: source.y - 0.7 * r },
      control: { x: source.x + bend, y: source.y - 4.2 * r - Math.abs(bend) * 0.4 },
      end: { x: source.x + 0.7 * r, y: source.y - 0.7 * r },
    };
  }
  const control = { x: (source.x + target.x) / 2 - dy * bend / distance, y: (source.y + target.y) / 2 + dx * bend / distance };
  function port(node: RouteNode) {
    const vx = control.x - node.x, vy = control.y - node.y;
    const scale = Math.min(0.88, (node.ring + 3) / (Math.hypot(vx, vy) || 1));
    return { x: node.x + vx * scale, y: node.y + vy * scale };
  }
  return { start: port(source), control, end: port(target) };
}

function nodeLabel(node: RouteNode): Rect {
  const top = node.y + node.radius + node.labelOffset;
  return { left: node.x - node.labelWidth / 2, right: node.x + node.labelWidth / 2, top: top - 2, bottom: top + node.labelHeight + 2 };
}

function touches(a: Rect, b: Rect) {
  return a.left <= b.right && a.right >= b.left && a.top <= b.bottom && a.bottom >= b.top;
}

function trail(curve: Curve): Trail {
  const points = Array.from({ length: 13 }, (_, i) => pointOnEdge(curve, i / 12));
  const bounds = { left: Infinity, right: -Infinity, top: Infinity, bottom: -Infinity };
  for (const p of points) {
    bounds.left = Math.min(bounds.left, p.x); bounds.right = Math.max(bounds.right, p.x);
    bounds.top = Math.min(bounds.top, p.y); bounds.bottom = Math.max(bounds.bottom, p.y);
  }
  return { points, bounds };
}

function crossesRect(a: Point, b: Point, rect: Rect) {
  let low = 0, high = 1;
  const dx = b.x - a.x, dy = b.y - a.y;
  if (dx === 0) {
    if (a.x < rect.left || a.x > rect.right) return false;
  } else {
    const from = (rect.left - a.x) / dx, to = (rect.right - a.x) / dx;
    low = Math.max(low, Math.min(from, to)); high = Math.min(high, Math.max(from, to));
  }
  if (dy === 0) {
    if (a.y < rect.top || a.y > rect.bottom) return false;
  } else {
    const from = (rect.top - a.y) / dy, to = (rect.bottom - a.y) / dy;
    low = Math.max(low, Math.min(from, to)); high = Math.min(high, Math.max(from, to));
  }
  return low <= high;
}

function crossesTrail(a: Trail, b: Trail) {
  if (!touches(a.bounds, b.bounds)) return false;
  const side = (p: Point, q: Point, r: Point) => (q.x - p.x) * (r.y - p.y) - (q.y - p.y) * (r.x - p.x);
  for (let i = 3; i < a.points.length; i += 3) {
    const p = a.points[i - 3], q = a.points[i];
    for (let j = 3; j < b.points.length; j += 3) {
      const r = b.points[j - 3], s = b.points[j];
      if (side(p, q, r) * side(p, q, s) < 0 && side(r, s, p) * side(r, s, q) < 0) return true;
    }
  }
  return false;
}

function nodeObstacles(nodes: ReadonlyMap<string, RouteNode>) {
  return Array.from(nodes, ([id, node]) => {
    // A modest clearance also protects outlines, arrowheads and animated flow dots.
    const r = node.ring + 4;
    return { id, body: { left: node.x - r, right: node.x + r, top: node.y - r, bottom: node.y + r }, label: nodeLabel(node) };
  });
}

export function routeEdges(nodes: ReadonlyMap<string, RouteNode>, edges: RouteEdge[], surface: Surface, crossSection = false): Map<string, Curve> {
  const paths = new Map<string, Curve>();
  const groups = new Map<string, RouteEdge[]>();
  const obstacles = nodeObstacles(nodes);
  const routed: DrawnEdge[] = [];
  const frame = frameBounds(surface);
  for (const edge of edges) {
    if (!nodes.has(edge.source) || !nodes.has(edge.target)) continue;
    const pair = JSON.stringify([edge.source, edge.target].sort());
    const group = groups.get(pair);
    if (group) group.push(edge);
    else groups.set(pair, [edge]);
  }

  // Establish short links first. A bundle keeps its lane order even when both directions occur.
  const distance = (edge: RouteEdge) => {
    const a = nodes.get(edge.source)!, b = nodes.get(edge.target)!;
    return Math.hypot(b.x - a.x, b.y - a.y);
  };
  const bundles = [...groups.entries()].sort(([ka, a], [kb, b]) => distance(a[0]) - distance(b[0]) || ka.localeCompare(kb));
  for (const [, group] of bundles) {
    group.sort((a, b) => a.key.localeCompare(b.key));
    const first = group[0];
    const a = nodes.get(first.source)!, b = nodes.get(first.target)!;
    const inside = (n: Point) => n.x >= frame.left && n.y >= frame.top && n.x <= frame.right && n.y <= frame.bottom;
    const keepInside = inside(a) && inside(b);
    const span = Math.min(crossSection ? Math.min(50,surface.width*0.12):210, Math.max(72, distance(first) * 0.68));
    const crosses = edges.length <= 100 ? routed.filter(other =>
      other.source !== first.source && other.target !== first.source && other.source !== first.target && other.target !== first.target) : [];
    let bestCost = Infinity;
    let best: { edge: RouteEdge; curve: Curve; trace: Trail }[] = [];
    for (const shift of [0, 0.24, -0.24, 0.5, -0.5, 0.8, -0.8, 1.2, -1.2]) {
      let cost = Math.abs(shift) * span * 0.075;
      if (cost >= bestCost) continue;
      const candidate = [];
      for (const [i, edge] of group.entries()) {
        const lane = (i - (group.length - 1) / 2) * (crossSection ? 16 : 48);
        const direction = edge.source <= edge.target ? 1 : -1;
        const curve = geometry(nodes.get(edge.source)!, nodes.get(edge.target)!, (lane + shift * span) * direction);
        const trace = trail(curve);
        for (const obstacle of obstacles) {
          const body = obstacle.id !== edge.source && obstacle.id !== edge.target && touches(trace.bounds, obstacle.body);
          const label = touches(trace.bounds, obstacle.label);
          if (!body && !label) continue;
          for (let s = 1; s < trace.points.length; s++) {
            const p = trace.points[s - 1], q = trace.points[s];
            if (body && crossesRect(p, q, obstacle.body)) cost += 90;
            if (label && crossesRect(p, q, obstacle.label)) cost += 34;
          }
        }
        if (keepInside && trace.points.some(p => !inside(p))) cost += crossSection ? 10000 : 60;
        for (const other of crosses) if (crossesTrail(trace, other.trail)) cost += 12;
        candidate.push({ edge, curve, trace });
        if (cost >= bestCost) break;
      }
      if (cost < bestCost) { bestCost = cost; best = candidate; }
      if (bestCost === 0) break;
    }
    for (const { edge, curve, trace } of best) {
      paths.set(edge.key, curve);
      if (edges.length <= 100) routed.push({ ...edge, trail: trace });
    }
  }
  return paths;
}

function overlap(a: Rect, b: Rect) {
  return Math.max(0, Math.min(a.right, b.right) - Math.max(a.left, b.left)) *
    Math.max(0, Math.min(a.bottom, b.bottom) - Math.max(a.top, b.top));
}

export function placeCaptions(nodes: ReadonlyMap<string, RouteNode>, paths: ReadonlyMap<string, Curve>,
  captions: Caption[], surface: Surface): Map<string, CaptionPosition> {
  const positions = new Map<string, CaptionPosition>();
  const occupied = nodeObstacles(nodes).flatMap(node => [node.body, node.label]);
  const frame = frameBounds(surface);
  const ordered = [...captions].sort((a, b) => Number(b.hot) - Number(a.hot) || a.key.localeCompare(b.key));
  for (const caption of ordered) {
    const path = paths.get(caption.key);
    if (!path) continue;
    let bestCost = Infinity, bestOverlap = Infinity;
    let best: CaptionPosition = pointOnEdge(path, 0.5);
    let bestBoxes: Rect[] = [];
    function consider(x: number, y: number, distance: number, anchor?: Point) {
      if (bestOverlap === 0 && distance >= bestCost) return;
      const boxes = [{ left: x - caption.width / 2 - 2, right: x + caption.width / 2 + 2, top: y - 12, bottom: y + 12 }];
      if (caption.clauseWidth) boxes.push({ left: x - caption.clauseWidth / 2 - 2, right: x + caption.clauseWidth / 2 + 2, top: y + 12, bottom: y + 37 });
      let covered = 0;
      for (const box of boxes) {
        covered += 2 * Math.max(0, (box.right - box.left) * (box.bottom - box.top) - overlap(box, frame));
        for (const block of occupied) covered += overlap(box, block);
      }
      if (covered < 0.01) covered = 0;
      if (covered < bestOverlap || (covered === bestOverlap && distance < bestCost)) {
        bestOverlap = covered; bestCost = distance; best = anchor ? { x, y, anchor } : { x, y }; bestBoxes = boxes;
      }
    }
    // Prefer the midpoint, then slide along the curve. An offset label retains a small leader.
    for (const offset of [0, -23, 23, -44, 44]) {
      for (const t of [0.5, 0.40, 0.60, 0.30, 0.70]) {
        const anchor = pointOnEdge(path, t);
        const dx = (1 - t) * (path.control.x - path.start.x) + t * (path.end.x - path.control.x);
        const dy = (1 - t) * (path.control.y - path.start.y) + t * (path.end.y - path.control.y);
        const length = Math.hypot(dx, dy) || 1;
        consider(anchor.x - dy / length * offset, anchor.y + dx / length * offset,
          Math.abs(t - 0.5) * 40 + Math.abs(offset), offset ? anchor : undefined);
      }
      if (bestOverlap === 0 && bestCost === 0) break;
    }
    // At a dense hub, neither side of a short edge may fit a caption. Find the nearest free callout slot.
    if (bestOverlap > 0) {
      const anchor = pointOnEdge(path, 0.5);
      for (const reach of [30, 46, 64, 84, 108, 138, 172]) {
        for (let i = 0; i < 16; i++) {
          const angle = Math.PI * i / 8;
          consider(anchor.x + Math.cos(angle) * reach, anchor.y + Math.sin(angle) * reach, reach, anchor);
        }
        if (bestOverlap === 0) break;
      }
    }
    occupied.push(...bestBoxes);
    positions.set(caption.key, best);
  }
  return positions;
}

// A camera move changes every endpoint. Keep the lanes stable in model order
// and project them directly, instead of running the 2D obstacle solver per frame.
export function routeOrbitEdges(nodes: ReadonlyMap<string,RouteNode>, edges:RouteEdge[], _surface:Surface) {
  const bundles = new Map<string,RouteEdge[]>();
  for (const edge of edges) {
    if (!nodes.has(edge.source) || !nodes.has(edge.target)) continue;
    const key = JSON.stringify([edge.source,edge.target].sort());
    const group = bundles.get(key);
    if (group) group.push(edge);
    else bundles.set(key,[edge]);
  }
  const paths=new Map<string,Curve>();
  for (const group of bundles.values()) {
    group.sort((a,b)=>a.key.localeCompare(b.key));
    group.forEach((edge,i)=>{
      const lane=(i-(group.length-1)/2)*12*(edge.source<=edge.target?1:-1);
      paths.set(edge.key,geometry(nodes.get(edge.source)!,nodes.get(edge.target)!,lane));
    });
  }
  return paths;
}

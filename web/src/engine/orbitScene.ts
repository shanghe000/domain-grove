// All layer geometry and pickable nodes use the same world-to-screen camera.
// X runs across a layer, Y is height, and +Z points towards the observer.
import type { Node } from '../types.ts';
import { plane, planeNames, type Cut } from './indexProjection.ts';

type Point = { x: number; y: number };
type Area = Point & { width: number; height: number };
type WorldPoint = Point & { z: number };
type LayoutNode = Point & { id: string; model: Node };
type Projected = Point & { depth: number; scale: number };
type Bed = { id: string; name: string; level: number; w: number; d: number; y: number; occupied: boolean };
export type OrbitScene = { beds: Bed[]; nodes: Map<string, WorldPoint>; lens: number };
export type OrbitPoint = Projected & { foot: Point };
export type OrbitPlate = {
  id: string; name: string; level: number; populated: boolean;
  corners: Projected[]; lower: Projected[]; center: Projected; depth: number;
};
export type OrbitFrame = {
  points: Map<string, OrbitPoint>; plates: OrbitPlate[]; scale: number;
};

// Leave a larger cell for a node which will actually need a longer label.
// Each row is centred separately so a sparse level is still legible.
export function buildOrbitScene(
  positions: readonly LayoutNode[], area: Area, cut: Cut, labelWidths: ReadonlyMap<string, number>,
): OrbitScene {
  const layers = planeNames[cut].map((name, level) => ({
    name, level,
    members: positions.filter(n => plane(n.model, cut) === level)
      .sort((a, b) => a.x - b.x || a.y - b.y || a.id.localeCompare(b.id)),
  }));
  const largest = Math.max(1, ...layers.map(g => g.members.length));
  const label = Math.max(84, ...positions.map(p => labelWidths.get(p.id) || 84));
  const aspect = Math.max(.35, Math.min(3, area.width / Math.max(1, area.height)));
  // A broad shelf in a tall canvas exhausts the lateral camera frustum
  // while most of the vertical field stays empty. Give that shelf more rows
  // and separate the decks before projecting them, keeping the orbit rigid.
  const portrait = Math.max(0, Math.min(1, (1.35 - aspect) / .5));
  const widePitch = Math.min(160, label + 21);
  const pitchX = widePitch + portrait * (Math.min(112, label + 12) - widePitch);
  const columns = Math.min(largest, 10, Math.max(2, Math.round(Math.sqrt(largest * aspect * (2.9 - 1.55 * portrait)))));
  const width = Math.max(320, columns * pitchX + 36);
  const rowPitch = 174;
  const beds: Bed[] = [];
  const nodes = new Map<string, WorldPoint>();

  for (const { level, name, members } of layers) {
    const rows = Math.ceil(members.length / columns);
    // Empty semantic levels stay visible as shallow shelves.
    const d = members.length ? Math.max(width * .25, (rows - 1) * rowPitch + 138) : Math.max(52, width * .12);
    const previous = beds[level - 1];
    const y = previous ? previous.y + 80 + 38 * portrait + .27 * (previous.d + d) : 0;
    beds.push({ id: cut + ':' + level, name, level, w: width, d, y, occupied: rows > 0 });
    members.forEach((node, index) => {
      const row = Math.floor(index / columns);
      const count = Math.min(columns, members.length - row * columns);
      const x = (index % columns - (count - 1) / 2) * pitchX;
      // Shift forwards a little to leave room for the heading at the back.
      const z = (row - (rows - 1) / 2) * rowPitch + 13;
      nodes.set(node.id, { x, y: y + 15, z });
    });
  }
  const halfHeight = beds[beds.length - 1].y / 2;
  for (const bed of beds) bed.y -= halfHeight;
  for (const point of nodes.values()) point.y -= halfHeight;
  const span = Math.max(width, 2 * halfHeight, ...beds.map(b => b.d));
  // The far rows of a deep, narrow deck should remain usable hit targets.
  return { beds, nodes, lens: span * (1.9 + .90 * portrait) };
}

const min = Math.min, max = Math.max;
const planeThickness = 6;
export function projectOrbitScene(scene: OrbitScene, area: Area, camera: { tilt: number; yaw: number }): OrbitFrame {
  const pitch = max(0, min(1.4, camera.tilt));
  const sp = Math.sin(pitch), cp = Math.cos(pitch);
  const sy = Math.sin(camera.yaw), cy = Math.cos(camera.yaw);
  function cameraPoint(x: number, y: number, z: number): Projected {
    const side = cy * x + sy * z;
    const forward = cy * z - sy * x;
    const depth = cp * y + sp * forward;
    const perspective = scene.lens / max(scene.lens * .2, scene.lens - depth);
    return { x: side * perspective, y: (cp * forward - sp * y) * perspective, depth, scale: perspective };
  }

  let left = Infinity, right = -Infinity, top = Infinity, bottom = -Infinity;
  const plates = scene.beds.map(({ id, name, level, occupied, y, w, d }) => {
    const bounds = [[-w / 2, -d / 2], [w / 2, -d / 2], [w / 2, d / 2], [-w / 2, d / 2]];
    const corners = bounds.map(([x, z]) => cameraPoint(x, y, z));
    const lower = bounds.map(([x, z]) => cameraPoint(x, y - planeThickness, z));
    for (const p of [...corners, ...lower]) {
      left = min(left, p.x); right = max(right, p.x);
      top = min(top, p.y); bottom = max(bottom, p.y);
    }
    return { id, name, level, populated: occupied, corners, lower, center: cameraPoint(0, y, 0), depth: cameraPoint(0, y, 0).depth };
  });

  // Fit the physical model, not a fresh 2D graph layout, when the camera moves.
  const padX = Math.min(22, area.width * .035);
  const padY = Math.min(23, area.height * .045);
  const scale = min((area.width - padX * 2) / max(1, right - left), (area.height - 2 * padY) / max(1, bottom - top));
  const ox = area.x + area.width / 2 - scale * (right + left) / 2;
  const oy = area.y + area.height / 2 - scale * (bottom + top) / 2;
  function screen(p: Projected): Projected {
    return { x: ox + p.x * scale, y: oy + p.y * scale, depth: p.depth, scale: p.scale * scale };
  }

  const points = new Map<string, OrbitPoint>();
  for (const [id, { x, y, z }] of scene.nodes) {
    const p = screen(cameraPoint(x, y, z));
    points.set(id, { ...p, foot: screen(cameraPoint(x, y - 15, z)) });
  }
  for (const plate of plates) {
    plate.center = screen(plate.center);
    plate.corners = plate.corners.map(screen);
    plate.lower = plate.lower.map(screen);
  }
  plates.sort((a, b) => a.depth - b.depth);
  return { points, plates, scale };
}

// The visible front and side edges share the projected footprint. No CSS
// transform is applied to the bitmap, the hit layer or the text.
export function drawOrbitPlates(
  ctx: CanvasRenderingContext2D, frame: OrbitFrame, opacity: number, transform: (p: Point) => Point = p => p,
) {
  if (opacity <= 0) return;
  ctx.save();
  ctx.globalAlpha = min(1, opacity);
  ctx.lineJoin = 'round';
  ctx.lineWidth = 1;
  const contour = (points: readonly Point[]) => {
    ctx.beginPath();
    ctx.moveTo(points[0].x, points[0].y);
    for (let i = 1; i < points.length; i++) ctx.lineTo(points[i].x, points[i].y);
    ctx.closePath();
  };
  const line = (a: Point, b: Point) => {
    ctx.beginPath(); ctx.moveTo(a.x, a.y); ctx.lineTo(b.x, b.y); ctx.stroke();
  };
  const blend = (a: Point, b: Point, t: number) => ({ x: a.x + (b.x - a.x) * t, y: a.y + (b.y - a.y) * t });
  const levels = ['137,151,228', '106,178,186', '129,157,216', '202,171,111'];
  for (const plate of frame.plates) {
    const p = plate.corners.map(transform), b = plate.lower.map(transform);
    const pigment = levels[plate.level % levels.length];
    const rear = blend(p[0], p[1], .5), front = blend(p[3], p[2], .5);
    const finish = ctx.createLinearGradient(rear.x, rear.y, front.x, front.y + .1);
    finish.addColorStop(0, 'rgba(49,74,105,.18)');
    finish.addColorStop(1, 'rgba(' + pigment + ',' + (plate.populated ? '.19)' : '.10)'));
    ctx.fillStyle = finish;
    ctx.strokeStyle = 'rgba(' + pigment + ',.42)';
    contour(p); ctx.fill(); ctx.stroke();

    // A few datum marks make rotation apparent even on an empty layer.
    ctx.save();
    contour(p); ctx.clip();
    ctx.strokeStyle = 'rgba(' + pigment + ',.11)';
    for (const t of [.25, .5, .75]) line(blend(p[0], p[1], t), blend(p[3], p[2], t));
    line(blend(p[0], p[3], .5), blend(p[1], p[2], .5));
    ctx.restore();

    for (let i = 0; i < 4; i++) {
      const j = (i + 1) % 4;
      // The projected outward side has positive screen winding.
      const cross = (p[j].x - p[i].x) * (b[i].y - p[i].y) - (p[j].y - p[i].y) * (b[i].x - p[i].x);
      if (cross >= 0) continue;
      contour([p[i], p[j], b[j], b[i]]);
      ctx.fillStyle = 'rgba(29,44,65,.83)'; ctx.fill();
      ctx.strokeStyle = 'rgba(' + pigment + ',.30)'; ctx.stroke();
      ctx.strokeStyle = 'rgba(' + pigment + ',.73)'; line(p[i], p[j]);
    }

    // Captions sit above the back edge, outside the plane's node/label area.
    // Keep the caption on one physical corner as yaw crosses zero.
    const corner = p[0];
    ctx.font = '600 11px system-ui,"PingFang SC",sans-serif';
    ctx.textBaseline = 'bottom'; ctx.textAlign = 'left';
    ctx.fillStyle = '#c4d2e4';
    ctx.fillText(plate.name, corner.x + 3, corner.y - 3);
  }

  // Contact shadows and the short stand-off are projected at the node's own
  // world coordinate. This makes each circle read as sitting above its deck.
  for (const node of frame.points.values()) {
    const foot = transform(node.foot), center = transform(node);
    const rise = Math.hypot(center.x - foot.x, center.y - foot.y);
    if (rise < 2) continue;
    const r = min(17, max(3, rise * .8));
    ctx.fillStyle = 'rgba(5,12,22,.38)';
    ctx.beginPath(); ctx.ellipse(foot.x, foot.y + 1, r, r * .25, 0, 0, Math.PI * 2); ctx.fill();
  }
  ctx.restore();
}

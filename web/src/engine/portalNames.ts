import type { Node } from '../types.ts';
import { nodeFootprint, nodeRadii } from './portalLayout.ts';

export type NodeName = { text: string; font: string; width: number; height: number; offset: number; ascent: number };
export const nodeFont = (domain: boolean, size = domain ? 15 : 12) => `${domain ? 650 : 570} ${size}px system-ui, "PingFang SC", sans-serif`;

export function fitNodeName(ctx: Pick<CanvasRenderingContext2D, 'measureText' | 'font' | 'textBaseline'>,
  model: Node, fullWidth: number, scale: number): NodeName {
  const domain = model.node_type === 'Domain';
  const slot = nodeFootprint(model, fullWidth);
  const maxWidth = Math.max(0, (2 * slot.halfWidth - 4) * scale);
  const maxHeight = (slot.bottom - nodeRadii[model.node_type]) * scale;
  const size = Math.min(maxHeight / 1.35, Math.max(10, (domain ? 15 : 12) * Math.min(scale, 1)));
  const font = nodeFont(domain, size);
  ctx.font = font;
  ctx.textBaseline = 'alphabetic';
  let text = model.label;
  if (ctx.measureText(text).width > maxWidth) {
    const chars = Array.from(text);
    let low = 0, high = chars.length;
    while (low < high) {
      const mid = Math.ceil((low + high) / 2);
      if (ctx.measureText(chars.slice(0, mid).join('') + '…').width <= maxWidth) low = mid;
      else high = mid - 1;
    }
    text = ctx.measureText('…').width <= maxWidth ? chars.slice(0, low).join('') + '…' : '';
  }
  const metrics = ctx.measureText(text);
  const height = metrics.actualBoundingBoxAscent + metrics.actualBoundingBoxDescent;
  const offset = Math.max(0, Math.min((domain ? 16 : 10) * Math.min(scale, 1), maxHeight - height));
  return { text, font, width: metrics.width, height, offset, ascent: metrics.actualBoundingBoxAscent };
}

type LabelTarget = { id: string; x: number; y: number; radius: number; name: NodeName };
type Bounds = { left: number; right: number; top: number; bottom: number };

// Hide only captions that would cover another node or a label already placed.
// All node centres remain interactive. Input order supplies a stable priority.
export function visibleOrbitNames(targets: LabelTarget[], area: {x:number;y:number;width:number;height:number}): Set<string> {
  const visible = new Set<string>();
  const cells = new Map<string, Bounds[]>();
  function buckets(rect: Bounds, visit: (key: string) => void) {
    for (let x = Math.floor(rect.left / 64); x <= Math.floor(rect.right / 64); x++)
      for (let y = Math.floor(rect.top / 64); y <= Math.floor(rect.bottom / 64); y++) visit(x + '/' + y);
  }
  function reserve(rect: Bounds) {
    buckets(rect, key => {
      const bucket = cells.get(key);
      if (bucket) bucket.push(rect); else cells.set(key, [rect]);
    });
  }
  for (const { x, y, radius } of targets) reserve({
    left: x-radius-1, right:x+radius+1, top:y-radius-1, bottom:y+radius+1,
  });
  for (const {id, x, y, radius, name} of targets) {
    if (!name.text) continue;
    const top = y + radius + name.offset;
    const label = { left:x-name.width/2-2, right:x+name.width/2+2, top, bottom:top+name.height+2 };
    if (label.left<area.x || label.right>area.x+area.width || top<area.y || label.bottom>area.y+area.height) continue;
    let occupied = false;
    buckets(label, key => {
      if (!occupied) occupied = cells.get(key)?.some(b =>
        b.left<label.right && b.right>label.left && b.top<label.bottom && b.bottom>label.top) ?? false;
    });
    if (!occupied) { visible.add(id); reserve(label); }
  }
  return visible;
}

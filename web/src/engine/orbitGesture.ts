/** Angles in radians. Progress controls how far apart the graph layers are. */
export type OrbitPose = { tilt: number; yaw: number; progress: number };

/** The first orbit opens in either direction. After opening, dragging back
 * toward a top-down view closes the layers only in the last few degrees. */
export function dragOrbit(start: Pick<OrbitPose, 'tilt' | 'yaw'>, dx: number, dy: number, expanded: boolean): OrbitPose {
  const tilt = Math.max(0, Math.min(1.25, start.tilt === 0
    ? Math.hypot(dx * .55, dy) / 190
    : start.tilt + dy / 215));
  const closeRange = Math.min(.17, start.tilt || .17);
  const t = Math.min(1, tilt / (expanded ? closeRange : .28));
  return {
    tilt,
    yaw: Math.max(-1, Math.min(1, start.yaw + dx / 265)),
    progress: t * t * (3 - 2 * t),
  };
}

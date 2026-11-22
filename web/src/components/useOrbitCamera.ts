import { useRef, useState } from 'react';

import type { OrbitPose as Pose } from '../engine/orbitGesture';
type Flight = { from: Pose; to: Pose; start: number; duration: number };

// Pose is sampled by the canvas's own animation frame. React owns the two
// discrete mode changes; pointer events only update the next frame's target.
export function useOrbitCamera(reduced: boolean, invalidate: () => void) {
  const [depth, setDepth] = useState(false);
  const [depthRequested, setDepthRequested] = useState(false);
  const cameraRef = useRef<Pose>({ tilt: 0, yaw: 0, progress: 0 });
  const target = useRef(cameraRef.current);
  const flight = useRef<Flight | null>(null);
  const cameraMoving = useRef(false);
  const lastFrame = useRef(0);
  const mounted = useRef(false);
  const requested = useRef(false);

  function mountDepth(value: boolean) {
    if (mounted.current !== value) {
      mounted.current = value;
      setDepth(value);
    }
  }

  function requestDepth(value: boolean) {
    if (requested.current !== value) {
      requested.current = value;
      setDepthRequested(value);
    }
    if (value) mountDepth(true);
  }

  function finish() {
    cameraRef.current = target.current;
    cameraMoving.current = false;
    flight.current = null;
    lastFrame.current = 0;
    if (!requested.current) mountDepth(false);
  }

  function advanceCamera(now: number) {
    if (!cameraMoving.current) return;
    const tween = flight.current;
    if (reduced) {
      finish();
    } else if (tween) {
      const t = Math.min(1, Math.max(0, (now - tween.start) / tween.duration));
      if (t === 1) finish();
      else {
        // A continuous acceleration and braking phase for mode changes.
        const ease = t * t * (3 - 2 * t);
        cameraRef.current = {
          progress: tween.from.progress + (tween.to.progress - tween.from.progress) * ease,
          tilt: tween.from.tilt + (tween.to.tilt - tween.from.tilt) * ease,
          yaw: tween.from.yaw + (tween.to.yaw - tween.from.yaw) * ease,
        };
      }
    } else {
      const elapsed = lastFrame.current ? Math.max(0, now - lastFrame.current) : 16;
      const mix = 1 - Math.exp(-elapsed / 22);
      const next = target.current;
      const pose = cameraRef.current;
      const error = Math.max(Math.abs(next.tilt - pose.tilt), Math.abs(next.yaw - pose.yaw), Math.abs(next.progress - pose.progress));
      if (error < 0.001) finish();
      else cameraRef.current = {
        progress: pose.progress + (next.progress - pose.progress) * mix,
        tilt: pose.tilt + (next.tilt - pose.tilt) * mix,
        yaw: pose.yaw + (next.yaw - pose.yaw) * mix,
      };
    }
    if (cameraMoving.current) {
      lastFrame.current = now;
      invalidate();
    }
  }

  function tiltTo(tilt: number, yaw: number) {
    requestDepth(tilt > 0);
    const to = { tilt, yaw, progress: tilt > 0 ? 1 : 0 };
    target.current = to;
    const from = cameraRef.current;
    const distance = Math.max(Math.abs(from.tilt - tilt), Math.abs(from.yaw - yaw), Math.abs(from.progress - to.progress));
    if (reduced || distance < 0.001) finish();
    else {
      flight.current = { from, to, start: performance.now(), duration: 170 + Math.min(1, distance) * 240 };
      cameraMoving.current = true;
    }
    invalidate();
  }

  function follow(pose: Pose) {
    requestDepth(true);
    target.current = pose;
    flight.current = null;
    if (reduced) finish();
    else cameraMoving.current = true;
    invalidate();
  }

  return { cameraRef, cameraMoving, target, depth, depthRequested, tiltTo, follow, advanceCamera };
}

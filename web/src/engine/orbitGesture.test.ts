import assert from 'node:assert/strict';
import test from 'node:test';
import { dragOrbit } from './orbitGesture.ts';

test('spreads the layers independently of camera pitch and holds separation during yaw', () => {
  const flat = { tilt: 0, yaw: 0 };
  assert.equal(dragOrbit(flat, 0, 0, false).progress, 0);
  assert.equal(dragOrbit(flat, 0, -56, false).progress, 1);
  const mid = dragOrbit(flat, 180, 0, false);
  assert.ok(mid.tilt < .6);
  assert.equal(mid.progress, 1);
  const turned = dragOrbit(mid, -90, 0, true);
  assert.equal(turned.progress, 1);
  assert.equal(turned.tilt, mid.tilt);
  assert.ok(turned.yaw < mid.yaw);
});

test('a shallow settled view stays spread while rotating, then closes at the flat plane', () => {
  const shallow = { tilt: .15, yaw: .25 };
  assert.equal(dragOrbit(shallow, 30, 0, true).progress, 1);
  const closing = dragOrbit(shallow, 30, -12, true);
  assert.ok(closing.progress > 0 && closing.progress < 1);
  assert.equal(dragOrbit(shallow, 30, -34, true).progress, 0);
});

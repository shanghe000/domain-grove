import assert from 'node:assert/strict';
import { test } from 'node:test';
import type { Node } from '../types.ts';
import { portalLayoutLinks } from './portalLayoutLinks.ts';

test('hidden descendants attract their nearest visible containers without inventing a connection inside one container', () => {
  const node = (id: string, parent: string): Node => ({
    id, domain_id: parent, node_type: 'Concept', label: id, status: 'active', properties: {}, axioms: [],
  });
  const nodes = [node('a', ''), node('b', ''), node('b1', 'b'), node('b2', 'b1'), node('cycle', 'cycle')];
  const edge = (src_id: string, dst_id: string) => ({ src_id, dst_id });
  const links = [edge('a', 'b2'), edge('b1', 'b2'), edge('b1', 'missing'), edge('b1', 'cycle')];
  const byId = new Map(nodes.map(item => [item.id, item]));
  assert.deepEqual(portalLayoutLinks(links, byId, new Set(['a', 'b', 'b1'])), [edge('a', 'b1')]);
  assert.deepEqual(portalLayoutLinks(links, byId, new Set(['a', 'b'])), [edge('a', 'b')]);
});

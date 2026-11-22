import type { Edge, Node } from '../types';

type Endpoints = Pick<Edge, 'src_id' | 'dst_id'>;

// Resolve hidden endpoints to the nearest visible container for the layout forces only.
export function portalLayoutLinks(edges: readonly Endpoints[], nodes: ReadonlyMap<string, Node>, visible: ReadonlySet<string>): Endpoints[] {
  const ancestors = new Map<string, string>();
  function representative(id: string) {
    const path = new Set<string>();
    let current = id;
    while (current && !visible.has(current) && !ancestors.has(current) && !path.has(current)) {
      path.add(current);
      current = nodes.get(current)?.domain_id ?? '';
    }
    const result = visible.has(current) ? current : ancestors.get(current) ?? '';
    for (const member of path) ancestors.set(member, result);
    return result;
  }
  const links: Endpoints[] = [];
  for (const edge of edges) {
    const source = representative(edge.src_id), target = representative(edge.dst_id);
    if (source && target && source !== target) links.push({ src_id: source, dst_id: target });
  }
  return links;
}

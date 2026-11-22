import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import type { CSSProperties, PointerEvent as ReactPointerEvent } from 'react';

import { createBudgetTraversal, DEFAULT_BUDGET } from '../engine/bfsBudget';
import type { BudgetContext } from '../engine/bfsBudget';
import { layoutPortalRoom, nodeRadii as radii } from '../engine/portalLayout';
import { portalLayoutLinks } from '../engine/portalLayoutLinks';
import { layoutPortalPreview } from '../engine/portalPreview';
import { fitNodeName, nodeFont, visibleOrbitNames } from '../engine/portalNames';
import type { NodeName } from '../engine/portalNames';
import { projectIndex, plane, type Cut } from '../engine/indexProjection';
import { buildOrbitScene, projectOrbitScene, drawOrbitPlates, type OrbitFrame, type OrbitScene } from '../engine/orbitScene';
import { edgeKey } from '../types';
import { placeCaptions, pointOnEdge, routeEdges, routeOrbitEdges } from '../engine/portalRoutes';
import type { Caption, CaptionPosition, Curve, RouteNode } from '../engine/portalRoutes';
import type { Edge, Graph, Node, NodeType } from '../types';
import { dragOrbit } from '../engine/orbitGesture';
import { useOrbitCamera } from './useOrbitCamera';
import './portal.css';

interface PortalProps {
  graph: Graph;
  layoutKey?:string;
  loadContext?:(id:string) => Promise<BudgetContext>;
  draftEdgeIds?:ReadonlySet<string>;
  domainId: string;
  hierarchical?: boolean;
  emptyText?: string;
  onDomainChange: (id: string) => void;
  selectedId: string;
  onSelect: (id: string) => void;
  onMoveNode?: (id: string, domainId: string) => void;
  draftNodeIds: ReadonlySet<string>;
}

type Anchor = { x: number; y: number; radius: number };
type Aperture = Anchor & { extent: number };
type View = { domain: string; key: number; entrance?: Anchor };
type Transition = '' | 'enter' | 'exit';
type EdgeIdentity = (edge: Edge) => string;
type Position = Point & { id: string; model: Node };
type Point = { x: number; y: number };
type CanvasNode = Anchor & { model: Node };
type Viewport = { scale: number; x: number; y: number };
type NodeDrag = { node: Node; targetId: string; x: number; y: number };

const nodeColors: Record<NodeType, string> = { Domain: '#312E81', Metric: '#F59E0B', Concept: '#10B981', Artifact: '#818CF8' };
const typeLabels: Record<NodeType, string> = { Domain: '领域', Metric: '指标', Concept: '概念', Artifact: '资料' };
const graphColors: Record<Edge['predicate'], string> = {
  collaborates_with:'#ca9ae8', aggregate:'#9caac9', membership:'#617398', related:'#adbad0', depends_on: '#3B82F6', impacted_by: '#F59E0B', defined_in: '#10B981', configured_by: '#10B981', foreign_key_to: '#94A3B8',
};
const ink = '#eaf1ff';
const portalDuration = 350;
const domainClickDelay = 360;
const edgeLabelScale = 0.75;
const inactiveOpacity = 0.10;
const indexEdgeKey: EdgeIdentity = edge => edge.id ? `id:${edge.id}` : edgeKey(edge);
const rootView: View = { domain: '', key: 0 };

export function PortalCanvas(props: PortalProps) {
  const { domainId, graph, hierarchical = false, onDomainChange } = props;
  const parents = useMemo(() => hierarchical ? new Map(graph.nodes.map(node => [node.id, node.domain_id])) : undefined, [graph.nodes, hierarchical]);
  const targetPath = useMemo(() => viewPath(domainId, parents), [domainId, parents]);
  const [views, setViews] = useState<View[]>(() => targetPath.map((domain, key) => key ? { domain, key } : rootView));
  const view = views[views.length - 1];
  const viewKey = useRef(views.length - 1);
  const [previous, setPrevious] = useState<View | null>(null);
  const [direction, setDirection] = useState<Transition>('');
  const [transitionOrigin, setTransitionOrigin] = useState<Aperture>();
  const host = useRef<HTMLDivElement>(null);
  const nextAnchor = useRef<Anchor>();
  const viewAnchors = useRef(new Map<string, ReadonlyMap<string, CanvasNode>>());
  const storeAnchors = useCallback((id: string, anchors: ReadonlyMap<string, CanvasNode>) => {
    if (anchors.size) viewAnchors.current.set(id, anchors);
    else viewAnchors.current.delete(id);
  }, []);
  const skipMotion = useReducedMotion();

  const navigate = (id: string, anchor?: Anchor) => {
    if (id === domainId) return;
    nextAnchor.current = anchor;
    onDomainChange(id);
  };

  useLayoutEffect(() => {
    if (views.length === targetPath.length && views.every((layer, index) => layer.domain === targetPath[index])) return;
    const existing = new Map(views.map(layer => [layer.domain, layer]));
    const buildViews = (entrance?: Anchor) => targetPath.map(domain => existing.get(domain) ?? {
      domain, key: ++viewKey.current, entrance: domain === domainId ? entrance : undefined,
    });
    if (view.domain === domainId) {
      setViews(buildViews());
      return;
    }
    const bounds = host.current?.getBoundingClientRect();
    const fallback = { x: (bounds?.width ?? 0) / 2, y: (bounds?.height ?? 0) / 2, radius: 32 };
    const returnIndex = views.findIndex(layer => layer.domain === domainId);
    const exiting = returnIndex >= 0;
    const aperture = exiting
      ? viewAnchors.current.get(domainId)?.get(views[returnIndex + 1].domain)
      : viewAnchors.current.get(view.domain)?.get(domainId);
    const anchor = (exiting ? aperture ?? nextAnchor.current : nextAnchor.current ?? aperture) ?? fallback;
    nextAnchor.current = undefined;
    const width = bounds?.width ?? 0;
    const height = bounds?.height ?? 0;
    setTransitionOrigin({
      ...anchor,
      // CSS circle() resolves percentage radii against the normalized diagonal.
      extent: 1.5 * Math.hypot(width, height) / Math.SQRT2,
    });
    setPrevious(skipMotion ? null : view);
    setDirection(exiting ? 'exit' : 'enter');
    setViews(buildViews(!skipMotion && !exiting ? anchor : undefined));
  }, [domainId, skipMotion, targetPath, view, views]);

  useEffect(() => {
    if (!previous) return;
    // Inactive tabs can suppress CSS animation events.
    const id = window.setTimeout(() => setPrevious(null), portalDuration + 120);
    return () => window.clearTimeout(id);
  }, [previous]);

  const finishTransition = useCallback(() => setPrevious(null), []);
  const transition = previous && !skipMotion ? direction : '';
  const originStyle = transitionOrigin && {
    '--portal-x': `${transitionOrigin.x}px`,
    '--portal-y': `${transitionOrigin.y}px`,
    '--portal-r': `${transitionOrigin.radius}px`,
    '--portal-extent': `${transitionOrigin.extent}px`,
    '--portal-duration': `${portalDuration}ms`,
  } as CSSProperties;
  const drawingOrder = [...views];
  if (previous && transition && !drawingOrder.includes(previous)) {
    drawingOrder.splice(transition === 'exit' ? drawingOrder.length : drawingOrder.length - 1, 0, previous);
  }

  return (
    <div className="ontology-portal" ref={host} style={originStyle} data-portal-transition={transition || undefined}>
      {drawingOrder.map(layer => (
        <CanvasLayer
          key={layer.key} view={layer} active={layer.key === view.key}
          phase={layer.key === view.key && transition === 'enter' ? 'enter' : layer.key === previous?.key && transition === 'exit' ? 'exit' : ''}
          skipMotion={skipMotion} navigate={navigate} storeAnchors={storeAnchors} finishTransition={finishTransition} {...props}
        />
      ))}
      {transition && <div key={transition} aria-hidden="true" className={`portal-rim portal-rim--${transition}`} />}
    </div>
  );
}

function viewPath(domain: string, parents?: ReadonlyMap<string, string>): string[] {
  const path = new Set<string>();
  for (let id = domain; id && !path.has(id); id = parents?.get(id) ?? '') path.add(id);
  return ['', ...Array.from(path).reverse()];
}

function useReducedMotion() {
  const [reduced, setReduced] = useState(() => window.matchMedia('(prefers-reduced-motion: reduce)').matches);
  useEffect(() => {
    const media = window.matchMedia('(prefers-reduced-motion: reduce)');
    const change = () => setReduced(media.matches);
    media.addEventListener('change', change);
    return () => media.removeEventListener('change', change);
  }, []);
  return reduced;
}

type LayerProps = PortalProps & {
  view: View;
  active: boolean;
  phase: Transition;
  skipMotion: boolean;
  navigate: (id: string, anchor?: Anchor) => void;
  storeAnchors: (id: string, anchors: ReadonlyMap<string, CanvasNode>) => void;
  finishTransition: () => void;
};

type Renderer = {
  draw: () => void;
  zoom: (factor: number, origin?: Point) => number;
  reset: () => void;
  pan: (dx: number, dy: number) => void;
  pin:(id:string,x:number,y:number)=>void;
  hitEdge:(x:number,y:number)=>void;
  anchor: (id: string) => Anchor | undefined;
};

function CanvasLayer({ graph, hierarchical = false, emptyText, view, active, phase, skipMotion, navigate, storeAnchors, finishTransition, selectedId, onSelect, onMoveNode, draftNodeIds, draftEdgeIds, loadContext, layoutKey:spaceKey }: LayerProps) {
  const layer = useRef<HTMLDivElement>(null);
  const canvas = useRef<HTMLCanvasElement>(null);
  const controls = useRef(new Map<string, HTMLButtonElement>());
  const renderer = useRef<Renderer>();
  const gesture = useRef<{ pointerId: number; x: number; y: number; moved: boolean }>();
  const nodeGesture = useRef<{ node: Node; pointerId: number; x: number; y: number; offsetX: number; offsetY: number; moved: boolean }>();
  const ignoreClick = useRef('');
  const pendingDomainClick = useRef<number>();
  const [scale, setScale] = useState(1);
  const [hoverId, setHover] = useState('');
  const [focusedId, setFocused] = useState('');
  const [dragging, setDragging] = useState(false);
  const [nodeDrag, setNodeDrag] = useState<NodeDrag>();
  const [cut,setCut]=useState<Cut>('structure');
  const { cameraRef, cameraMoving, target: cameraTarget, depth, depthRequested, tiltTo, follow, advanceCamera } =
    useOrbitCamera(skipMotion, () => renderer.current?.draw());
  const rotate = useRef<{ x: number; y: number; tilt: number; yaw: number; pointer: number; moved: boolean; expanded: boolean }>();
  const hoverFrame = useRef(0);
  const hoverPoint = useRef<Point>();
  const dragFrame = useRef(0);
  const dragPoint = useRef<NodeDrag>();
  useEffect(() => () => {
    cancelAnimationFrame(hoverFrame.current);
    cancelAnimationFrame(dragFrame.current);
  }, []);
  const focusBranch = depth && !view.domain ? selectedId : '';
  const room=useMemo(()=>hierarchical ? projectIndex(graph,view.domain,depth,focusBranch,cut) : null,[graph,view.domain,hierarchical,depth,focusBranch,cut]);
  const flatRoom = useMemo(() => hierarchical ? projectIndex(graph,view.domain,false,'') : null, [graph,view.domain,hierarchical]);
  const lastRoom = useRef<{ cut: Cut; depth: boolean; frame?: OrbitFrame; nodes: Map<string, CanvasNode> }>();
  const flatLayoutCache = useRef<{graph:Graph; bounds:string; layout:ReturnType<typeof layoutPortalRoom>}>();
  const navigation = useRef({ zoom: 1, panX: 0, panY: 0 });
  const models=useMemo(()=>room?.nodes ?? viewModels(graph,view.domain,false),[room,graph,view.domain]);
  const [serverContext,setServerContext]=useState<{id:string;run:typeof loadContext;value:BudgetContext}>();
  const [contextFailure,setContextFailure]=useState('');
  const portalIds = useMemo(() => room?.enterable ?? new Set(
    graph.nodes.filter(node => node.node_type === 'Domain').map(node => node.id)), [graph.nodes, room]);
  const parentId = hierarchical ? graph.nodes.find(node => node.id === view.domain)?.domain_id ?? '' : '';
  const identity = hierarchical ? indexEdgeKey : edgeKey;
  const traversal = useMemo(() => createBudgetTraversal(graph, hierarchical ? indexEdgeKey : undefined), [graph, hierarchical]);
  const hot = active && !nodeDrag && !dragging ? hoverId || focusedId : '';
  const contextCache = useMemo(()=>new Map<string,Promise<BudgetContext>>(),[loadContext]);
  useEffect(()=>{
    setContextFailure('');
    if(!hot || !loadContext) return;
    let current=true;
    const timer=window.setTimeout(()=>{
      let query=contextCache.get(hot);
      if (!query) {
        if (contextCache.size>=64) contextCache.delete(contextCache.keys().next().value!);
        query=loadContext(hot); contextCache.set(hot,query);
      }
      query.then(value=>{if(current)setServerContext({id:hot,run:loadContext,value});})
        .catch(()=>{contextCache.delete(hot);if(current)setContextFailure('暂时无法读取关联子图');});
    },contextCache.has(hot)?0:65);
    return ()=>{current=false;window.clearTimeout(timer);};
  },[hot,loadContext,contextCache]);
  const context = useMemo(() => loadContext ? (serverContext?.id===hot && serverContext.run===loadContext ? serverContext.value : null) :
    hot ? traversal(hot) : null, [hot, traversal,loadContext,serverContext]);
  const appearance = useRef({ selectedId, hot, context, draftNodeIds, active, skipMotion });
  appearance.current = { selectedId, hot, context, draftNodeIds, active, skipMotion };
  const enteredAt = useRef(performance.now());
  const minimumScale = useRef(0.38);

  useLayoutEffect(() => {
    const host = layer.current;
    const element = canvas.current;
    const ctx = element?.getContext('2d');
    if (!host || !element || !ctx) return;

    let home: Viewport = { scale: 1, x: 0, y: 0 };
    let viewport = home;
    let area = { x: 0, y: 0, width: 1, height: 1 };
    let width = 1;
    let height = 1;
    let ratio = 1;
    let frame = 0;
    let idleTimer: number | undefined;
    let lastContext: BudgetContext | null = null;
    let displayedContext: BudgetContext | null = null;
    let fadeFrom = 0;
    let fadeTo = 0;
    let faded = 0;
    let fadeStart = 0;
    let labelsVisible = viewport.scale >= edgeLabelScale;
    let labelsShownAt = -Infinity;
    let screenNodes = new Map<string, CanvasNode>();
    let positions: Position[] = [];
    const byId = new Map(graph.nodes.map(node => [node.id, node]));
    const labelWidths = new Map<string, number>();
    function labelWidth(model: Node): number {
      const previous = labelWidths.get(model.id);
      if (previous !== undefined) return previous;
      const domain = model.node_type === 'Domain';
      ctx!.font = nodeFont(domain);
      const width = Math.min(domain ? 135 : 130, ctx!.measureText(model.label).width) + 16;
      labelWidths.set(model.id, width);
      return width;
    }
    models.forEach(labelWidth);
    const fittedNames = new Map<string, NodeName>();
    const controlGeometry = new WeakMap<HTMLButtonElement,string>();
    let orbitScene: OrbitScene | undefined;
    let orbitFrame: OrbitFrame | undefined;
    let lastPose: typeof cameraRef.current | undefined;
    const previousRoom = lastRoom.current;
    const swappingCut = depth && previousRoom?.depth && previousRoom.cut !== cut && !skipMotion;
    let switchStart: number | undefined;
    let blend = swappingCut ? 0 : 1;
    function depthProgress(){return cameraRef.current.progress;}
    function displayScale(){
      const t=depthProgress();
      return !t || !orbitFrame ? viewport.scale : viewport.scale*(1-t)+(viewport.scale/home.scale)*Math.min(1.15,orbitFrame.scale)*t;
    }
    function nodeName(model: Node): NodeName {
      // Text metrics need not be remeasured for subpixel changes of the lens.
      // Dense decks declutter screen-space captions separately. A distant
      // sphere can still carry a readable business name when space permits.
      const labelScale = depth && models.length > 8
        ? Math.max(displayScale(), .8 * depthProgress())
        : displayScale();
      const size = Math.max(.08, Math.round(labelScale * 32) / 32);
      const key = model.id+'/'+size;
      let name = fittedNames.get(key);
      if (!name) {
        name = fitNodeName(ctx!, model, labelWidth(model), size);
        fittedNames.set(key, name);
      }
      return name;
    }
    let cachedPreview: { context: BudgetContext; positions: Position[]; nodes: ReturnType<typeof layoutPortalPreview> } | undefined;
    const visibleIds = new Set(models.map(model => model.id));
    const flatModels = flatRoom?.nodes ?? models;
    const flatIds = new Set(flatModels.map(node => node.id));
    const roomEdges = room?.edges ?? graph.edges.filter(edge => visibleIds.has(edge.src_id) && visibleIds.has(edge.dst_id));
    let cachedEdges: {
      nodes: Map<string, CanvasNode>; context: BudgetContext | null; hot: BudgetContext | null;
      showLabels: boolean; layoutKey: string; drafts: ReadonlySet<string>; paths: Map<string, Curve>; labels: Map<string, CaptionPosition>;
      captions: (Caption & { text: string; clause: string })[];
    } | undefined;
    // Nested relationships inform placement without turning them into visible relationships between containers.
    const domainOverview = !view.domain && roomEdges.length === 0 && models.every(node => node.node_type === 'Domain');
    const layoutLinks = portalLayoutLinks(graph.edges, byId, visibleIds);
    const contextIds = new Set(models.filter(node => view.domain && node.domain_id !== view.domain).map(node => node.id));
    const storageKey=spaceKey ? 'ontology-layout:'+JSON.stringify([spaceKey,view.domain]) : '';
    let saved:Record<string,Point>={};
    if(storageKey) try { saved=JSON.parse(localStorage.getItem(storageKey) ?? '{}') || {}; } catch { /* Private mode. */ }
    let hotEdge='';
    const workspace = host.closest('.canvas-workspace');
    let layoutKey = '';

    function layout() {
      const rect = host!.getBoundingClientRect();
      width = Math.max(1, rect.width);
      height = Math.max(1, rect.height);
      ratio = Math.min(2, window.devicePixelRatio || 1);
      const padding = Math.min(24, width * 0.045, height * 0.045);
      let top = padding, bottom = height - padding;
      for (const bar of workspace?.querySelectorAll('.canvas-toolbar, .draft-bar, .portal-hint') ?? []) {
        top = Math.max(top, bar.getBoundingClientRect().bottom - rect.top + padding / 2);
      }
      for (const bar of [workspace?.querySelector('.graph-legend'), host!.querySelector('.portal-zoom')]) {
        if (bar) bottom = Math.min(bottom, bar.getBoundingClientRect().top - rect.top - padding / 2);
      }
      // Very shallow windows still need a navigable slice of the graph.
      top = Math.min(top, height * 0.48);
      bottom = Math.max(bottom, height * 0.60);
      area = { x: padding, y: top, width: Math.max(1, width - 2 * padding), height: Math.max(1, bottom - top) };
      const key = [width, height, ratio, top, bottom].join('/');
      if (key === layoutKey) return;
      layoutKey = key;
      if (element!.width !== Math.round(width * ratio)) element!.width = Math.round(width * ratio);
      if (element!.height !== Math.round(height * ratio)) element!.height = Math.round(height * ratio);
      const flatLinks = depth ? portalLayoutLinks(graph.edges, byId, flatIds) : layoutLinks;
      const flatContext = depth ? new Set(flatModels.filter(n => view.domain && n.domain_id !== view.domain).map(n=>n.id)) : contextIds;
      const overview = depth ? !view.domain && flatModels.every(n => n.node_type === 'Domain') && !flatRoom?.edges.length : domainOverview;
      flatModels.forEach(labelWidth);
      let flatLayout = flatLayoutCache.current?.graph===graph && flatLayoutCache.current.bounds===key ? flatLayoutCache.current.layout : undefined;
      if (!flatLayout) {
        flatLayout=layoutPortalRoom(flatModels, flatLinks, labelWidths, area, flatContext, overview);
        flatLayoutCache.current={graph,bounds:key,layout:flatLayout};
      }
      const flatPositions = new Map(flatLayout.positions.map(p => [p.id, p]));
      const center = {x:area.x+area.width/2,y:area.y+area.height/2};
      positions = models.map(model => {
        const origin = flatPositions.get(model.id) ?? flatPositions.get(model.domain_id) ?? flatPositions.get(view.domain) ?? center;
        const p = { id:model.id, model, x:origin.x, y:origin.y };
        const pin=saved[p.id]; if(!pin || !Number.isFinite(pin.x)|| !Number.isFinite(pin.y)) return p;
        return {...p,x:p.x+pin.x*area.width,y:p.y+pin.y*area.height};
      });
      if (depth) orbitScene = buildOrbitScene(positions, area, cut, labelWidths);
      lastPose = undefined;
      const fit = flatLayout.fitScale;
      home = {
        scale: fit,
        x: (area.x + area.width / 2 - width / 2) * (1 - fit),
        y: (area.y + area.height / 2 - height / 2) * (1 - fit),
      };
      minimumScale.current = Math.min(0.38, fit);
      const { zoom, panX, panY } = navigation.current;
      viewport = {scale: fit*zoom, x:home.x*zoom+panX, y:home.y*zoom+panY};
      setScale(viewport.scale);
      requestDraw();
    }

    function rememberView() {
      const zoom = viewport.scale/home.scale;
      navigation.current = {zoom,panX:viewport.x-home.x*zoom,panY:viewport.y-home.y*zoom};
    }

    function screenPoint(point: Point) {
      return {
        x: width / 2 + (point.x - width / 2) * viewport.scale + viewport.x,
        y: height / 2 + (point.y - height / 2) * viewport.scale + viewport.y,
      };
    }

    function slicedPoint(point:Point){
      return screenPoint({x:width/2+(point.x-width/2-home.x)/home.scale,y:height/2+(point.y-height/2-home.y)/home.scale});
    }
    function projected(point:Point, level:number, id:string) {
      const t=depthProgress();
      if(!t || !orbitFrame) return point;
      const target=orbitFrame.points.get(id) ?? orbitFrame.plates.find(p=>p.level===level)?.center;
      if(!target) return point;
      const end=slicedPoint(target);
      let x=point.x+(end.x-point.x)*t, y=point.y+(end.y-point.y)*t;
      if (blend<1) {
        const start=previousRoom?.nodes.get(id) ?? slicedPoint(orbitFrame.plates.find(p=>p.level===level)!.center);
        x=start.x+(x-start.x)*blend; y=start.y+(y-start.y)*blend;
      }
      return {x,y};
    }

    function render(now: number) {
      frame = 0;
      advanceCamera(now);
      if (orbitScene && lastPose!==cameraRef.current) {
        lastPose=cameraRef.current;
        orbitFrame=projectOrbitScene(orbitScene,area,lastPose);
      }
      if (switchStart===undefined) switchStart=now;
      const fraction=Math.min(1,Math.max(0,(now-switchStart)/410));
      blend = swappingCut ? fraction*fraction*(3-2*fraction) : 1;
      ctx!.setTransform(ratio, 0, 0, ratio, 0, 0);
      ctx!.clearRect(0, 0, width, height);
      const settings = appearance.current;
      if (settings.context !== lastContext) {
        lastContext = settings.context;
        if (lastContext) displayedContext = lastContext;
        fadeFrom = faded;
        fadeTo = lastContext ? 1 : 0;
        fadeStart = now;
      }
      const elapsed = settings.skipMotion ? 1 : Math.min(1, (now - fadeStart) / 155);
      faded = fadeFrom + (fadeTo - fadeFrom) * (1 - (1 - elapsed) ** 3);
      if (!lastContext && faded === 0) displayedContext = null;
      const focusedNodes = new Set(displayedContext?.subgraph.nodes.map(node => node.id));
      const focusedEdges = new Set(displayedContext?.subgraph.edges.map(identity));
      for(const e of roomEdges) if(Array.isArray(e.metadata.members) && e.metadata.members.some(id=>focusedEdges.has('id:'+id))) {
        focusedEdges.add(identity(e)); focusedNodes.add(e.src_id); focusedNodes.add(e.dst_id);
      }
      const glowingDomains = crossDomainTargets(displayedContext?.subgraph.edges ?? [], byId, portalIds);
      const activeMotion = settings.active && !settings.skipMotion;
      const showLabels = !cameraMoving.current && blend===1 && viewport.scale >= edgeLabelScale && (!depth || roomEdges.length<14 || viewport.scale>home.scale*1.4);
      if (showLabels && !labelsVisible) labelsShownAt = now;
      labelsVisible = showLabels;
      const labelFade = !showLabels ? 0 : settings.skipMotion ? 1 : Math.min(1, (now - labelsShownAt) / 130);

      const progress = !view.entrance || settings.skipMotion ? 1 : Math.min(1, Math.max(0, (now - enteredAt.current) / portalDuration));
      const spread = 1 - (1 - progress) ** 3 * Math.cos(5.5 * progress);
      const detailScale = Math.min(1, displayScale());
      const lift = depthProgress();
      const reveal = (id: string) => !depth || flatIds.has(id) ? 1 : lift;
      screenNodes = new Map();
      if (orbitFrame) {
        if (blend<1 && previousRoom?.frame) drawOrbitPlates(ctx!,previousRoom.frame,(1-blend)*depthProgress(),slicedPoint);
        drawOrbitPlates(ctx!,orbitFrame,blend*depthProgress(),slicedPoint);
      }
      for (const node of positions) {
        const point = projected(screenPoint(node),plane(node.model,cut),node.model.id);
        if (view.entrance && progress < 1) {
          point.x = view.entrance.x + (point.x - view.entrance.x) * spread;
          point.y = view.entrance.y + (point.y - view.entrance.y) * spread;
        }
        const orbitNode=orbitFrame?.points.get(node.id);
        let size=viewport.scale;
        if (orbitNode) size+=(Math.min(1.28,orbitNode.scale)*viewport.scale/home.scale-size)*lift;
        let radius = radii[node.model.node_type] * size * (.45 + .55 * reveal(node.id));
        if (blend<1) {
          const oldRadius=previousRoom?.nodes.get(node.id)?.radius ?? radius*.5;
          radius=oldRadius+(radius-oldRadius)*blend;
        }
        const target = controls.current.get(node.id);
        if (target) {
          const hidden = reveal(node.id) < .12;
          if (target.hidden !== hidden) target.hidden = hidden;
          const diameter = Math.max(radius * 2 + 12 * detailScale, 44 * detailScale);
          const name = nodeName(node.model);
          const order = orbitNode ? Math.round(orbitNode.depth*4) : 0;
          const geometry = [point.x,point.y,diameter,order,name.width,detailScale].join('/');
          if (controlGeometry.get(target)!==geometry) {
            controlGeometry.set(target,geometry);
            target.style.zIndex=orbitNode ? String(10000+order) : '';
            target.style.transform = `translate3d(${point.x-diameter/2}px,${point.y-diameter/2}px,0)`;
            target.style.width=target.style.height=`${diameter}px`;
            target.style.setProperty('--portal-inner-diameter',`${Math.max(0,radius*2-2)}px`);
            target.style.setProperty('--portal-label-width',`${name.width+6*detailScale}px`);
            target.style.setProperty('--portal-label-height',`${name.height+6*detailScale}px`);
            target.style.setProperty('--portal-label-top',`${diameter/2+radius+name.offset-3*detailScale}px`);
          }
        }
        screenNodes.set(node.id, { ...point, radius, model: node.model });
      }

      lastRoom.current={depth,cut,nodes:screenNodes,frame:orbitFrame};
      storeAnchors(view.domain, screenNodes);
      const previews = new Map<string, CanvasNode>();
      if (displayedContext && !depth) {
        if (!cachedPreview || cachedPreview.context !== displayedContext || cachedPreview.positions !== positions) {
          const w = area.width / home.scale, h = area.height / home.scale;
          const usable = { x: area.x + (area.width - w) / 2, y: area.y + (area.height - h) / 2, width: w, height: h };
          cachedPreview = { context: displayedContext, positions,
            nodes: layoutPortalPreview(displayedContext, positions, byId, view.domain, usable, labelWidth) };
        }
        for (const [id, node] of cachedPreview.nodes) {
          previews.set(id, { ...projected(screenPoint(node),plane(node.model,cut),node.model.id), model: node.model, radius: radii[node.model.node_type] * displayScale() });
        }
      }
      const all = new Map([...screenNodes, ...previews]);
      const displayEdges = new Map(roomEdges.map(edge => [identity(edge), edge]));
      for (const edge of displayedContext?.subgraph.edges ?? []) displayEdges.set(identity(edge), edge);
      const validCache = cachedEdges && cachedEdges.context === displayedContext && cachedEdges.hot === lastContext &&
        cachedEdges.showLabels === showLabels && cachedEdges.layoutKey === layoutKey && cachedEdges.drafts === settings.draftNodeIds &&
        cachedEdges.nodes.size === all.size && [...all].every(([id, node]) => {
          const old = cachedEdges!.nodes.get(id);
          return old && old.x === node.x && old.y === node.y && old.radius === node.radius;
        });
      if (!validCache) {
        const obstacles = new Map<string, RouteNode>();
        for (const [id, node] of all) {
          const domain = node.model.node_type === 'Domain';
          const name = nodeName(node.model);
          const ringMargin = settings.draftNodeIds.has(id) ? (domain ? 15 : 10) : domain ? 10 : portalIds.has(id) ? 5 : 0;
          obstacles.set(id, {
            ...node, ring: node.radius + ringMargin * detailScale,
            labelWidth: name.width + 4, labelOffset: name.offset, labelHeight: name.height,
          });
        }
        const paths = (depth ? routeOrbitEdges : routeEdges)(obstacles, [...displayEdges].map(([key, edge]) => ({
          key, source: edge.src_id, target: edge.dst_id,
        })), area);
        const captions: (Caption & { text: string; clause: string })[] = [];
        for (const [key, edge] of displayEdges) {
          const hot = hotEdge===key || !!lastContext && focusedEdges.has(key);
          if (!paths.has(key) || !edge.label_zh || (!hot && !showLabels)) continue;
          const clause = hot && edge.predicate === 'foreign_key_to' && typeof edge.metadata.join_clause === 'string' ? edge.metadata.join_clause : '';
          ctx!.font = captionFont(hot);
          const width = Math.min(145, ctx!.measureText(edge.label_zh).width) + 16;
          ctx!.font = clauseFont;
          const clauseWidth = clause ? Math.min(245, ctx!.measureText(clauseText(clause)).width) + 16 : 0;
          captions.push({ key, hot, text: edge.label_zh, clause, width, clauseWidth });
        }
        const labels = placeCaptions(obstacles, paths, captions, area);
        cachedEdges = { nodes: all, context: displayedContext, hot: lastContext, showLabels, layoutKey, drafts: settings.draftNodeIds,
          paths, labels, captions: captions.sort((a, b) => Number(a.hot) - Number(b.hot)) };
      }

      const scene = cachedEdges!;
      const opacity = (key: string, edge: Edge) => {
        if (!lastContext && (previews.has(edge.src_id) || previews.has(edge.dst_id))) return faded;
        return focusedEdges.has(key) ? 1 : (depth && roomEdges.length>=14 ? .36 : .76) * (1 - faded) + inactiveOpacity * faded;
      };
      for (const [key, edge] of displayEdges) {
        const path = scene.paths.get(key);
        if (path) drawEdge(ctx!, edge, path, {
          alpha: (hotEdge===key ? 1 : opacity(key, edge)) * Math.min(reveal(edge.src_id), reveal(edge.dst_id)), highlighted: hotEdge===key || !!lastContext && focusedEdges.has(key), motion: activeMotion, now,
          draft: !!draftEdgeIds?.has(edge.id ?? '') || (Array.isArray(edge.metadata.members) && edge.metadata.members.some(id=>draftEdgeIds?.has(id))),
        });
      }
      for (const caption of scene.captions) {
        const point = scene.labels.get(caption.key)!;
        const alpha = caption.hot ? 1 : opacity(caption.key, displayEdges.get(caption.key)!) * labelFade;
        const edge = displayEdges.get(caption.key)!;
        if (alpha > 0) drawEdgeCaption(ctx!, caption.text, point, alpha * Math.min(reveal(edge.src_id), reveal(edge.dst_id)), caption.hot, caption.clause);
      }
      const compactLabels = depth && all.size > 8 && depthProgress() > .45;
      const visibleNames = compactLabels ? visibleOrbitNames(Array.from(all, ([id, n]) => ({
        id, x:n.x, y:n.y, radius:n.radius, name:nodeName(n.model),
      })), area) : null;
      for (const [id, target] of controls.current) {
        const hidden = !!visibleNames && !visibleNames.has(id);
        if (hidden !== target.hasAttribute('data-label-hidden')) target.toggleAttribute('data-label-hidden', hidden);
      }
      const drawOrder = depth && orbitFrame ? [...all].sort(([a],[b])=>
        (orbitFrame!.points.get(a)?.depth ?? 0) - (orbitFrame!.points.get(b)?.depth ?? 0)) : all;
      for (const [id, node] of drawOrder) {
        const normalOpacity = node.model.status === 'deprecated' ? 0.43 : 1;
        const preview = previews.has(id);
        drawNode(ctx!, node, visibleNames && (!visibleNames.has(id) || id === settings.hot) ? undefined : nodeName(node.model), {
          alpha: reveal(id) * (room?.remote.has(id) && !focusedNodes.has(id) ? 0.58 : preview && !lastContext ? faded :
            focusedNodes.has(id) ? 1 : normalOpacity * (1 - faded) + inactiveOpacity * faded),
          lift:depthProgress(), picked: id === settings.selectedId,
          focused: id === displayedContext?.seed_node_id,
          glow: !!lastContext && glowingDomains.has(id), draft: settings.draftNodeIds.has(id), portal: portalIds.has(id),
          ghost: preview, now, motion: activeMotion,
        });
        const count=room?.artifacts.get(id)?.size;
        if(count && node.model.node_type!=='Domain' && !depth) {
          const x=node.x+node.radius*.8,y=node.y-node.radius*.75;
          ctx!.save();ctx!.globalAlpha=focusedNodes.has(id)?1:1-faded*.8;
          ctx!.fillStyle='#24384e';ctx!.beginPath();ctx!.arc(x,y,8,0,Math.PI*2);ctx!.fill();
          ctx!.font='10px system-ui';ctx!.textAlign='center';ctx!.fillStyle='#e4f5ee';ctx!.fillText(String(count),x,y+3);ctx!.restore();
        }
      }
      const named = compactLabels ? all.get(settings.hot) : undefined;
      if (named && !cameraMoving.current) drawFocusedName(ctx!, named, area);
      if (activeMotion) {
        if (blend<1 || cameraMoving.current || progress<1 || elapsed<1 || (showLabels && labelFade<1)) requestDraw();
        else if (hotEdge || lastContext || settings.draftNodeIds.size) {
          // A settled proposal only pulses its dashed outlines. Pointer input
          // interrupts this idle cadence and gets the very next animation frame.
          idleTimer=window.setTimeout(requestDraw, lastContext || hotEdge ? 24 : 67);
        }
      }
    }

    function requestDraw() {
      window.clearTimeout(idleTimer);
      if (!frame) frame = requestAnimationFrame(render);
    }

    const api: Renderer = {
      draw: requestDraw,
      zoom(factor, origin = { x: width / 2, y: height / 2 }) {
        const scale = Math.min(2.5, Math.max(minimumScale.current, viewport.scale * factor));
        const relative = scale / viewport.scale;
        viewport = {
          scale,
          x: origin.x - width / 2 - (origin.x - width / 2 - viewport.x) * relative,
          y: origin.y - height / 2 - (origin.y - height / 2 - viewport.y) * relative,
        };
        rememberView();
        requestDraw();
        return scale;
      },
      reset() {
        viewport = home;
        rememberView();
        setScale(home.scale);
        requestDraw();
      },
      pan(dx, dy) {
        viewport = { ...viewport, x: viewport.x + dx, y: viewport.y + dy };
        rememberView();
        requestDraw();
      },
      pin(id,x,y) {
        const point=screenNodes.get(id), p=positions.find(n=>n.id===id);
        if(!point || !p || !storageKey || cameraRef.current.tilt) return;
        const dx=(x-point.x)/viewport.scale,dy=(y-point.y)/viewport.scale;
        const old=saved[id] ?? {x:0,y:0};
        saved[id]={x:old.x+dx/area.width,y:old.y+dy/area.height};
        positions=positions.map(n=>n.id===id ? {...n,x:n.x+dx,y:n.y+dy} : n);
        try { localStorage.setItem(storageKey, JSON.stringify(saved)); } catch { /* Session-only positioning. */ }
        requestDraw();
      },
      hitEdge(x,y) {
        let nearest='',distance=8;
        for(const [key,path] of cachedEdges?.paths ?? []) {
          for(let i=0;i<=32;i++) {
            const p=pointOnEdge(path,i/32),d=Math.hypot(p.x-x,p.y-y);
            if(d<distance) {nearest=key;distance=d;}
          }
        }
        if(hotEdge!==nearest) {hotEdge=nearest;cachedEdges=undefined;requestDraw();}
      },
      anchor(id) {
        return screenNodes.get(id);
      },
    };
    renderer.current = api;
    layout();

    const observer = new ResizeObserver(layout);
    const observeChrome = () => {
      observer.disconnect();
      observer.observe(host);
      for (const bar of workspace?.querySelectorAll('.canvas-toolbar, .draft-bar, .portal-hint, .graph-legend') ?? []) observer.observe(bar);
      layout();
    };
    observeChrome();
    const chromeChanges = new MutationObserver(observeChrome);
    if (workspace) chromeChanges.observe(workspace, { childList: true });
    return () => {
      observer.disconnect();
      chromeChanges.disconnect();
      window.clearTimeout(idleTimer);
      if (frame) cancelAnimationFrame(frame);
      renderer.current = undefined;
      storeAnchors(view.domain, new Map());
    };
  // Camera gestures redraw the settled room without recomputing its layout.
  }, [graph, hierarchical, identity, models, portalIds, skipMotion, storeAnchors, view, room, flatRoom, cut, depth, draftEdgeIds, spaceKey]);

  useLayoutEffect(() => renderer.current?.draw(), [selectedId, hot, context, draftNodeIds, active]);
  const cancelDomainClick = useCallback(() => {
    if (pendingDomainClick.current !== undefined) window.clearTimeout(pendingDomainClick.current);
    pendingDomainClick.current = undefined;
  }, []);

  useEffect(() => cancelDomainClick, [active, cancelDomainClick]);
  useEffect(() => {
    if (!active) {
      setHover('');
      setFocused('');
    }
    const host = layer.current;
    if (!host || !active) return;
    const wheel = (event: WheelEvent) => {
      if (!(event.target instanceof HTMLElement) || event.target.closest('.portal-zoom')) return;
      event.preventDefault();
      const bounds = host.getBoundingClientRect();
      const origin = { x: event.clientX - bounds.left, y: event.clientY - bounds.top };
      const value = renderer.current?.zoom(Math.exp(-event.deltaY * 0.00125), origin);
      if (value !== undefined) setScale(value);
    };
    host.addEventListener('wheel', wheel, { passive: false });
    return () => host.removeEventListener('wheel', wheel);
  }, [active]);

  function clearHover() {
    if (hoverFrame.current) cancelAnimationFrame(hoverFrame.current);
    hoverFrame.current = 0;
    hoverPoint.current = undefined;
    setHover('');
    renderer.current?.hitEdge(-100, -100);
  }

  function settleOrbit() {
    const pose = cameraTarget.current;
    if (pose.tilt < .105) tiltTo(0, 0);
    else if (pose.progress < 1) tiltTo(pose.tilt, pose.yaw);
  }

  useEffect(() => {
    if (!active) return;
    function interrupt() {
      const rotating = rotate.current?.moved;
      const captured = rotate.current?.pointer ?? gesture.current?.pointerId;
      rotate.current = undefined;
      gesture.current = undefined;
      const node = nodeGesture.current;
      nodeGesture.current = undefined;
      const target = node ? controls.current.get(node.node.id) : layer.current;
      const pointer = node?.pointerId ?? captured;
      if (pointer !== undefined && target?.hasPointerCapture(pointer)) target.releasePointerCapture(pointer);
      if (rotating) settleOrbit();
      if (node?.moved) ignoreClick.current = node.node.id;
      cancelAnimationFrame(dragFrame.current);
      dragFrame.current = 0;
      dragPoint.current = undefined;
      setNodeDrag(undefined);
      setDragging(false);
      clearHover();
      cancelDomainClick();
    }
    function visibility() { if (document.hidden) interrupt(); }
    window.addEventListener('blur', interrupt);
    document.addEventListener('visibilitychange', visibility);
    return () => {
      window.removeEventListener('blur', interrupt);
      document.removeEventListener('visibilitychange', visibility);
      interrupt();
    };
  }, [active, skipMotion, cancelDomainClick]);

  function panStart(event: ReactPointerEvent<HTMLDivElement>) {
    if (!active || !event.isPrimary || rotate.current || gesture.current || nodeGesture.current) return;
    const element = event.target instanceof Element ? event.target : undefined;
    if (element?.closest('.portal-zoom')) return;
    if (event.button === 2 && hierarchical) {
      const { tilt, yaw } = cameraRef.current;
      rotate.current = { x: event.clientX, y: event.clientY, tilt, yaw, pointer: event.pointerId, moved: false, expanded: cameraRef.current.progress >= .99 };
      event.currentTarget.setPointerCapture(event.pointerId);
      event.preventDefault();
      return;
    }
    if (event.button !== 0 || element?.closest('button')) return;
    gesture.current = { pointerId: event.pointerId, x: event.clientX, y: event.clientY, moved: false };
    event.currentTarget.setPointerCapture(event.pointerId);
  }

  function panMove(event: ReactPointerEvent<HTMLDivElement>) {
    const orbit = rotate.current;
    if (orbit?.pointer === event.pointerId) {
      if (!(event.buttons & 2)) { panEnd(event); return; }
      const dx = event.clientX - orbit.x, dy = orbit.y - event.clientY;
      if (!orbit.moved) {
        if (Math.hypot(dx, dy) < 5) return;
        orbit.moved = true;
        cancelDomainClick();
        clearHover();
        setDragging(true);
      }
      const pose = dragOrbit(orbit, dx, dy, orbit.expanded);
      if (pose.progress === 1) orbit.expanded = true;
      follow(pose);
      return;
    }
    const pan = gesture.current;
    if (pan?.pointerId === event.pointerId) {
      if (!(event.buttons & 1)) { panEnd(event); return; }
      const dx = event.clientX - pan.x, dy = event.clientY - pan.y;
      if (!pan.moved) {
        if (Math.hypot(dx, dy) < 3) return;
        clearHover();
        setDragging(true);
        pan.moved = true;
      }
      pan.x = event.clientX; pan.y = event.clientY;
      renderer.current?.pan(dx, dy);
      return;
    }
    if (event.pointerType !== 'mouse' || event.buttons || nodeGesture.current || cameraMoving.current) return;
    const target = event.target instanceof Element ? event.target : undefined;
    const id = target?.closest<HTMLElement>('[data-node-id]')?.dataset.nodeId ?? '';
    setHover(id);
    // A pointer can send several samples between rendered frames. Test edges
    // only at the latest actual pointer position, never on hover-induced reflow.
    hoverPoint.current = target?.closest('button,select,.portal-zoom') ? undefined : { x: event.clientX, y: event.clientY };
    if (!hoverFrame.current) hoverFrame.current = requestAnimationFrame(() => {
      hoverFrame.current = 0;
      const p = hoverPoint.current;
      const rect = layer.current?.getBoundingClientRect();
      renderer.current?.hitEdge(p && rect ? p.x - rect.left : -100, p && rect ? p.y - rect.top : -100);
    });
  }

  function panEnd(event: ReactPointerEvent<HTMLDivElement>) {
    const orbit = rotate.current;
    if (orbit?.pointer === event.pointerId) {
      rotate.current = undefined;
      if (orbit.moved) settleOrbit();
      setDragging(false);
    }
    if (gesture.current?.pointerId === event.pointerId) {
      gesture.current = undefined;
      setDragging(false);
    }
  }

  function startNodeDrag(event: ReactPointerEvent<HTMLButtonElement>, node: Node) {
    ignoreClick.current = '';
    if(!event.isPrimary || rotate.current || gesture.current || event.button!==0 || depth || (!spaceKey && (!onMoveNode || node.node_type==='Domain' || node.status==='deprecated'))) return;
    const rect = layer.current?.getBoundingClientRect();
    const anchor = renderer.current?.anchor(node.id);
    nodeGesture.current = {
      node, pointerId: event.pointerId, x: event.clientX, y: event.clientY, moved: false,
      offsetX: rect && anchor ? anchor.x + rect.left - event.clientX : 0,
      offsetY: rect && anchor ? anchor.y + rect.top - event.clientY : 0,
    };
    event.currentTarget.setPointerCapture(event.pointerId);
  }

  function dropTarget(event: ReactPointerEvent<HTMLButtonElement>, node: Node): string {
    const target = document.elementFromPoint(event.clientX, event.clientY)?.closest<HTMLButtonElement>(
      '.portal-node-hit[data-node-type="Domain"]:not([data-deprecated])',
    );
    const id = target?.dataset.nodeId;
    return id && id !== node.domain_id && layer.current?.contains(target) ? id : '';
  }

  function moveNodeDrag(event: ReactPointerEvent<HTMLButtonElement>) {
    const drag = nodeGesture.current;
    if (!drag || drag.pointerId !== event.pointerId) return;
    if (!drag.moved && Math.hypot(event.clientX - drag.x, event.clientY - drag.y) < 6) return;
    const bounds = layer.current?.getBoundingClientRect();
    if (!bounds) return;
    cancelDomainClick();
    drag.moved = true;
    dragPoint.current = {
      node: drag.node, targetId: onMoveNode && (!spaceKey || event.shiftKey) ? dropTarget(event, drag.node) : '',
      x: event.clientX - bounds.left + drag.offsetX, y: event.clientY - bounds.top + drag.offsetY,
    };
    if (!dragFrame.current) dragFrame.current = requestAnimationFrame(() => {
      dragFrame.current = 0;
      setNodeDrag(dragPoint.current);
    });
  }

  function endNodeDrag(event: ReactPointerEvent<HTMLButtonElement>) {
    const drag = nodeGesture.current;
    if (!drag || drag.pointerId !== event.pointerId) return;
    nodeGesture.current = undefined;
    if (!drag.moved) return;
    cancelAnimationFrame(dragFrame.current);
    dragFrame.current = 0;
    dragPoint.current = undefined;
    ignoreClick.current = drag.node.id;
    setNodeDrag(undefined);
    if (event.type === 'pointerup') {
      const target = onMoveNode && (!spaceKey || event.shiftKey) ? dropTarget(event, drag.node) : '';
      if (target) onMoveNode?.(drag.node.id, target);
      else if(spaceKey) {
        const bounds = layer.current?.getBoundingClientRect();
        if (bounds) renderer.current?.pin(drag.node.id, event.clientX - bounds.left + drag.offsetX, event.clientY - bounds.top + drag.offsetY);
      }
    }
  }

  function zoom(factor: number) {
    const value = renderer.current?.zoom(factor);
    if (value !== undefined) setScale(value);
  }

  function openDomain(id: string) {
    cancelDomainClick();
    navigate(id, renderer.current?.anchor(id));
  }

  const canEnter = (id: string) => id !== view.domain && portalIds.has(id);

  function selectNode(node: Node, clickCount: number, tap: boolean) {
    cancelDomainClick();
    if (!canEnter(node.id) || clickCount === 0 || tap) {
      onSelect(node.id);
    } else if (clickCount === 1) {
      // A label and its circle share one target. Let a second mouse click
      // dispatch dblclick before a single-click inspector can obscure it.
      pendingDomainClick.current = window.setTimeout(() => {
        pendingDomainClick.current = undefined;
        onSelect(node.id);
      }, domainClickDelay);
    }
  }

  return (
    <div
      ref={layer} className={`portal-layer${phase ? ` portal-layer--${phase}` : ''}${dragging || nodeDrag ? ' portal-layer--dragging' : ''}`}
      aria-hidden={!active || undefined} data-domain={view.domain} data-highlighted-node={hot || undefined}
      data-cut={depth ? cut : 'flat'} onContextMenu={event=>{if(hierarchical)event.preventDefault();}}
      onPointerLeave={clearHover} onLostPointerCapture={panEnd}
      onPointerDown={panStart} onPointerMove={panMove} onPointerUp={panEnd} onPointerCancel={panEnd}
      onAnimationEnd={event => {
        if (event.target === event.currentTarget && phase && event.animationName === (phase === 'enter' ? 'portal-open' : 'portal-close')) {
          finishTransition();
        }
      }}
      onDoubleClick={event => {
        if (view.domain && !(event.target instanceof Element && event.target.closest('button'))) {
          cancelDomainClick();
          navigate(parentId);
        }
      }}
      onKeyDown={event => {
        if (event.key === 'Escape') {
          cancelDomainClick();
          if (!selectedId && view.domain) navigate(parentId);
        }
      }}
    >
      <canvas ref={canvas} className="portal-canvas" aria-hidden="true" />
      {active && (
        <>
          <div className="portal-hits" role="group" aria-label="本体图谱节点">
            {models.map(model => (
              <button
                key={model.id} ref={element => {
                  if (element) controls.current.set(model.id, element);
                  else controls.current.delete(model.id);
                }}
                type="button" className="portal-node-hit" data-node-id={model.id}
                data-node-type={model.node_type}
                data-portal-target={canEnter(model.id) || undefined}
                data-deprecated={model.status === 'deprecated' || undefined}
                data-draft={draftNodeIds.has(model.id) || undefined}
                data-drop-target={nodeDrag?.targetId === model.id || undefined}
                aria-label={`${typeLabels[model.node_type]} ${model.label}`}
                aria-pressed={selectedId === model.id}
                title={`${model.label}\n${canEnter(model.id) ? (hierarchical ? '查看详情，双击进入子图' : '查看详情，双击进入领域') :
                  (onMoveNode ? '查看详情，可拖到其他领域' : '查看节点详情')}`}
                onPointerEnter={event => { if (event.pointerType === 'mouse' && !event.buttons && !cameraMoving.current && !rotate.current) setHover(model.id); }}
                onFocus={event => setFocused(event.currentTarget.matches(':focus-visible') ? model.id : '')} onBlur={() => setFocused('')}
                onPointerDown={event => startNodeDrag(event, model)} onPointerMove={moveNodeDrag}
                onPointerUp={endNodeDrag} onPointerCancel={endNodeDrag} onLostPointerCapture={endNodeDrag}
                onClick={event => {
                  if (event.detail > 0 && ignoreClick.current === model.id) return;
                  const pointerType = (event.nativeEvent as PointerEvent).pointerType;
                  const tap = pointerType === 'touch' || (!pointerType && window.matchMedia('(pointer: coarse)').matches);
                  selectNode(model, event.detail, tap);
                }}
                onDoubleClick={event => {
                  event.stopPropagation();
                  if (ignoreClick.current === model.id) return;
                  if (canEnter(model.id)) openDomain(model.id);
                  else onSelect(model.id);
                }}
              />
            ))}
          </div>
          {nodeDrag && <div
            className="portal-node-drag" aria-hidden="true" data-over-domain={!!nodeDrag.targetId || undefined}
            style={{
              left: nodeDrag.x, top: nodeDrag.y, background: nodeColors[nodeDrag.node.node_type],
              width: radii[nodeDrag.node.node_type] * scale * 2, height: radii[nodeDrag.node.node_type] * scale * 2,
            }}
          />}
          {models.length === 0 && <div className="portal-empty">{emptyText ?? (view.domain ? '这个领域还没有本体节点' : '还没有可浏览的本体节点')}</div>}
          {hot && contextFailure && <div className="portal-context" role="status">{contextFailure}</div>}
          {context && (
            <div className="portal-context" role="status" aria-label="最小知识子图"
              data-edge-count={context.subgraph.edges.length}>
              <span className="portal-context__light" />
              <span>预算 {DEFAULT_BUDGET}</span>
              <span>{context.subgraph.nodes.length} 节点</span>
              <span>{context.frontier_nodes.length} 边界</span>
            </div>
          )}
          <div className="portal-zoom" role="group" aria-label="图谱缩放">
            {hierarchical && <div className="portal-view-switch" role="group" aria-label="分层视图">
              {([['flat','平面'],['structure','索引分层'],['causal','业务因果']] as const).map(([mode,label])=>(
                <button type="button" key={mode} title={mode==='flat'?'查看当前层的平面图':mode==='structure'?'按领域、业务与资料分层，右键拖动旋转':'按指标、干预、机制与资料分层，右键拖动旋转'} aria-pressed={mode==='flat' ? !depthRequested : depthRequested && cut===mode}
                  onClick={()=>{
                    if(mode==='flat') tiltTo(0,0);
                    else {setCut(mode); if(!depthRequested) tiltTo(1.02,.32);}
                  }}>{label}</button>
              ))}
            </div>}
            <button type="button" aria-label="缩小图谱"  disabled={scale <= minimumScale.current} onClick={() => zoom(0.83)}>
              <svg viewBox="0 0 16 16" aria-hidden="true"><path d="M3.5 8h9" /></svg>
            </button>
            <button type="button" className="portal-zoom__value" aria-label="重置图谱视图" title="适应当前画布并复位视角" onClick={() => { renderer.current?.reset(); if(depthRequested) tiltTo(1.02,.32); }}>
              {Math.round(scale * 100)}%
            </button>
            <button type="button" aria-label="放大图谱" disabled={scale >= 2.5} onClick={() => zoom(1.2)}>
              <svg viewBox="0 0 16 16" aria-hidden="true"><path d="M3.5 8h9M8 3.5v9" /></svg>
            </button>
          </div>
        </>
      )}
    </div>
  );
}

function viewModels(graph: Graph, domain: string, hierarchical: boolean): Node[] {
  if (hierarchical) {
    const children = graph.nodes.filter(node => node.domain_id === domain);
    if (!domain) return children;
    const byId = new Map(graph.nodes.map(node => [node.id, node]));
    const childIds = new Set(children.map(node => node.id));
    const neighbors = new Set<string>();
    for (const edge of graph.edges) {
      if (childIds.has(edge.src_id) && !childIds.has(edge.dst_id)) neighbors.add(edge.dst_id);
      if (childIds.has(edge.dst_id) && !childIds.has(edge.src_id)) neighbors.add(edge.src_id);
    }
    const contextIds = new Set<string>();
    for (const neighbor of neighbors) {
      const model = byId.get(neighbor);
      const anchor = model?.domain_id && neighbor !== domain ? model.domain_id : neighbor;
      if (!childIds.has(anchor)) contextIds.add(anchor);
    }
    return [...children, ...graph.nodes.filter(node => contextIds.has(node.id))];
  }
  if (!domain) return graph.nodes.filter(node => node.node_type === 'Domain' || !node.domain_id);
  const local = graph.nodes.filter(node => node.domain_id === domain);
  const localIds = new Set(local.map(node => node.id));
  const relatedIds = new Set<string>();
  for (const edge of graph.edges) {
    if (localIds.has(edge.src_id)) relatedIds.add(edge.dst_id);
    if (localIds.has(edge.dst_id)) relatedIds.add(edge.src_id);
  }
  const remote = new Set(graph.nodes.filter(node => relatedIds.has(node.id) && node.domain_id && node.domain_id !== domain).map(node => node.domain_id));
  return [...local, ...graph.nodes.filter(node => node.node_type === 'Domain' && remote.has(node.id))];
}

function domainOf(node: Node | undefined, portalIds: ReadonlySet<string>): string {
  return node && portalIds.has(node.id) ? node.id : node?.domain_id ?? '';
}

function crossDomainTargets(edges: Edge[], all: Map<string, Node>, portalIds: ReadonlySet<string>): Set<string> {
  const result = new Set<string>();
  for (const edge of edges) {
    const from = domainOf(all.get(edge.src_id), portalIds);
    const to = domainOf(all.get(edge.dst_id), portalIds);
    if (to && from !== to) result.add(to);
  }
  return result;
}

function drawPath(ctx: CanvasRenderingContext2D, { start, control, end }: Curve) {
  ctx.beginPath();
  ctx.moveTo(start.x, start.y);
  ctx.quadraticCurveTo(control.x, control.y, end.x, end.y);
}

// Distance along a quadratic curve is not linear in its time parameter.
function uniformLightPoint(path: Curve, now: number): Point {
  const samples = Array.from({ length: 13 }, (_, index) => pointOnEdge(path, index / 12));
  const lengths = samples.slice(1).map((p, i) => Math.hypot(p.x - samples[i].x, p.y - samples[i].y));
  const length = lengths.reduce((sum, part) => sum + part, 0);
  if (length === 0) return path.start;
  let distance = (now * 0.116) % length;
  for (let i = 0; i < lengths.length; i++) {
    const part = lengths[i];
    if (distance < part) {
      const from = samples[i], to = samples[i + 1], progress = distance / part;
      return { x: from.x + (to.x - from.x) * progress, y: from.y + (to.y - from.y) * progress };
    }
    distance -= part;
  }
  return path.end;
}

function drawDefinitionFlow(ctx: CanvasRenderingContext2D, path: Curve, now: number) {
  const progress = (now / 1800) % 1;
  const end = Math.min(1, progress * 1.22);
  const start = Math.max(0, progress * 1.22 - 0.22);
  if (end - start < 0.01) return;
  const tail = pointOnEdge(path, start);
  const head = pointOnEdge(path, end);
  const gradient = ctx.createLinearGradient(tail.x, tail.y, head.x, head.y);
  gradient.addColorStop(0, 'rgb(16 185 129 / 0%)');
  gradient.addColorStop(0.58, 'rgb(71 227 173 / 50%)');
  gradient.addColorStop(1, '#c0ffeb');
  ctx.strokeStyle = gradient;
  ctx.lineWidth = 2.2;
  ctx.lineCap = 'round';
  ctx.beginPath();
  ctx.moveTo(tail.x, tail.y);
  for (let part = 1; part <= 8; part++) {
    const point = pointOnEdge(path, start + (end - start) * part / 8);
    ctx.lineTo(point.x, point.y);
  }
  ctx.stroke();
}

function drawEdge(ctx: CanvasRenderingContext2D, edge: Edge, path: Curve, options: {
  alpha: number; highlighted: boolean; motion: boolean; now: number; draft?:boolean;
}) {
  const triangle = edge.predicate === 'depends_on';
  const impact = edge.predicate === 'impacted_by';
  const foreign = edge.predicate === 'foreign_key_to';
  const strong = edge.predicate === 'defined_in' || edge.predicate === 'configured_by';
  const color = graphColors[edge.predicate];
  const arrival = Math.atan2(path.end.y - path.control.y, path.end.x - path.control.x);
  ctx.save();
  ctx.globalAlpha = options.alpha;
  ctx.strokeStyle = color;
  ctx.fillStyle = color;
  ctx.lineWidth = strong ? (options.highlighted ? 1.35 : 1.03) : options.highlighted ? 2.2 : 1.5;
  const dash = options.draft ? [6,4] : edge.predicate==='collaborates_with' ? [5,4] : impact ? [12, 7] : foreign ? [1, 5, 8, 5] : [];
  if (foreign) ctx.lineCap = 'round';
  ctx.setLineDash(dash);
  drawPath(ctx, path);
  ctx.stroke();
  ctx.setLineDash([]);
  if (impact) {
    const angle = Math.atan2(path.control.y - path.start.y, path.control.x - path.start.x);
    ctx.save();
    ctx.translate(path.start.x, path.start.y);
    ctx.rotate(angle);
    ctx.beginPath();
    ctx.moveTo(4, 0);
    ctx.lineTo(0, -3.5);
    ctx.lineTo(-4, 0);
    ctx.lineTo(0, 3.5);
    ctx.closePath();
    ctx.fillStyle = '#0f172a';
    ctx.fill();
    ctx.stroke();
    ctx.restore();
  }
  if (strong) {
    ctx.beginPath();
    ctx.arc(path.start.x, path.start.y, 2.3, 0, Math.PI * 2);
    ctx.fill();
  }

  ctx.save();
  ctx.translate(path.end.x, path.end.y);
  ctx.rotate(arrival);
  const arrowSize = strong ? 6 : 8;
  ctx.beginPath();
  ctx.moveTo(-arrowSize, -arrowSize * 0.48);
  ctx.lineTo(0, 0);
  ctx.lineTo(-arrowSize, arrowSize * 0.48);
  if (triangle) {
    ctx.closePath();
    ctx.fill();
  } else {
    ctx.stroke();
    if (foreign) {
      ctx.translate(-4.5, 0);
      ctx.stroke(new Path2D('M-7 -3.5L0 0 -7 3.5'));
    }
  }
  ctx.restore();

  if (options.highlighted && options.motion) {
    if (impact) {
      const progress = (options.now / 2400) % 1;
      const point = pointOnEdge(path, progress);
      ctx.globalAlpha = 0.65 * (1 - progress);
      ctx.beginPath();
      ctx.arc(point.x, point.y, 4 + progress * 7, 0, Math.PI * 2);
      ctx.stroke();
    } else if (strong) {
      drawDefinitionFlow(ctx, path, options.now);
    } else {
      const point = uniformLightPoint(path, options.now);
      ctx.shadowBlur = 5;
      ctx.shadowColor = color;
      ctx.fillStyle = '#e9fcff';
      ctx.beginPath();
      ctx.arc(point.x, point.y, 2.7, 0, Math.PI * 2);
      ctx.fill();
    }
  }
  ctx.restore();

}

const captionFont = (hot: boolean) => `${hot ? 650 : 500} 11px system-ui, "PingFang SC", sans-serif`;
const clauseFont = '11px ui-monospace, SFMono-Regular, monospace';
const clauseText = (clause: string) => clause.length > 68 ? clause.slice(0, 67) + '…' : clause;

function drawEdgeCaption(ctx: CanvasRenderingContext2D, label: string, point: CaptionPosition, opacity: number, highlighted: boolean, clause: string) {
  ctx.save();
  ctx.globalAlpha = opacity;
  if (point.anchor) {
    ctx.strokeStyle = '#697895';
    ctx.lineWidth = 0.8;
    ctx.beginPath();
    ctx.moveTo(point.anchor.x, point.anchor.y);
    ctx.lineTo(point.x, point.y);
    ctx.stroke();
  }
  ctx.font = captionFont(highlighted);
  ctx.textAlign = 'center';
  ctx.textBaseline = 'middle';
  const width = Math.min(145, ctx.measureText(label).width) + 16;
  ctx.fillStyle = highlighted ? 'rgb(32 46 69 / 88%)' : 'rgb(21 31 51 / 82%)';
  ctx.beginPath();
  ctx.roundRect(point.x - width / 2, point.y - 10, width, 20, 10);
  ctx.fill();
  ctx.fillStyle = highlighted ? '#f0f5ff' : '#c1d0ec';
  ctx.fillText(label, point.x, point.y, 145);
  if (clause) {
    const normalized = clauseText(clause);
    ctx.font = clauseFont;
    const textWidth = Math.min(245, ctx.measureText(normalized).width) + 16;
    ctx.fillStyle = '#17263e';
    ctx.beginPath();
    ctx.roundRect(point.x - textWidth / 2, point.y + 14, textWidth, 21, 5);
    ctx.fill();
    ctx.fillStyle = '#dbe9ff';
    ctx.fillText(normalized, point.x, point.y + 25, 245);
  }
  ctx.restore();
}

function drawNode(ctx: CanvasRenderingContext2D, node: CanvasNode, name: NodeName | undefined, options: {
  lift: number; alpha: number; picked: boolean; focused: boolean; glow: boolean; draft: boolean; portal: boolean; ghost: boolean; motion: boolean; now: number;
}) {
  const { x, y, radius: r, model } = node;
  const type = model.node_type;
  const detailScale = Math.min(1, r / radii[type]);
  const domain = type === 'Domain';
  const color = nodeColors[type];
  ctx.save();
  ctx.globalAlpha = options.alpha;
  ctx.translate(x, y);
  ctx.lineWidth = 1;
  if (domain) {
    ctx.strokeStyle = '#51578d';
    ctx.beginPath();
    ctx.arc(0, 0, r + 5 * detailScale, 0, Math.PI * 2);
    ctx.stroke();
    ctx.save();
    if (options.glow) ctx.globalAlpha = 1;
    ctx.strokeStyle = options.glow ? '#a5b4fc' : '#373e6e';
    ctx.lineWidth = options.glow ? 1.9 : 1;
    ctx.beginPath();
    ctx.arc(0, 0, r + 10 * detailScale, 0, Math.PI * 2);
    ctx.stroke();
    ctx.restore();
  } else if (options.portal) {
    ctx.save();
    if (options.glow) ctx.globalAlpha = 1;
    ctx.strokeStyle = options.glow ? color : `${color}8c`;
    ctx.lineWidth = options.glow ? 1.9 : 1;
    ctx.beginPath();
    ctx.arc(0, 0, r + 5 * detailScale, 0, Math.PI * 2);
    ctx.stroke();
    ctx.restore();
  }
  if (options.draft) {
    const pulse = options.motion ? 0.5 + 0.5 * Math.sin(options.now / 245) : 0.5;
    ctx.save();
    ctx.strokeStyle = '#d2d2ff';
    ctx.lineWidth = 1.4;
    ctx.setLineDash([4, 4]);
    ctx.globalAlpha *= 0.65 + 0.35 * pulse;
    ctx.beginPath();
    ctx.arc(0, 0, r + ((domain ? 12 : 7) + pulse * 3) * detailScale, 0, Math.PI * 2);
    ctx.stroke();
    ctx.restore();
  }
  if (options.lift > .04) {
    const light = ctx.createRadialGradient(-r*.38,-r*.45,r*.03,0,0,r*1.07);
    light.addColorStop(0, domain ? '#817dd3' : color);
    light.addColorStop(.56,color);
    light.addColorStop(1,domain ? '#252345' : type==='Metric' ? '#965218' : type==='Concept' ? '#116751' : '#4d518c');
    ctx.fillStyle=light;
  } else ctx.fillStyle=color;
  ctx.shadowColor = '#070c18';
  ctx.shadowBlur = 14;
  ctx.shadowOffsetY = 4;
  ctx.beginPath();
  ctx.arc(0, 0, r, 0, Math.PI * 2);
  ctx.fill();
  ctx.shadowBlur = 0;
  ctx.shadowOffsetY = 0;
  ctx.strokeStyle = domain ? '#615ba6' : '#ffffff35';
  ctx.lineWidth = 1;
  ctx.stroke();
  if (options.picked || options.focused) {
    ctx.strokeStyle = options.focused ? '#e8f2ff' : '#a3b4ef';
    ctx.lineWidth = 1.5;
    ctx.beginPath();
    ctx.arc(0, 0, Math.max(0, r - 1.75), 0, Math.PI * 2);
    ctx.stroke();
  }
  drawGlyph(ctx, type, r, domain ? '#e3e4ff' : '#102334');

  if (!name) { ctx.restore(); return; }
  ctx.font = name.font;
  ctx.fillStyle = options.ghost ? '#d0d9ee' : ink;
  ctx.textAlign = 'center';
  ctx.textBaseline = 'alphabetic';
  ctx.shadowColor = '#0f172a';
  ctx.shadowBlur = 5;
  ctx.fillText(name.text, 0, r + name.offset + name.ascent);
  ctx.restore();
}

// Render the current full name last so nearer nodes cannot obscure it.
function drawFocusedName(ctx: CanvasRenderingContext2D, node: CanvasNode,
  area: { x:number; y:number; width:number; height:number }) {
  ctx.save();
  ctx.font = nodeFont(false, 12);
  ctx.textBaseline = 'middle';
  const padding = 7, lineHeight = 17, available = Math.max(24, Math.min(285, area.width-2*padding));
  const lines: string[] = [];
  let last = '';
  for (const c of node.model.label) {
    if (last && ctx.measureText(last+c).width > available) { lines.push(last); last=''; }
    last += c;
  }
  lines.push(last);
  const w = Math.min(available,Math.max(...lines.map(line => ctx.measureText(line).width)))+2*padding;
  const h = lines.length*lineHeight+6;
  const x = Math.max(area.x,Math.min(area.x+area.width-w,node.x-w/2));
  const below = node.y+node.radius+6;
  const y = below+h <= area.y+area.height ? below : Math.max(area.y,node.y-node.radius-h-6);
  ctx.fillStyle = '#142137';
  ctx.strokeStyle = '#617a9b';
  ctx.lineWidth = 1;
  ctx.beginPath(); ctx.roundRect(x,y,w,h,5); ctx.fill(); ctx.stroke();
  ctx.fillStyle = '#edf2fa';
  ctx.textAlign = 'center';
  lines.forEach((line,i) => ctx.fillText(line,x+w/2,y+3+(i+.5)*lineHeight));
  ctx.restore();
}

function drawGlyph(ctx: CanvasRenderingContext2D, type: NodeType, radius: number, ink: string) {
  const size = radius / (type === 'Domain' ? 2.45 : 2.1);
  ctx.save();
  ctx.scale(size, size);
  ctx.strokeStyle = ink;
  ctx.lineCap = 'round';
  ctx.lineJoin = 'round';
  ctx.lineWidth = type === 'Domain' ? 0.105 : 0.15;
  ctx.beginPath();
  switch (type) {
    case 'Domain':
      for (const x of [-0.72, 0.12]) for (const y of [-0.72, 0.12]) ctx.roundRect(x, y, 0.60, 0.60, 0.08);
      break;
    case 'Metric':
      ctx.moveTo(-0.75, 0.28); ctx.lineTo(-0.32, -0.13); ctx.lineTo(-0.04, 0.04); ctx.lineTo(0.63, -0.59);
      ctx.moveTo(0.24, -0.59); ctx.lineTo(0.63, -0.59); ctx.lineTo(0.63, -0.20);
      break;
    case 'Concept':
      ctx.moveTo(0, -0.77); ctx.lineTo(0.72, -0.29); ctx.lineTo(0.72, 0.31); ctx.lineTo(0, 0.77);
      ctx.lineTo(-0.72, 0.31); ctx.lineTo(-0.72, -0.29); ctx.closePath();
      ctx.moveTo(-0.72, -0.29); ctx.lineTo(0, 0.16); ctx.lineTo(0.72, -0.29); ctx.moveTo(0, 0.16); ctx.lineTo(0, 0.77);
      break;
    case 'Artifact':
      ctx.moveTo(-0.56, -0.73); ctx.lineTo(0.20, -0.73); ctx.lineTo(0.59, -0.34); ctx.lineTo(0.59, 0.72);
      ctx.lineTo(-0.56, 0.72); ctx.closePath();
      ctx.moveTo(0.2, -0.73); ctx.lineTo(0.2, -0.31); ctx.lineTo(0.59, -0.31);
      ctx.moveTo(-0.30, 0.10); ctx.lineTo(0.34, 0.10); ctx.moveTo(-0.30, 0.37); ctx.lineTo(0.19, 0.37);
      break;
  }
  ctx.stroke();
  ctx.restore();
}

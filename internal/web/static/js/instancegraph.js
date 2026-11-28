// @ts-check
// The Explore view's instance graph: built up from the instances you open
// (each panel carries its edges as an <fga-graph-patch>), edges added and
// removed from edge lists, expanding in place (+1 hop), removing, undo, and
// the node menu.
import { $, $$, article, base, baseOpts, labelWidth, pal, reducedMotion, typeColor } from './core.js';
import { fitView, graphs, plural, relayout, showCanvas, showNote } from './exploregraphs.js';
import { onEscape } from './shortcuts.js';

/** @typedef {import('./edgelist.js').FgaEdgeList} FgaEdgeList */

/**
 * What the instance graph asks of the page.
 * @typedef {object} InstanceHooks
 * @property {() => void} changed the graph changed (to keep it)
 * @property {(obj: string) => void} open open an instance in the panel
 * @property {(d: any) => void} chooser open the edge chooser behind a count node
 * @property {(edge: any) => void} edge an edge was tapped
 */
/** @type {InstanceHooks} */
let hooks = { changed() {}, open() {}, chooser() {}, edge() {} };

/** @param {ReturnType<typeof pal>} p */
function instStyle(p) {
  return [
    { selector: 'node', style: {
      shape: 'round-rectangle', label: 'data(label)', 'font-size': 11, color: p.fg, 'text-valign': 'center',
      'background-color': p.panel, 'border-width': 2, 'border-color': (/** @type {any} */ e) => typeColor(e.data('type')),
      width: (/** @type {any} */ e) => labelWidth(e.data('label')), height: 28, 'text-wrap': 'ellipsis', 'text-max-width': 260,
      'border-style': 'dashed', // not expanded yet: its own edges are not on the graph
    } },
    // Expanded: opened in the panel (focus) or expanded in place.
    { selector: 'node[?focus], node.expanded', style: { 'border-width': 3, 'border-style': 'solid' } },
    { selector: 'node.fresh', style: { 'border-color': p.accent } },
    { selector: 'node.current', style: { 'background-color': (/** @type {any} */ e) => typeColor(e.data('type')), color: '#fff' } },
    { selector: 'node[kind = "count"]', style: {
      shape: 'round-tag', 'border-style': 'dashed', 'border-color': p.muted, color: p.muted, 'font-style': 'italic',
    } },
    { selector: 'edge', style: {
      'curve-style': 'bezier', 'target-arrow-shape': 'triangle', width: 1.4, 'line-color': p.line, 'target-arrow-color': p.line,
      label: 'data(label)', 'font-size': 9, color: p.muted, 'text-background-color': p.bg, 'text-background-opacity': 0.9, 'text-background-padding': 2,
    } },
    { selector: 'edge.count', style: { 'line-style': 'dashed' } },
  ];
}

/**
 * <fga-graph-patch>: server-computed additions to the instance graph (an
 * instance's edges, as JSON in a <script type="application/json"> child),
 * sent with each instance panel and Incoming tab. It adds them to the graph
 * when it arrives on the page, once.
 */
class FgaGraphPatch extends HTMLElement {
  connectedCallback() {
    const json = $('script', this)?.textContent;
    if (this.applied || !json) return;
    this.applied = true;
    applyPatch(JSON.parse(json));
  }
}

/**
 * Puts the instance open in the panel (if any) back on the instance graph,
 * with the edges its panel and Incoming tab carry: no request.
 */
function showOpenInstance() {
  for (const s of $$('#instance-panel fga-graph-patch script')) applyPatch(JSON.parse(s.textContent || '{}'));
}

/** Adds server-computed elements (an instance's edges) to the instance graph. @param {any} patch */
function applyPatch(patch) {
  const instCy = graphs.inst;
  if (!instCy) return;
  const first = instCy.nodes().length === 0;
  /** @type {string[]} */
  const ids = [];
  instCy.batch(() => {
    for (const el of patch.elements || []) {
      if (instCy.getElementById(el.data.id).length) continue;
      if (el.group === 'edges' && (!instCy.getElementById(el.data.source).length || !instCy.getElementById(el.data.target).length)) continue;
      instCy.add(el);
      ids.push(el.data.id);
    }
    instCy.nodes().removeClass('current');
    if (patch.focus) instCy.getElementById(patch.focus).addClass('current');
    if (patch.focus) markExpanded(patch.focus);
  });
  updateCount();
  markEdgeLists();
  showCanvas('instances');
  // The first instance is laid out and fitted. After that, the nodes already
  // placed stay put, new ones go beside their neighbors, and the view keeps
  // its zoom, moving to the instance opened.
  if (first) { relayout(instCy); return; }
  placeNear(instCy.collection(ids.map((id) => instCy.getElementById(id))).nodes());
  const f = instCy.getElementById(patch.focus);
  if (f.length) {
    instCy.stop(true);
    instCy.animate({ center: { eles: f }, duration: reducedMotion() ? 0 : 250 });
  }
}

/**
 * An instance-graph edge: user (source) → object (target), and the object's
 * relation it stands for. id is its element ID, set by the server
 * (web.edgeData), which edge lists and graph elements share.
 * @typedef {{ id: string, source: string, target: string, label: string, relation?: string, sourceLabel?: string, targetLabel?: string }} EdgeData
 */

/** @param {EdgeData} d */
const edgeId = (d) => d.id;
/** Whether an edge is on the graph. @param {EdgeData} d */
export const onGraph = (d) => !!graphs.inst && graphs.inst.getElementById(edgeId(d)).length > 0;

/** @param {EdgeData} d */
export function addEdge(d) {
  const instCy = graphs.inst;
  if (!instCy || !d) return;
  /** @type {any[]} */
  const fresh = [];
  instCy.batch(() => {
    for (const [nid, lbl] of [[d.source, d.sourceLabel], [d.target, d.targetLabel]]) {
      if (!instCy.getElementById(nid).length) fresh.push(instCy.add({ group: 'nodes', data: { id: nid, label: lbl || nid, type: String(nid).split(':')[0] } }));
    }
    if (!instCy.getElementById(edgeId(d)).length) instCy.add({ group: 'edges', data: { id: edgeId(d), source: d.source, target: d.target, label: d.label, relation: d.relation } });
    const anchor = $('#instance-panel .instance')?.dataset.object;
    if (anchor) instCy.getElementById(anchor).addClass('current');
  });
  // A new peer goes beside its neighbor; the rest of the graph stays put.
  if (fresh.length && instCy.nodes().length > fresh.length) placeNear(instCy.collection(fresh));
  else if (fresh.length) relayout(instCy);
}

/** @param {EdgeData} d */
export function removeEdge(d) {
  const e = graphs.inst?.getElementById(edgeId(d));
  if (!e?.length) return;
  const ends = e.connectedNodes();
  e.remove();
  // Drop peers left with no edges, but keep the instance being explored.
  const anchor = $('#instance-panel .instance')?.dataset.object;
  ends.forEach((/** @type {any} */ n) => { if (n.id() !== anchor && !n.hasClass('current') && n.connectedEdges().length === 0) n.remove(); });
}

/** Shows how many instances the graph holds. */
export function updateCount() {
  const c = $('#inst-count');
  if (c && graphs.inst) c.textContent = graphs.inst.nodes().filter((/** @type {any} */ n) => n.data('kind') !== 'count').length;
  updateEmpty();
}

/** The instance graph fills in from instances you open; an empty canvas says how. */
export function updateEmpty() {
  const box = $('#instance-empty');
  if (!box || !graphs.inst) return;
  const showing = !(/** @type {HTMLElement} */ ($('#instance-canvas')).hidden);
  box.classList.toggle('hidden', !(showing && graphs.inst.nodes().length === 0));
  const type = $('#type-panel [data-selected-type]')?.dataset.selectedType;
  const btn = $('[data-act="start-picking"]', box);
  const msg = $('.ce-msg', box);
  if (!btn || !msg) return;
  if (type) {
    msg.textContent = `Pick ${article(type)} ${type} instance to start. Its edges appear here, and clicking a node walks to it.`;
    btn.textContent = `Pick ${article(type)} ${type} instance`;
  } else {
    msg.textContent = 'It fills in as you open instances. Choose a type on the right, then pick one of its instances.';
    btn.textContent = 'Choose a type';
  }
}

/** After edges were added or removed. @param {{ show?: boolean }} [opts] show: switch to the instance graph */
export function graphChanged({ show = false } = {}) {
  updateCount();
  markEdgeLists();
  hooks.changed();
  if (show) showCanvas('instances');
}

/**
 * The edge lists (edgelist.js) mirror the graph: checked (or "−") means the
 * edge is on it. Lists not set up yet (:defined) mark themselves on arrival.
 */
function markEdgeLists() {
  for (const list of /** @type {FgaEdgeList[]} */ ($$('fga-edge-list:defined'))) list.mark(onGraph);
}

// ---- expanding the instance graph in place (api/instance-graph) ----
// Expanding a node adds its edges (both directions, as its panel shows them)
// without opening it: the panel and the nodes already placed stay put.

const MAX_GRAPH_NODES = 500; // no expanding past this
/** Instances one expansion may read for this person (graph_expansion); 0: off. */
export const expandMax = () => Number($('.explore-layout')?.dataset.expandMax || 0);
/** One expansion at a time (the server allows one per person). */
let expanding = false;
/** @param {boolean} on */
function setExpanding(on) {
  expanding = on;
  for (const b of /** @type {HTMLButtonElement[]} */ ($$('[data-act="expand-hop"]'))) b.disabled = on;
}
/** Reverses the last expansion or removal (the status line's Undo). @type {(() => void) | null} */
let undoLast = null;

/** The instance a node stands for ("group:eng" for "group:eng#member"). @param {string} id */
export const objectOf = (id) => {
  const colon = id.indexOf(':');
  const hash = id.lastIndexOf('#');
  return hash > colon ? id.slice(0, hash) : id;
};
/** @param {any} n */
const isExpanded = (n) => n.hasClass('expanded') || !!n.data('focus');
/** @param {any} n */
const isInstance = (n) => n.data('kind') !== 'count';

/** Marks an instance (and its usersets' nodes) as expanded. @param {string} obj */
function markExpanded(obj) {
  graphs.inst.nodes().filter((/** @type {any} */ n) => isInstance(n) && objectOf(n.id()) === obj).addClass('expanded');
}

/**
 * Says what the graph did, in a line over the instance graph.
 * @param {string} text
 * @param {{ undo?: boolean }} [opts] undo: offer to undo the last expansion
 */
function graphNote(text, { undo = false } = {}) {
  const note = /** @type {HTMLElement | null} */ ($('#instance-note'));
  if (!note) return;
  const t = $('.note-text', note);
  if (t) t.textContent = text;
  const u = /** @type {HTMLElement | null} */ ($('[data-act="undo"]', note));
  if (u) u.hidden = !undo;
  showNote(note, !!text, graphs.inst);
}

/**
 * Places new nodes in a ring around a node already on the graph, so the
 * existing layout stays as it is (Re-layout tidies everything).
 * @param {any} added new nodes
 */
function placeNear(added) {
  const instCy = graphs.inst;
  /** @type {Map<string, any[]>} anchor id → the new nodes placed around it */
  const around = new Map();
  const before = instCy.nodes().not(added);
  const placed = new Set(before.map((/** @type {any} */ n) => n.id()));
  /** @type {any[]} */
  let todo = added.toArray();
  /** @type {any[]} not connected to anything placed */
  let loose = [];
  while (todo.length) {
    /** @type {any[]} */
    const later = [];
    for (const n of todo) {
      const anchor = n.neighborhood().nodes().filter((/** @type {any} */ m) => placed.has(m.id()))[0];
      if (!anchor) { later.push(n); continue; }
      if (!around.has(anchor.id())) around.set(anchor.id(), []);
      around.get(anchor.id())?.push(n);
      placed.add(n.id());
    }
    if (later.length === todo.length) { loose = later; break; }
    todo = later;
  }
  // Boxes already taken; each new node moves out until it is clear of them.
  /** @type {{ x: number, y: number, w: number, h: number }[]} */
  const taken = before.map((/** @type {any} */ n) => ({ ...n.position(), w: n.outerWidth(), h: n.outerHeight() }));
  const clear = (/** @type {{ x: number, y: number, w: number, h: number }} */ b) =>
    taken.every((t) => Math.abs(t.x - b.x) > (t.w + b.w) / 2 + 12 || Math.abs(t.y - b.y) > (t.h + b.h) / 2 + 12);
  // The graph's middle: new nodes fan out on the side of their anchor facing away from it.
  const mid = before.length ? before.boundingBox() : { x1: 0, x2: 0, y1: 0, y2: 0 };
  const cx = (mid.x1 + mid.x2) / 2;
  const cy = (mid.y1 + mid.y2) / 2;
  for (const [id, nodes] of around) {
    const a = instCy.getElementById(id).position();
    const away = Math.hypot(a.x - cx, a.y - cy) < 1 ? -Math.PI / 2 : Math.atan2(a.y - cy, a.x - cx);
    const spread = Math.min(2 * Math.PI, 0.45 * nodes.length);
    nodes.forEach((n, i) => {
      const angle = away - spread / 2 + (spread * (i + 0.5)) / nodes.length;
      const box = { x: 0, y: 0, w: n.outerWidth(), h: n.outerHeight() };
      for (let r = 130, tries = 0; tries < 16; r += 45, tries++) {
        box.x = a.x + r * Math.cos(angle);
        box.y = a.y + r * Math.sin(angle);
        if (clear(box)) break;
      }
      n.position({ x: box.x, y: box.y });
      taken.push(box);
    });
  }
  // Anything not connected to the graph goes in a column to its right.
  loose.forEach((n, i) => n.position({ x: mid.x2 + 160, y: mid.y1 + 44 * i }));
}

/**
 * Expands instances in place: adds their edges and peers to the graph.
 * @param {string[]} objects instances (type:id), nearest first (only the first expandMax() are read)
 * @param {string} what for the status line ("from document:doc-0007")
 * @param {string} [more] said after the result (e.g. that more remain)
 */
async function expand(objects, what, more = '') {
  const instCy = graphs.inst;
  if (!instCy || !expandMax()) return;
  showCanvas('instances');
  if (expanding) { graphNote('Still expanding; one expansion at a time.'); return; }
  const n = instCy.nodes().length;
  if (n >= MAX_GRAPH_NODES) {
    graphNote(`The graph has ${n} nodes, the most it expands to. Remove some (Remove leaves, or right-click a node), or Clear.`);
    return;
  }
  graphNote(`Expanding ${what}…`);
  const q = new URLSearchParams();
  for (const o of objects.slice(0, expandMax())) q.append('object', o);
  /** @type {{ objects: string[], incomplete?: string[], limit?: string, elements: any[], errors?: string[] }} */
  let g;
  setExpanding(true);
  try {
    const r = await fetch(`${base()}/api/instance-graph?${q}`);
    const body = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(body.error || r.statusText);
    g = body;
  } catch (err) {
    graphNote(`Could not expand ${what}: ${err instanceof Error ? err.message : err}`);
    return;
  } finally {
    setExpanding(false);
  }
  const newly = g.objects.filter((o) => !instCy.getElementById(o).hasClass('expanded'));
  /** @type {string[]} */
  const ids = [];
  instCy.batch(() => {
    for (const el of g.elements) {
      if (instCy.getElementById(el.data.id).length) continue;
      if (el.group === 'edges' && (!instCy.getElementById(el.data.source).length || !instCy.getElementById(el.data.target).length)) continue;
      instCy.add(el);
      ids.push(el.data.id);
    }
    g.objects.forEach(markExpanded);
  });
  undoLast = () => {
    instCy.collection(ids.map((id) => instCy.getElementById(id))).remove();
    for (const o of newly) {
      instCy.nodes().filter((/** @type {any} */ x) => objectOf(x.id()) === o && !x.data('focus')).removeClass('expanded');
    }
  };
  const added = instCy.collection(ids.map((id) => instCy.getElementById(id)));
  placeNear(added.nodes());
  const nodes = added.nodes().filter(isInstance).length;
  const counts = added.nodes().length - nodes;
  let text = ids.length ? `Added ${plural(nodes, 'node')} and ${plural(added.edges().length, 'edge')} ${what}.` : `Nothing new ${what}: its edges are already on the graph.`;
  if (counts) text += ` ${plural(counts, 'relation')} with many edges ${counts === 1 ? 'is' : 'are'} shown as a count (click it to choose edges).`;
  if (g.incomplete?.length) {
    // Not marked expanded, so +1 hop picks them up again.
    text += ` ${plural(g.incomplete.length, 'instance')} ${g.incomplete.length === 1 ? 'was' : 'were'} only partly read: ${g.limit} ran out (${g.incomplete.join(', ')}); +1 hop again to continue.`;
  }
  if (g.errors?.length) text += ` Could not read: ${g.errors.join('; ')}.`;
  graphNote(text + (more ? ` ${more}` : ''), { undo: ids.length > 0 || newly.length > 0 });
  // New nodes stand out for a moment; bring them into view if they are not.
  added.nodes().addClass('fresh');
  setTimeout(() => added.nodes().removeClass('fresh'), 2000);
  const view = instCy.extent();
  const box = added.nodes().boundingBox();
  if (added.nodes().length && (box.x1 < view.x1 || box.x2 > view.x2 || box.y1 < view.y1 || box.y2 > view.y2)) {
    instCy.stop(true); // a centering still running would undo the fit
    fitView(instCy, added.union(added.neighborhood()).union(instCy.nodes('.current')));
  }
  updateCount();
  markEdgeLists();
  hooks.changed();
}

/** Expands one node in place. @param {any} n */
function expandNode(n) {
  if (!isInstance(n)) return;
  expand([objectOf(n.id())], `from ${n.data('label')}`);
}

/**
 * One more hop around a node: expands the nearest instances not expanded
 * yet. From an expanded node, those are its neighbors; when they are all
 * expanded, the ring beyond them; and so on. A node not expanded yet is
 * expanded first.
 * @param {any} start
 */
function expandHop(start) {
  if (!start?.length || !isInstance(start)) return;
  const label = start.data('label');
  if (!isExpanded(start)) { expandNode(start); return; }
  /** @type {string[]} */
  const frontier = [];
  const seen = new Set([start.id()]);
  let ring = [start];
  while (ring.length) {
    /** @type {any[]} */
    const next = [];
    for (const x of ring) {
      for (const y of x.neighborhood().nodes().filter(isInstance)) {
        if (seen.has(y.id())) continue;
        seen.add(y.id());
        if (isExpanded(y)) next.push(y);
        else if (!frontier.includes(objectOf(y.id()))) frontier.push(objectOf(y.id()));
      }
    }
    if (frontier.length) break; // the nearest ring that has unexpanded instances
    ring = next;
  }
  if (!frontier.length) { graphNote(`Everything connected to ${label} on the graph is expanded.`); return; }
  const take = frontier.slice(0, expandMax());
  const more = frontier.length > take.length ? `Expanded ${take.length} of ${frontier.length} neighbors; +1 hop again for more.` : '';
  expand(take, `around ${label}`, more);
}

/** +1 hop around the open instance (the canvas bar and the instance panel). */
export function hopFromOpen() {
  const instCy = graphs.inst;
  const obj = $('#instance-panel .instance')?.dataset.object;
  // Taken off the graph (cleared, or removed): put it back first.
  if (obj && !instCy?.getElementById(obj).length) showOpenInstance();
  const n = obj && instCy?.getElementById(obj);
  if (n?.length) { expandHop(n); return; }
  showCanvas('instances');
  graphNote('Open an instance first (click a node, or pick one on the right); +1 hop then expands around it. Shift+click a node to expand just that node.');
}

/** Undoes the last expansion or removal. */
export function undo() {
  if (!undoLast || !graphs.inst) return;
  const f = undoLast;
  undoLast = null;
  graphs.inst.batch(f);
  graphNote('');
  updateCount();
  markEdgeLists();
  hooks.changed();
}

/** Starts over from the instance open in the panel, if any. */
export function clearGraph() {
  graphs.inst?.elements().remove();
  undoLast = null;
  graphNote('');
  showOpenInstance();
  graphChanged();
}

/**
 * Removes elements from the instance graph, and says so with an Undo.
 * @param {any} gone nodes and edges
 * @param {string} text for the status line
 */
function removeFromGraph(gone, text) {
  // Nodes first, so Undo can add the edges back between them.
  const saved = [...gone.nodes().jsons(), ...gone.edges().jsons()];
  gone.remove();
  undoLast = () => { graphs.inst.add(saved); };
  graphNote(text, { undo: true });
  updateCount();
  markEdgeLists();
  hooks.changed();
}

/** Removes a node and its edges, and peers left with no other edges (not the open instance). @param {any} n */
function removeNode(n) {
  const peers = n.neighborhood().nodes().filter((/** @type {any} */ p) => !p.hasClass('current') && p.connectedEdges().not(n.connectedEdges()).length === 0);
  const gone = n.union(peers);
  removeFromGraph(gone.union(gone.connectedEdges()), `Removed ${n.data('label')}${peers.length ? ` and ${plural(peers.length, 'node')} left with no edges` : ''}.`);
}

/**
 * Removes leaves: instances with at most one edge that are neither expanded
 * nor open (the fringe an expansion leaves). Again, the next layer.
 * @param {any} [around] only the leaves next to this node
 */
export function removeLeaves(around) {
  const instCy = graphs.inst;
  if (!instCy) return;
  const pool = around ? around.neighborhood().nodes() : instCy.nodes();
  const leaves = pool.filter((/** @type {any} */ n) => isInstance(n) && !isExpanded(n) && !n.hasClass('current') && n.degree(false) <= 1);
  const where = around ? ` around ${around.data('label')}` : '';
  if (!leaves.length) {
    graphNote(`No leaves${where}: every instance there is expanded, open, or has more than one edge.`);
    return;
  }
  removeFromGraph(leaves.union(leaves.connectedEdges()), `Removed ${plural(leaves.length, 'leaf', 'leaves')}${where} (instances with one edge, not expanded).`);
}

// ---- the node menu (right-click or long-press a node) ----

/** @type {any} the node the menu is for */
let menuNode = null;

/** @param {any} n @param {{ x: number, y: number }} at page (client) coordinates */
function openNodeMenu(n, at) {
  const menu = /** @type {HTMLElement | null} */ ($('#node-menu'));
  if (!menu || !isInstance(n)) return;
  menuNode = n;
  const title = $('.nm-title', menu);
  if (title) title.textContent = n.data('label');
  menu.hidden = false;
  const r = menu.getBoundingClientRect();
  menu.style.left = `${Math.min(at.x, window.innerWidth - r.width - 8)}px`;
  menu.style.top = `${Math.min(at.y, window.innerHeight - r.height - 8)}px`;
  /** @type {HTMLElement | null} */ ($('[role="menuitem"]', menu))?.focus();
}

function closeNodeMenu() {
  const menu = /** @type {HTMLElement | null} */ ($('#node-menu'));
  if (!menu || menu.hidden) return;
  menu.hidden = true;
  menuNode = null;
}

/** @param {string} act */
function nodeAction(act) {
  const n = menuNode;
  closeNodeMenu();
  if (!n || n.removed()) return;
  if (act === 'open') hooks.open(objectOf(n.id()));
  else if (act === 'expand') expandNode(n);
  else if (act === 'hop') expandHop(n);
  else if (act === 'remove') removeNode(n);
  else if (act === 'leaves') removeLeaves(n);
}

function initNodeMenu() {
  const menu = $('#node-menu');
  if (!menu) return;
  menu.addEventListener('click', (e) => {
    const b = e.target instanceof Element ? e.target.closest('[data-node-act]') : null;
    if (b instanceof HTMLElement) nodeAction(b.dataset.nodeAct || '');
  });
  // ↑/↓ move between items, Escape or Tab closes (WAI-ARIA menu).
  menu.addEventListener('keydown', (e) => {
    const items = /** @type {HTMLElement[]} */ ($$('[role="menuitem"]', menu));
    const i = items.indexOf(/** @type {HTMLElement} */ (document.activeElement));
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault();
      items[(i + (e.key === 'ArrowDown' ? 1 : items.length - 1)) % items.length].focus();
    } else if (e.key === 'Escape' || e.key === 'Tab') {
      e.preventDefault();
      e.stopPropagation();
      closeNodeMenu();
    }
  });
  document.addEventListener('pointerdown', (e) => { if (!(e.target instanceof Node && menu.contains(e.target))) closeNodeMenu(); }, true);
  onEscape(closeNodeMenu);
  // The browser's own menu would cover ours.
  $('#instance-canvas')?.addEventListener('contextmenu', (e) => e.preventDefault());
}

/**
 * Draws the (empty) instance graph and defines <fga-graph-patch>, which
 * fills it.
 * @param {HTMLElement} container
 * @param {InstanceHooks} h
 */
export function initInstanceGraph(container, h) {
  hooks = h;
  const instCy = cytoscape({ container, elements: [], style: instStyle(pal()), ...baseOpts });
  graphs.inst = instCy;
  if (!customElements.get('fga-graph-patch')) customElements.define('fga-graph-patch', FgaGraphPatch);
  // Click opens a node; Shift+click expands it in place; right-click or a
  // long press opens its menu (and the tap that ends a long press does nothing).
  let heldAt = 0;
  /** @param {any} e @returns {{ x: number, y: number }} where the menu opens */
  const menuAt = (e) => {
    const r = instCy.container().getBoundingClientRect();
    return { x: r.left + e.renderedPosition.x, y: r.top + e.renderedPosition.y };
  };
  instCy.on('cxttap', 'node', (/** @type {any} */ e) => openNodeMenu(e.target, menuAt(e)));
  instCy.on('taphold', 'node', (/** @type {any} */ e) => { heldAt = Date.now(); openNodeMenu(e.target, menuAt(e)); });
  instCy.on('tap', 'node', (/** @type {any} */ e) => {
    if (Date.now() - heldAt < 800) return;
    const d = e.target.data();
    if (e.originalEvent?.shiftKey && d.kind !== 'count' && expandMax()) { expandNode(e.target); return; }
    if (d.kind === 'count') hooks.chooser(d); else hooks.open(d.id);
  });
  instCy.on('tap', 'edge', (/** @type {any} */ e) => hooks.edge(e.target));
  initNodeMenu();
}

// The Explore view: the type graph, the instance graph built up as you open
// instances, the side panel (type and instance panels, rendered by the
// server), and the per-tab persistence of all of it.
import { $, $$, article, base, baseOpts, categoricalColor, dagre, fitCapped, labelWidth, load, pal, reducedMotion, rememberObject, scrollBehavior, session, stateKey, store, typeColor } from './core.js';
import { initEdgeLists } from './edgelist.js';
import { picker } from './pickers.js';
import { onEscape, onKey } from './shortcuts.js';
import { initTypeFinder } from './typefinder.js';
import { initResizer, tabsOf } from './ui.js';

/** @typedef {{ open(): void, remember(t: string): void }} TypeFinder <fga-type-finder> (typefinder.js) */
/** @returns {TypeFinder | null} */
const typeFinder = () => /** @type {any} */ ($('fga-type-finder:defined'));

/** @typedef {import('./edgelist.js').FgaEdgeList} FgaEdgeList */

/** @type {any} */ let schemaCy = null;
/** @type {any} */ let instCy = null;
// Whether the type graph shows a neighborhood laid out on its own.
let subLayout = false;
// Graphs fitted to a view at least once. After that, choosing or opening
// something keeps the person's zoom; only Fit, Re-layout, and Show all types
// fit the graph again.
const fitted = new WeakSet();
/** @param {any} cy @param {any} [eles] */
const fitView = (cy, eles) => { fitCapped(cy, eles); fitted.add(cy); };
// Large models start with an empty type graph (see exploreData.Large).
const isLarge = () => $('.explore-layout')?.dataset.large === 'true';
// The "too many types to draw" prompt shows (on the type graph tab) until a
// type is chosen or every type is drawn.
let promptWanted = false;
let noteText = '';
// The prompt and the note belong to the type graph tab.
const updatePrompt = () => {
  const onSchema = !$('#schema-canvas')?.hidden;
  const el = $('#schema-empty');
  if (el) el.hidden = !(promptWanted && onSchema);
  const note = $('#schema-note');
  if (note) showNote(note, !!noteText && onSchema, schemaCy);
};
/**
 * Shows or hides a note over a graph. The note moves the graph's canvas, so
 * the graph re-measures it: clicks would otherwise land a line off.
 * @param {HTMLElement} note @param {boolean} show @param {any} cy
 */
function showNote(note, show, cy) {
  if (note.hidden === !show) return;
  note.hidden = !show;
  cy?.resize();
}
/** @param {boolean} on */
const schemaPrompt = (on) => { promptWanted = on; updatePrompt(); };
// Types shown around a hub at most (see focusType).
const MAX_NEIGHBORS = 40;
/** A one-line note over the type graph (empty hides it). @param {string} text */
const schemaNote = (text) => {
  noteText = text;
  const el = $('#schema-note');
  if (el) el.textContent = text;
  updatePrompt();
};

// Optionally, types are colored by the module that defines them (modular models).
const modules = () => ($('.explore-layout')?.dataset.modules || '').split(',').filter(Boolean);
let byModule = false;
/** @param {any} e a type node */
const nodeColor = (e) => (byModule && e.data('module') ? moduleColor(e.data('module')) : typeColor(e.id()));
/** Modules get the categorical colors in module order. @param {string} m */
const moduleColor = (m) => categoricalColor(Math.max(0, modules().indexOf(m)));

/** Switches type colors between per-type and per-module. @param {boolean} on */
function colorByModule(on) {
  byModule = on;
  store.set('colorByModule', on);
  const legend = $('.mod-legend');
  if (legend) {
    legend.hidden = !on;
    legend.replaceChildren();
    // Built with the DOM (not HTML with style attributes, which the CSP blocks).
    for (const m of on ? modules() : []) {
      const item = document.createElement('span');
      const swatch = document.createElement('i');
      swatch.className = 'nd';
      swatch.style.borderColor = moduleColor(m);
      item.append(swatch, document.createTextNode(m));
      legend.append(item);
    }
  }
  schemaCy?.style().update();
}

/** @param {ReturnType<typeof pal>} p */
function schemaStyle(p) {
  return [
    { selector: 'node', style: {
      shape: 'round-rectangle', label: 'data(label)', 'font-size': 12, 'font-weight': 600, color: p.fg,
      'text-valign': 'center', 'background-color': p.panel, 'border-width': 2, 'border-color': (/** @type {any} */ e) => nodeColor(e),
      width: (/** @type {any} */ e) => labelWidth(e.data('label'), 12), height: 30,
    } },
    { selector: 'edge', style: {
      'curve-style': 'bezier', 'target-arrow-shape': 'triangle', width: 1.4, 'line-color': p.line, 'target-arrow-color': p.line,
      label: 'data(label)', 'font-size': 9, color: p.muted, 'text-background-color': p.bg, 'text-background-opacity': 0.9,
      'text-background-padding': 2, 'control-point-step-size': 40, 'text-wrap': 'wrap',
    } },
    { selector: 'edge.userset', style: { 'line-style': 'dashed' } },
    { selector: 'edge.wildcard', style: { 'line-style': 'dotted' } },
    { selector: 'edge.ttu', style: { 'line-color': p.ttu, 'target-arrow-color': p.ttu, 'line-style': 'dashed', color: p.ttu } },
    // The second hop recedes without losing legibility (no opacity on labels).
    { selector: 'node.faded', style: { 'border-opacity': 0.4, color: p.muted, 'font-weight': 400 } },
    { selector: 'edge.faded', style: { label: '', 'line-color': p.skipped, 'target-arrow-color': p.skipped, width: 1, 'line-style': 'dotted' } },
    { selector: 'node.sel', style: { 'border-width': 4, 'background-color': (/** @type {any} */ e) => nodeColor(e), color: '#fff' } },
  ];
}

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
 * Collapses or opens the side panel (types, type and instance panels).
 * @param {boolean} [collapse] collapse (true) or open (false); toggles when omitted
 */
function toggleSide(collapse) {
  const layout = $('.explore-layout');
  if (!layout) return;
  const was = layout.classList.contains('side-collapsed');
  const now = collapse ?? !was;
  layout.classList.toggle('side-collapsed', now);
  for (const b of $$('[data-act="toggle-side"]')) b.setAttribute('aria-expanded', String(!now));
  store.set('sideCollapsed', now);
  if (now !== was) {
    // The canvas changes width: keep the zoom, and what was in the middle.
    const cy = activeCy();
    const e = cy?.extent();
    resizeAll();
    if (cy && e) cy.pan({ x: cy.width() / 2 - ((e.x1 + e.x2) / 2) * cy.zoom(), y: cy.height() / 2 - ((e.y1 + e.y2) / 2) * cy.zoom() });
  }
}

const activeCy = () => ($('#schema-canvas')?.hidden ? instCy : schemaCy);
const resizeAll = () => { instCy?.resize(); schemaCy?.resize(); };

/** Shows a graph (its tab is selected; canvasShown follows). @param {'schema' | 'instances'} which */
const showCanvas = (which) => tabsOf('#graph-tabs')?.select(which);

/**
 * Updates the rest of the view for the graph now showing.
 * @param {'schema' | 'instances'} which
 */
function canvasShown(which) {
  // The side panel follows the canvas: types on the type graph, the open
  // instance (kept, not discarded) on the instance graph.
  $('#explore-side')?.classList.toggle('on-schema', which === 'schema');
  setInstanceFocus(which === 'instances' && !!$('#instance-panel .instance'));
  $$('.canvas-wrap .legend').forEach((l) => l.classList.toggle('hidden', l.dataset.for !== which));
  $('#instance-note')?.classList.toggle('hidden', which !== 'instances');
  const cy = which === 'schema' ? schemaCy : instCy;
  if (cy) { cy.resize(); if (!fitted.has(cy) && cy.elements(':visible').length) fitView(cy); }
  updateEmpty();
  updatePrompt();
  saveExplore();
}

// ---- type graph ----

/**
 * Shows the selected type's neighborhood (two hops) in the type graph,
 * hiding the rest until "Show all types" is pressed.
 * @param {string} t
 */
function focusType(t) {
  const n = schemaCy.getElementById(t);
  let capped = 0;
  schemaCy.batch(() => {
    schemaCy.elements().removeClass('sel faded').style('display', 'element');
    if (!n.length) return;
    let hood1 = n.closedNeighborhood();
    // A hub (user, group, … linked to most types) has the whole model as its
    // neighborhood: show its best-connected neighbors only, and say so.
    const neighbors = hood1.nodes().not(n);
    if (neighbors.length > MAX_NEIGHBORS) {
      const degree = (/** @type {any} */ x) => x.neighborhood().nodes().length;
      const keep = neighbors.sort((a, b) => degree(b) - degree(a) || (a.id() < b.id() ? -1 : 1)).slice(0, MAX_NEIGHBORS);
      hood1 = n.union(keep).union(n.connectedEdges().filter((/** @type {any} */ e) => keep.has(e.source()) || keep.has(e.target())));
      capped = neighbors.length;
    }
    // The second hop skips hubs, which would otherwise pull in the whole
    // model; if it is still large, show only the direct neighborhood.
    const hub = Math.max(12, schemaCy.nodes().length / 10);
    const via = hood1.nodes().filter((/** @type {any} */ x) => x === n || x.neighborhood().nodes().length <= hub);
    let hood2 = capped ? hood1 : hood1.union(via.neighborhood());
    if (hood2.nodes().length > 60) hood2 = hood1;
    const far = schemaCy.elements().not(hood2);
    far.style('display', 'none');
    hood2.not(hood1).addClass('faded');
    n.addClass('sel');
    $('.canvas-bar [data-act="show-all-types"]')?.classList.toggle('hidden', far.nodes().length === 0);
  });
  schemaPrompt(false);
  schemaNote(capped ? `${t} is linked to ${capped} types; showing the ${MAX_NEIGHBORS} best connected. Choose one of them, or search on the right.` : '');
  // Positions must come from a layout of what is visible: a neighborhood is
  // laid out on its own; when everything is visible, the full layout is used
  // (run now if it never has been, e.g. in a large model).
  if (schemaCy.nodes(':hidden').length) {
    schemaCy.elements(':visible').layout(dagre('LR', { rankSep: 150, nodeSep: 40, fit: false })).run();
    subLayout = true;
  } else if (subLayout) {
    schemaCy.layout(dagre('LR', { rankSep: 150, nodeSep: 40, fit: false })).run();
    subLayout = false;
  }
  if ($('#schema-canvas').hidden) return; // fitted when shown (canvasShown)
  // Choosing a type keeps the zoom: the type comes to the middle, and the
  // rest of its neighborhood is a scroll away. Only a first view (nothing
  // drawn before, as in a large model) is fitted.
  if (!fitted.has(schemaCy)) {
    fitView(schemaCy, schemaCy.elements(':visible'));
  } else if (n.length) {
    schemaCy.stop(true);
    schemaCy.center(n);
  }
}

function showAllTypes() {
  schemaPrompt(false);
  schemaNote('');
  schemaCy.elements().style('display', 'element');
  if (subLayout) {
    subLayout = false;
    schemaCy.layout(dagre('LR', { rankSep: 150, nodeSep: 40, fit: false })).run();
  }
  $('.canvas-bar [data-act="show-all-types"]')?.classList.add('hidden');
  fitView(schemaCy);
}

/** Marks t as the selected type among the chips, and remembers it as recent. @param {string} t */
function markType(t) {
  $$('.type-chip').forEach((c) => c.classList.toggle('on', c.dataset.type === t));
  $(`.type-list .type-chip[data-type="${CSS.escape(t)}"]`)?.scrollIntoView({ block: 'nearest' });
  typeFinder()?.remember(t);
}

/**
 * @param {string} t
 * @param {{ loadPanel?: boolean, browse?: boolean }} [opts] browse: the person
 *   is choosing a type (not opening an instance), so the type section shows
 */
function selectType(t, { loadPanel = true, browse = true } = {}) {
  if (browse) setInstanceFocus(false);
  markType(t);
  if (schemaCy) focusType(t);
  const panel = $('#type-panel');
  if (loadPanel && panel && panel.querySelector('[data-selected-type]')?.dataset.selectedType !== t) {
    return load(`${base()}/explore/type?type=${encodeURIComponent(t)}`, panel).then(() => { updateEmpty(); saveExplore(); });
  }
  return Promise.resolve();
}

/**
 * With an instance open, the side panel shows just the instance: the type
 * section (type list, type panel and its picker) is hidden until asked for
 * ("Type …", "Pick another…") or a type is chosen.
 * @param {boolean} on
 */
function setInstanceFocus(on) {
  const side = $('#explore-side');
  if (!side) return;
  side.classList.toggle('focus-instance', on);
  for (const b of $$('[data-act="show-type"]', side)) b.setAttribute('aria-expanded', String(!on));
}

/** Opens an instance (or a userset's object) in the side panel. @param {string} obj */
function loadInstance(obj) {
  const i = obj.indexOf(':');
  if (i <= 0) return Promise.resolve();
  let id = obj.slice(i + 1);
  const hash = id.lastIndexOf('#');
  if (hash >= 0) { obj = obj.slice(0, i + 1 + hash); id = id.slice(0, hash); }
  const type = obj.slice(0, i);
  selectType(type, { browse: false });
  const url = new URL(location.href);
  url.searchParams.set('type', type); url.searchParams.set('id', id); url.searchParams.delete('relation');
  history.replaceState(null, '', url);
  const panel = $('#instance-panel');
  return panel ? load(`${base()}/explore/instance?object=${encodeURIComponent(obj)}`, panel).then(instanceShown) : Promise.resolve();
}

/** With an instance loaded, the side panel shows just it, scrolled into view. */
function instanceShown() {
  const panel = $('#instance-panel');
  const inst = /** @type {HTMLElement | null} */ ($('.instance', panel));
  if (inst) setInstanceFocus(true);
  // Only instances in tuples are remembered, so typos don't come back as suggestions.
  if (inst?.dataset.exists === 'true') rememberObject(inst.dataset.object || '');
  const det = $('#type-panel .type-details');
  if (det) det.open = false;
  panel.scrollIntoView({ block: 'start', behavior: scrollBehavior() });
  saveExplore();
}

// ---- instance graph ----

/** @param {any} cy */
function relayout(cy) {
  if (!cy || cy.elements().length === 0) return;
  cy.layout(dagre('LR', { fit: false, rankSep: 130, nodeSep: 18 })).run();
  fitView(cy);
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
 * relation it stands for.
 * @typedef {{ source: string, target: string, label: string, relation?: string, sourceLabel?: string, targetLabel?: string }} EdgeData
 */

/** @param {EdgeData} d */
const edgeId = (d) => `${d.source}|${d.label}|${d.target}`;
/** @param {EdgeData} d */
const onGraph = (d) => !!instCy && instCy.getElementById(edgeId(d)).length > 0;

/** @param {EdgeData} d */
function addEdge(d) {
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
function removeEdge(d) {
  const e = instCy.getElementById(edgeId(d));
  if (!e.length) return;
  const ends = e.connectedNodes();
  e.remove();
  // Drop peers left with no edges, but keep the instance being explored.
  const anchor = $('#instance-panel .instance')?.dataset.object;
  ends.forEach((/** @type {any} */ n) => { if (n.id() !== anchor && !n.hasClass('current') && n.connectedEdges().length === 0) n.remove(); });
}

function updateCount() {
  const c = $('#inst-count');
  if (c && instCy) c.textContent = instCy.nodes().filter((/** @type {any} */ n) => n.data('kind') !== 'count').length;
  updateEmpty();
}

/** The instance graph fills in from instances you open; an empty canvas says how. */
function updateEmpty() {
  const box = $('#instance-empty');
  if (!box || !instCy) return;
  const showing = !$('#instance-canvas').hidden;
  box.classList.toggle('hidden', !(showing && instCy.nodes().length === 0));
  const type = $('#type-panel [data-selected-type]')?.dataset.selectedType;
  const btn = $('[data-act="start-picking"]', box);
  const msg = $('.ce-msg', box);
  if (type) {
    msg.textContent = `Pick ${article(type)} ${type} instance to start. Its edges appear here, and clicking a node walks to it.`;
    btn.textContent = `Pick ${article(type)} ${type} instance`;
  } else {
    msg.textContent = 'It fills in as you open instances. Choose a type on the right, then pick one of its instances.';
    btn.textContent = 'Choose a type';
  }
}

function startPicking() {
  const p = picker('#type-panel');
  if (p) { p.open(); return; }
  const finder = typeFinder();
  if (finder) { finder.open(); return; }
  $$('.type-chip').forEach((c) => { c.classList.remove('nudge'); void c.offsetWidth; c.classList.add('nudge'); });
}

/** @param {{ show?: boolean }} [opts] */
function graphChanged({ show = false } = {}) {
  updateCount();
  markEdgeLists();
  saveExplore();
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
const expandMax = () => Number($('.explore-layout')?.dataset.expandMax || 0);
/** One expansion at a time (the server allows one per person). */
let expanding = false;
/** @param {boolean} on */
function setExpanding(on) {
  expanding = on;
  for (const b of /** @type {HTMLButtonElement[]} */ ($$('[data-act="expand-hop"]'))) b.disabled = on;
}
/** Reverses the last expansion or removal (the status line's Undo). @type {(() => void) | null} */
let undoLast = null;
/** @param {number} k @param {string} one @param {string} [many] */
const plural = (k, one, many = `${one}s`) => `${k} ${k === 1 ? one : many}`;

/** The instance a node stands for ("group:eng" for "group:eng#member"). @param {string} id */
const objectOf = (id) => {
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
  instCy.nodes().filter((/** @type {any} */ n) => isInstance(n) && objectOf(n.id()) === obj).addClass('expanded');
}

/**
 * Says what the graph did, in a line over the instance graph.
 * @param {string} text
 * @param {{ undo?: boolean }} [opts] undo: offer to undo the last expansion
 */
function graphNote(text, { undo = false } = {}) {
  const note = $('#instance-note');
  if (!note) return;
  $('.note-text', note).textContent = text;
  $('[data-act="undo"]', note).hidden = !undo;
  showNote(note, !!text, instCy);
}

/**
 * Places new nodes in a ring around a node already on the graph, so the
 * existing layout stays as it is (Re-layout tidies everything).
 * @param {any} added new nodes
 */
function placeNear(added) {
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
      instCy.nodes().filter((/** @type {any} */ n) => objectOf(n.id()) === o && !n.data('focus')).removeClass('expanded');
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
  saveExplore();
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
function hopFromOpen() {
  const obj = $('#instance-panel .instance')?.dataset.object;
  // Taken off the graph (cleared, or removed): put it back first.
  if (obj && !instCy?.getElementById(obj).length) showOpenInstance();
  const n = obj && instCy?.getElementById(obj);
  if (n?.length) { expandHop(n); return; }
  showCanvas('instances');
  graphNote('Open an instance first (click a node, or pick one on the right); +1 hop then expands around it. Shift+click a node to expand just that node.');
}

/** Undoes the last expansion or removal. */
function undo() {
  if (!undoLast || !instCy) return;
  const f = undoLast;
  undoLast = null;
  instCy.batch(f);
  graphNote('');
  updateCount();
  markEdgeLists();
  saveExplore();
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
  undoLast = () => { instCy.add(saved); };
  graphNote(text, { undo: true });
  updateCount();
  markEdgeLists();
  saveExplore();
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
function removeLeaves(around) {
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

/**
 * Closes the instance open in the panel: the side panel shows the type
 * section again; the instance stays on the graph, no longer marked open.
 */
function closeInstance() {
  const panel = $('#instance-panel');
  if (!panel || !$('.instance', panel)) return;
  panel.innerHTML = '';
  instCy?.nodes().removeClass('current');
  setInstanceFocus(false);
  const url = new URL(location.href);
  url.searchParams.delete('id');
  url.searchParams.delete('relation');
  history.replaceState(null, '', url);
  updateEmpty();
  saveExplore();
  $('#type-panel .ptitle')?.focus();
}

// ---- the node menu (right-click or long-press a node) ----

/** @type {any} the node the menu is for */
let menuNode = null;

/** @param {any} n @param {{ x: number, y: number }} at page (client) coordinates */
function openNodeMenu(n, at) {
  const menu = $('#node-menu');
  if (!menu || !isInstance(n)) return;
  menuNode = n;
  $('.nm-title', menu).textContent = n.data('label');
  menu.hidden = false;
  const r = menu.getBoundingClientRect();
  menu.style.left = `${Math.min(at.x, window.innerWidth - r.width - 8)}px`;
  menu.style.top = `${Math.min(at.y, window.innerHeight - r.height - 8)}px`;
  $('[role="menuitem"]', menu)?.focus();
}

function closeNodeMenu() {
  const menu = $('#node-menu');
  if (!menu || menu.hidden) return;
  menu.hidden = true;
  menuNode = null;
}

/** @param {string} act */
function nodeAction(act) {
  const n = menuNode;
  closeNodeMenu();
  if (!n || n.removed()) return;
  if (act === 'open') { toggleSide(false); loadInstance(objectOf(n.id())); }
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
    const items = $$('[role="menuitem"]', menu);
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

// ---- instance panel tabs (Edges / Derived / Incoming / Access) ----
// <fga-tabs> (ui.js) remembers the last tab per type and loads the lazy ones.

/**
 * Shows an instance panel tab, by name or by its panel.
 * @param {string | Element} which
 */
const showTab = (which) => tabsOf('#instance-panel .itabs')?.select(which) ?? Promise.resolve();

/** Opens the edge chooser behind a "100+" count node. @param {any} d */
function openChooser(d) {
  const tab = d.chooser.startsWith('in-') ? 'incoming' : 'edges';
  const go = () => showTab(tab).then(() => {
    const btn = $(`[data-chooser-btn="${CSS.escape(d.chooser)}"]`);
    if (!btn) return;
    const row = btn.closest('#instance-panel [id^="rel-"], #instance-panel tr');
    if (row) markRelation(row);
    btn.scrollIntoView({ block: 'center', behavior: scrollBehavior() });
    btn.click();
  });
  if ($('#instance-panel .instance')?.dataset.object === d.owner) go();
  else loadInstance(d.owner).then(go);
}

// ---- persistence (per browser tab) ----

function saveExplore() {
  if (!instCy || !$('#schema-canvas')) return;
  session.set(stateKey('explore'), {
    type: $('#type-panel [data-selected-type]')?.dataset.selectedType || '',
    object: $('#instance-panel .instance')?.dataset.object || '',
    canvas: $('#schema-canvas').hidden ? 'instances' : 'schema',
    elements: instCy.elements().jsons(),
  });
}

function restoreExplore() {
  const p = new URLSearchParams(location.search);
  if (p.get('type') || p.get('id')) return false; // an explicit link wins
  const saved = session.get(stateKey('explore'));
  if (!saved) return false;
  if (saved.elements?.length) { instCy.add(saved.elements); updateCount(); }
  const done = () => { showCanvas(saved.canvas || 'schema'); saveExplore(); };
  if (saved.object) loadInstance(saved.object).then(done);
  else { if (saved.type) selectType(saved.type); done(); }
  return true;
}

// ---- wiring ----

/**
 * Goes to relations in a panel (one row, or several from a type-graph edge):
 * switches to their tab or opens their section if needed, scrolls the first
 * into view only when it is out of view, marks them (until another is
 * chosen) and moves focus to the first, so it can be found in the list.
 * @param {...HTMLElement} targets
 */
function goToRelation(...targets) {
  const [first] = targets;
  if (!first) return;
  const sec = first.closest('.ipanel');
  const shown = sec instanceof HTMLElement && sec.hidden ? showTab(sec) : Promise.resolve();
  const det = first.closest('details');
  if (det) det.open = true;
  shown.then(() => {
    markRelation(...targets);
    for (const t of targets) {
      t.classList.remove('rel-arrive');
      void t.offsetWidth; // restart the arrival pulse
      t.classList.add('rel-arrive');
    }
    if (!first.hasAttribute('tabindex')) first.setAttribute('tabindex', '-1');
    first.focus({ preventScroll: true });
    first.scrollIntoView({ block: 'nearest', behavior: scrollBehavior() });
  });
}

/**
 * Marks the chosen relation (one at a time, or the relations of one
 * type-graph edge): the target of a relation link or a graph edge, or the
 * row someone clicks or tabs into.
 * @param {...Element} els
 */
function markRelation(...els) {
  const marked = $$('.rel-target');
  if (marked.length === els.length && els.every((el) => marked.includes(el))) return;
  marked.forEach((x) => x.classList.remove('rel-target', 'rel-arrive'));
  els.forEach((el) => el.classList.add('rel-target'));
}

/**
 * An instance-graph edge stands for a relation on its target (the object):
 * tapping it opens that instance, as tapping the node does, and goes to the
 * relation. A count edge opens its edge chooser, as its count node does.
 * @param {any} edge a Cytoscape edge
 */
function tapInstanceEdge(edge) {
  const count = edge.connectedNodes('[kind = "count"]');
  if (count.length) { openChooser(count.data()); return; }
  const d = /** @type {EdgeData} */ (edge.data());
  const rel = d.relation || d.label.split(' ')[0]; // graphs saved before edges carried it
  const go = () => {
    const row = document.getElementById(`rel-${rel}`);
    if (row) goToRelation(row);
  };
  if ($('#instance-panel .instance')?.dataset.object === d.target) go();
  else loadInstance(d.target).then(go);
}

/**
 * A type-graph edge stands for relations on its target type (several when
 * parallel edges are merged): tapping it selects that type, as tapping the
 * node does, and goes to those relations in the type panel.
 * @param {any} edge a Cytoscape edge
 */
function tapTypeEdge(edge) {
  const d = edge.data();
  selectType(d.target).then(() => {
    const rows = (d.relations || []).map((/** @type {string} */ r) => document.getElementById(`type-rel-${r}`)).filter(Boolean);
    goToRelation(...rows);
  });
}

/** The relation row (Edges or Derived tab) holding an element. @param {EventTarget | null} t */
const relationOf = (t) => (t instanceof Element ? t.closest('#instance-panel [id^="rel-"]') : null);

/** @param {MouseEvent} e */
function onClick(e) {
  const t = /** @type {Element} */ (e.target);
  const row = relationOf(t);
  if (row) markRelation(row);
  const chip = t.closest('.type-chip');
  if (chip instanceof HTMLElement) { selectType(chip.dataset.type || ''); showCanvas('schema'); return; }
  if (t.closest('[data-act="start-picking"]')) toggleSide(false);
  if (t.closest('[data-act="start-picking"]')) { startPicking(); return; }
  // The instance header: show the type section, or pick another instance.
  if (t.closest('[data-act="show-type"]')) { setInstanceFocus(!$('#explore-side')?.classList.contains('focus-instance')); return; }
  if (t.closest('[data-act="change-instance"]')) {
    setInstanceFocus(false);
    const p = picker('#type-panel');
    if (p) { p.q.scrollIntoView({ block: 'nearest', behavior: scrollBehavior() }); p.open(); }
    return;
  }
  // Links to a relation row (possibly in another tab): show and mark it.
  const anchor = t.closest('a.anchor[href^="#rel-"]');
  if (anchor) {
    const target = document.getElementById((anchor.getAttribute('href') || '').slice(1));
    if (target) { e.preventDefault(); goToRelation(target); }
    return;
  }
  if (t.closest('#schema-empty [data-act="show-all-types"]')) { showAllTypes(); return; }
  if (t.closest('#instance-panel [data-act="expand-hop"]')) { hopFromOpen(); return; }
  if (t.closest('[data-act="undo"]')) { undo(); return; }
  if (t.closest('#instance-panel [data-act="close-instance"]')) { closeInstance(); return; }
  const act = t.closest('.canvas-bar [data-act]');
  if (act instanceof HTMLElement) {
    const cy = activeCy();
    switch (act.dataset.act) {
      case 'fit': fitView(cy); break;
      case 'relayout': relayout(cy); break;
      case 'clear': // start over from the instance open in the panel, if any
        instCy.elements().remove();
        undoLast = null;
        graphNote('');
        showOpenInstance();
        graphChanged();
        break;
      case 'expand-hop': hopFromOpen(); break;
      case 'remove-leaves': removeLeaves(); break;
      case 'reset-explore': session.del(stateKey('explore')); location.assign(`${base()}/explore`); break;
      case 'show-all-types': showAllTypes(); break;
      case 'toggle-side': toggleSide(); break;
      default:
    }
    return;
  }
  // Walks to a peer (an edge list has already added the edge to the graph).
  const peer = t.closest('[data-explore]');
  if (peer instanceof HTMLElement && $('#instance-canvas')) {
    e.preventDefault();
    loadInstance(peer.dataset.explore || '');
  }
}

export function initExplore() {
  const sc = $('#schema-canvas');
  if (!sc) return;
  document.addEventListener('fgax:type-chosen', (/** @type {any} */ e) => { selectType(e.detail.type); showCanvas('schema'); });
  initTypeFinder();
  toggleSide(store.get('sideCollapsed', false));
  $('.canvas-wrap > .panel-rail')?.addEventListener('click', () => toggleSide(false));
  const setSideW = (/** @type {number} */ w) => { document.documentElement.style.setProperty('--side-w', `${Math.round(Math.min(Math.max(w, 320), window.innerWidth * 0.7))}px`); return w; };
  initResizer($('.side-resizer'), (x) => setSideW(window.innerWidth - x), setSideW, 'sideWidth', resizeAll);
  const p = pal();
  instCy = cytoscape({ container: $('#instance-canvas'), elements: [], style: instStyle(p), ...baseOpts });
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
    // Opening something from the graph fills the panel: show it.
    toggleSide(false);
    if (d.kind === 'count') openChooser(d); else loadInstance(d.id);
  });
  instCy.on('tap', 'edge', (/** @type {any} */ e) => { toggleSide(false); tapInstanceEdge(e.target); });
  initNodeMenu();
  fetch(`${base()}/api/schema`).then((r) => r.json()).then((data) => {
    const large = isLarge();
    const toggle = $('[data-act="color-modules"]');
    if (toggle) {
      toggle.checked = store.get('colorByModule', false);
      byModule = toggle.checked;
      toggle.addEventListener('change', () => colorByModule(toggle.checked));
    }
    // A large model is not laid out (or shown) until asked: only neighborhoods.
    schemaCy = cytoscape({ container: sc, elements: data.elements, style: schemaStyle(p),
      layout: large ? { name: 'null' } : dagre('LR', { rankSep: 150, nodeSep: 40 }), ...baseOpts });
    if (large) {
      schemaCy.elements().style('display', 'none');
      subLayout = true;
      $('.canvas-bar [data-act="show-all-types"]')?.classList.remove('hidden');
      schemaPrompt(true);
    } else fitView(schemaCy);
    if (byModule) colorByModule(true);
    // Choosing a type shows its details; it never opens the instance picker
    // over them (only "Pick … instance" and "/" do).
    schemaCy.on('tap', 'node', (/** @type {any} */ e) => { toggleSide(false); selectType(e.target.id()); });
    schemaCy.on('tap', 'edge', (/** @type {any} */ e) => { toggleSide(false); tapTypeEdge(e.target); });
    // Tapping the background shows every type again, except in large models
    // (it would bring back the unreadable whole).
    schemaCy.on('tap', (/** @type {any} */ e) => { if (e.target === schemaCy && !large) { schemaCy.elements().removeClass('faded sel'); showAllTypes(); } });
    const tp = $('#type-panel');
    const ip = $('#instance-panel');
    if (restoreExplore()) return;
    if (ip.dataset.initialType && ip.dataset.initialId) {
      loadInstance(`${ip.dataset.initialType}:${ip.dataset.initialId}`).then(() => {
        const rel = ip.dataset.initialRelation;
        const target = rel && document.getElementById(`rel-${rel}`);
        if (target) goToRelation(target);
      });
    } else if (tp.dataset.initialType) {
      selectType(tp.dataset.initialType);
    }
  });

  document.addEventListener('click', onClick);
  document.addEventListener('fgax:tab', (/** @type {any} */ e) => { if (e.target.id === 'graph-tabs') canvasShown(e.detail.name); });
  document.addEventListener('focusin', (e) => { const row = relationOf(e.target); if (row) markRelation(row); });
  // Edge lists ask for edges to be added or removed, and to be marked.
  document.addEventListener('fgax:edges', (/** @type {any} */ e) => {
    const { edges, add } = e.detail;
    for (const d of edges) { if (add) addEdge(d); else removeEdge(d); }
    graphChanged({ show: add });
  });
  document.addEventListener('fgax:edge-list', (/** @type {any} */ e) => e.target.mark(onGraph));
  initEdgeLists();
  document.addEventListener('fgax:picked', (/** @type {any} */ e) => { if (e.detail.mode === 'explore') loadInstance(e.detail.value); });
  onKey('fit', () => fitView(activeCy()));
  onKey('toggleText', () => toggleSide());
  onKey('find', () => {
    const box = picker('#type-panel') ?? typeFinder();
    if (box) box.open(); else $('.type-chip')?.focus();
  });
}



// The Explore view: the type graph, the instance graph built up as you open
// instances, the side panel (type and instance panels, rendered by the
// server), and the per-tab persistence of all of it.
import { $, $$, article, base, baseOpts, categoricalColor, dagre, fitCapped, labelWidth, pal, scrollBehavior, session, stateKey, store, typeColor } from './core.js';
import { openPicker } from './pickers.js';
import { onKey } from './shortcuts.js';
import { initTypeFinder, rememberType } from './typefinder.js';
import { initResizer, selectTab } from './ui.js';

/** @type {any} */ let schemaCy = null;
/** @type {any} */ let instCy = null;
// Whether the type graph shows a neighborhood laid out on its own.
let subLayout = false;
// Large models start with an empty type graph (see exploreData.Large).
const isLarge = () => $('.explore-layout')?.dataset.large === 'true';
// The "too many types to draw" prompt shows (on the type graph tab) until a
// type is chosen or every type is drawn.
let promptWanted = false;
let noteText = '';
// The prompt and the note belong to the type graph tab.
const updatePrompt = () => {
  const onSchema = !$('#schema-canvas')?.classList.contains('hidden');
  const el = $('#schema-empty');
  if (el) el.hidden = !(promptWanted && onSchema);
  const note = $('#schema-note');
  if (note) note.hidden = !(noteText && onSchema);
};
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
let relayoutTimer = 0;

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
    } },
    { selector: 'node[?focus]', style: { 'border-width': 4 } },
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
    resizeAll();
    fitCapped(activeCy());
  }
}

const activeCy = () => ($('#schema-canvas')?.classList.contains('hidden') ? instCy : schemaCy);
const resizeAll = () => { instCy?.resize(); schemaCy?.resize(); };

/** @param {'schema' | 'instances'} which */
function showCanvas(which) {
  const tab = $(`.canvas-bar [data-canvas="${which}"]`);
  if (tab) selectTab(tab);
  $('#schema-canvas')?.classList.toggle('hidden', which !== 'schema');
  $('#instance-canvas')?.classList.toggle('hidden', which !== 'instances');
  // The side panel follows the canvas: types on the type graph, the open
  // instance (kept, not discarded) on the instance graph.
  $('#explore-side')?.classList.toggle('on-schema', which === 'schema');
  setInstanceFocus(which === 'instances' && !!$('#instance-panel .instance'));
  $$('.canvas-wrap .legend').forEach((l) => l.classList.toggle('hidden', l.dataset.for !== which));
  const cy = which === 'schema' ? schemaCy : instCy;
  if (cy) { cy.resize(); fitCapped(cy); }
  updateEmpty();
  updatePrompt();
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
  if (!$('#schema-canvas').classList.contains('hidden')) fitCapped(schemaCy, schemaCy.elements(':visible'));
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
  fitCapped(schemaCy);
}

/**
 * @param {string} t
 * @param {{ loadPanel?: boolean, browse?: boolean }} [opts] browse: the person
 *   is choosing a type (not opening an instance), so the type section shows
 */
function selectType(t, { loadPanel = true, browse = true } = {}) {
  if (browse) setInstanceFocus(false);
  rememberType(t);
  if (schemaCy) focusType(t);
  const panel = $('#type-panel');
  if (loadPanel && panel && panel.querySelector('[data-selected-type]')?.dataset.selectedType !== t) {
    return htmx.ajax('GET', `${base()}/explore/type?type=${encodeURIComponent(t)}`, { target: '#type-panel', swap: 'innerHTML' });
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
  return htmx.ajax('GET', `${base()}/explore/instance?object=${encodeURIComponent(obj)}`, { target: '#instance-panel', swap: 'innerHTML' });
}

// ---- instance graph ----

/** @param {any} cy */
function relayout(cy) {
  if (!cy || cy.elements().length === 0) return;
  cy.layout(dagre('LR', { fit: false, rankSep: 130, nodeSep: 18 })).run();
  fitCapped(cy);
}

/** Adds server-computed elements (an instance's edges) to the instance graph. @param {any} patch */
function applyPatch(patch) {
  if (!instCy) return;
  let added = 0;
  instCy.batch(() => {
    for (const el of patch.elements || []) {
      if (instCy.getElementById(el.data.id).length) continue;
      if (el.group === 'edges' && (!instCy.getElementById(el.data.source).length || !instCy.getElementById(el.data.target).length)) continue;
      instCy.add(el); added++;
    }
    instCy.nodes().removeClass('current');
    if (patch.focus) instCy.getElementById(patch.focus).addClass('current');
  });
  updateCount();
  syncGraphMarks();
  showCanvas('instances');
  if (added) relayout(instCy);
  const f = instCy.getElementById(patch.focus);
  if (f.length) instCy.animate({ center: { eles: f }, duration: 250 });
}

/** @typedef {{ source: string, target: string, label: string, sourceLabel?: string, targetLabel?: string }} EdgeData */

/** @param {EdgeData} d */
const edgeId = (d) => `${d.source}|${d.label}|${d.target}`;
/** @param {EdgeData} d */
const onGraph = (d) => !!instCy && instCy.getElementById(edgeId(d)).length > 0;
/** @param {Element} el @returns {EdgeData} */
const edgeData = (el) => JSON.parse(/** @type {HTMLElement} */ (el.closest('[data-edge]')).dataset.edge || '{}');

/** @param {EdgeData} d */
function addEdge(d) {
  if (!instCy || !d) return;
  instCy.batch(() => {
    for (const [nid, lbl] of [[d.source, d.sourceLabel], [d.target, d.targetLabel]]) {
      if (!instCy.getElementById(nid).length) instCy.add({ group: 'nodes', data: { id: nid, label: lbl || nid, type: String(nid).split(':')[0] } });
    }
    if (!instCy.getElementById(edgeId(d)).length) instCy.add({ group: 'edges', data: { id: edgeId(d), source: d.source, target: d.target, label: d.label } });
    const anchor = $('#instance-panel .instance')?.dataset.object;
    if (anchor) instCy.getElementById(anchor).addClass('current');
  });
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
  const showing = !$('#instance-canvas').classList.contains('hidden');
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
  const q = $('#type-panel .picker-q');
  if (q) { openPicker(q); return; }
  const find = $('.type-q');
  if (find) { find.focus(); return; }
  $$('.type-chip').forEach((c) => { c.classList.remove('nudge'); void c.offsetWidth; c.classList.add('nudge'); });
}

/** @param {{ show?: boolean }} [opts] */
function graphChanged({ show = false } = {}) {
  updateCount();
  syncGraphMarks();
  saveExplore();
  if (show) showCanvas('instances');
  clearTimeout(relayoutTimer);
  relayoutTimer = window.setTimeout(() => relayout(instCy), 120);
}

/** The edge lists mirror the graph: checked (or "−") means the edge is on it. */
export function syncGraphMarks() {
  if (!instCy) return;
  for (const li of $$('#instance-panel [data-edge]')) {
    const on = onGraph(JSON.parse(li.dataset.edge));
    const cb = $('.edge-check', li);
    if (cb) cb.checked = on;
    const b = $('.add-edge', li);
    if (b) {
      b.textContent = on ? '−' : '＋';
      b.classList.toggle('on', on);
      b.title = on ? 'Remove this edge from the graph' : 'Add this edge to the graph';
      b.setAttribute('aria-label', b.title);
    }
  }
  for (const ch of $$('#instance-panel .chooser')) {
    const items = $$('[data-edge]', ch);
    const n = items.filter((li) => onGraph(JSON.parse(li.dataset.edge))).length;
    for (const el of $$('.sel-count', ch)) el.textContent = items.length ? `${n} of ${items.length} shown are on the graph` : '';
    for (const b of $$('.add-shown', ch)) b.disabled = n === items.length;
    for (const b of $$('.remove-shown', ch)) b.disabled = n === 0;
  }
}

// ---- instance panel tabs (Edges / Derived / Incoming) ----

/** @param {HTMLElement | null} panel @param {string} name */
function showTab(panel, name) {
  if (!panel) return Promise.resolve();
  const btn = $(`.itabs [data-tab="${name}"]`, panel);
  if (btn) selectTab(btn);
  for (const sec of $$('.ipanel', panel)) sec.hidden = sec.dataset.panel !== name;
  session.set(stateKey('itab'), { type: (panel.dataset.object || '').split(':')[0], tab: name });
  const sec = $(`.ipanel[data-panel="${name}"]`, panel);
  if (btn?.dataset.src && !sec.dataset.loaded) {
    sec.dataset.loaded = '1';
    return htmx.ajax('GET', btn.dataset.src, { target: sec, swap: 'innerHTML' });
  }
  return Promise.resolve();
}

/** Reuses the last tab only for instances of the same type. @param {HTMLElement | null} panel */
function initInstanceTabs(panel) {
  if (!panel) return;
  const saved = session.get(stateKey('itab'));
  const type = (panel.dataset.object || '').split(':')[0];
  const want = saved?.type === type ? saved.tab : null;
  const has = (/** @type {string} */ n) => !!$(`.itabs [data-tab="${n}"]`, panel);
  showTab(panel, want && has(want) ? want : panel.dataset.defaultTab || 'edges');
}

/** Opens the edge chooser behind a "100+" count node. @param {any} d */
function openChooser(d) {
  const tab = d.chooser.startsWith('in-') ? 'incoming' : 'edges';
  const go = () => showTab($('#instance-panel .instance'), tab).then(() => {
    const btn = $(`[data-chooser-btn="${CSS.escape(d.chooser)}"]`);
    if (btn) { btn.scrollIntoView({ block: 'center', behavior: scrollBehavior() }); btn.click(); }
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
    canvas: $('#schema-canvas').classList.contains('hidden') ? 'instances' : 'schema',
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
 * Goes to a relation in the instance panel: switches to its tab if needed,
 * scrolls it into view only when it is out of view, marks it (until another
 * is chosen) and moves focus to it, so it can be found in the list.
 * @param {HTMLElement} target
 */
function goToRelation(target) {
  const sec = target.closest('.ipanel');
  const shown = sec instanceof HTMLElement && sec.hidden ? showTab(sec.closest('.instance'), sec.dataset.panel || '') : Promise.resolve();
  shown.then(() => {
    markRelation(target);
    target.classList.remove('rel-arrive');
    void target.offsetWidth; // restart the arrival pulse
    target.classList.add('rel-arrive');
    if (!target.hasAttribute('tabindex')) target.setAttribute('tabindex', '-1');
    target.focus({ preventScroll: true });
    target.scrollIntoView({ block: 'nearest', behavior: scrollBehavior() });
  });
}

/**
 * Marks the chosen relation (one at a time): the target of a relation link,
 * or the row someone clicks or tabs into.
 * @param {Element} el
 */
function markRelation(el) {
  if (el.classList.contains('rel-target')) return;
  $$('.rel-target').forEach((x) => x.classList.remove('rel-target', 'rel-arrive'));
  el.classList.add('rel-target');
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
  const canvasTab = t.closest('[data-canvas]');
  if (canvasTab instanceof HTMLElement) { showCanvas(/** @type {any} */ (canvasTab.dataset.canvas)); saveExplore(); return; }
  const tab = t.closest('.itabs [data-tab]');
  if (tab instanceof HTMLElement) { showTab(tab.closest('.instance'), tab.dataset.tab || ''); return; }
  // The instance header: show the type section, or pick another instance.
  if (t.closest('[data-act="show-type"]')) { setInstanceFocus(!$('#explore-side')?.classList.contains('focus-instance')); return; }
  if (t.closest('[data-act="change-instance"]')) {
    setInstanceFocus(false);
    const q = $('#type-panel .picker-q');
    if (q) { q.scrollIntoView({ block: 'nearest', behavior: scrollBehavior() }); openPicker(q); }
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
  const act = t.closest('.canvas-bar [data-act]');
  if (act instanceof HTMLElement) {
    const cy = activeCy();
    switch (act.dataset.act) {
      case 'fit': fitCapped(cy); break;
      case 'relayout': relayout(cy); break;
      case 'clear': instCy.elements().remove(); graphChanged(); break;
      case 'reset-explore': session.del(stateKey('explore')); location.assign(`${base()}/explore`); break;
      case 'show-all-types': showAllTypes(); break;
      case 'toggle-side': toggleSide(); break;
      default:
    }
    return;
  }
  const toggle = t.closest('.add-edge');
  if (toggle) {
    const d = edgeData(toggle);
    const adding = !onGraph(d);
    if (adding) addEdge(d); else removeEdge(d);
    graphChanged({ show: adding });
    return;
  }
  const peer = t.closest('[data-explore]');
  if (peer instanceof HTMLElement && $('#instance-canvas')) {
    e.preventDefault();
    const li = peer.closest('.edge');
    if (li instanceof HTMLElement && li.dataset.edge) addEdge(JSON.parse(li.dataset.edge));
    loadInstance(peer.dataset.explore || '');
    return;
  }
  const bulk = t.closest('.add-shown, .remove-shown');
  if (bulk) {
    const adding = bulk.classList.contains('add-shown');
    for (const li of $$('[data-edge]', /** @type {Element} */ (bulk.closest('.chooser')))) {
      const d = JSON.parse(li.dataset.edge);
      if (adding) addEdge(d); else removeEdge(d);
    }
    graphChanged({ show: adding });
  }
}

export function initExplore() {
  const sc = $('#schema-canvas');
  if (!sc) return;
  initTypeFinder((t) => { selectType(t); showCanvas('schema'); });
  toggleSide(store.get('sideCollapsed', false));
  $('.canvas-wrap > .panel-rail')?.addEventListener('click', () => toggleSide(false));
  const setSideW = (/** @type {number} */ w) => { document.documentElement.style.setProperty('--side-w', `${Math.round(Math.min(Math.max(w, 320), window.innerWidth * 0.7))}px`); return w; };
  initResizer($('.side-resizer'), (x) => setSideW(window.innerWidth - x), setSideW, 'sideWidth', resizeAll);
  const p = pal();
  instCy = cytoscape({ container: $('#instance-canvas'), elements: [], style: instStyle(p), ...baseOpts });
  instCy.on('tap', 'node', (/** @type {any} */ e) => {
    const d = e.target.data();
    // Opening something from the graph fills the panel: show it.
    toggleSide(false);
    if (d.kind === 'count') openChooser(d); else loadInstance(d.id);
  });
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
    } else fitCapped(schemaCy);
    if (byModule) colorByModule(true);
    // Choosing a type shows its details; it never opens the instance picker
    // over them (only "Pick … instance" and "/" do).
    schemaCy.on('tap', 'node', (/** @type {any} */ e) => { toggleSide(false); selectType(e.target.id()); });
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
  document.addEventListener('focusin', (e) => { const row = relationOf(e.target); if (row) markRelation(row); });
  document.addEventListener('change', (e) => {
    const t = /** @type {HTMLInputElement} */ (e.target);
    if (!t.matches('.edge-check')) return;
    const d = edgeData(t);
    if (t.checked) addEdge(d); else removeEdge(d);
    graphChanged({ show: t.checked });
  });
  document.addEventListener('fgax:picked', (/** @type {any} */ e) => { if (e.detail.mode === 'explore') loadInstance(e.detail.value); });
  onKey('fit', () => fitCapped(activeCy()));
  onKey('toggleText', () => toggleSide());
  onKey('find', () => {
    const q = $('#type-panel .picker-q');
    if (q) openPicker(q); else ($('.type-q') || $('.type-chip'))?.focus();
  });
}

/** After htmx swaps content into the page. @param {Element} t */
export function exploreSwapped(t) {
  if (!instCy) return;
  // Any loaded fragment (instance panel, incoming tab) may carry graph updates.
  for (const patch of $$('.graph-patch', t)) {
    if (patch.dataset.applied) continue;
    patch.dataset.applied = '1';
    applyPatch(JSON.parse(patch.textContent));
  }
  if (t.id === 'instance-panel') {
    initInstanceTabs($('.instance', t));
    if ($('.instance', t)) setInstanceFocus(true);
    const det = $('#type-panel .type-details');
    if (det) det.open = false;
    t.scrollIntoView({ block: 'start', behavior: scrollBehavior() });
  }
  if (t.id === 'instance-panel' || t.id === 'type-panel') saveExplore();
  syncGraphMarks();
}

/**
 * After htmx has processed new content.
 * @param {Element} t
 */
export function exploreSettled(t) {
  // Settling puts back the server's class attributes on elements whose id
  // the previous instance also had (the tabs), dropping the selected tab's
  // mark set at swap time: mark it again.
  if (t.id === 'instance-panel') {
    const on = $('.itabs [aria-selected="true"]', t);
    if (on) selectTab(on);
  }
  if (t.id === 'type-panel') updateEmpty();
}

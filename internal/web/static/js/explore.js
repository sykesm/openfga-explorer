// @ts-check
// The Explore view: the type graph (schemagraph.js), the instance graph built
// up as you open instances (instancegraph.js), the side panel (type and
// instance panels, rendered by the server), and the per-tab persistence of
// all of it. This module wires them together.
import { $, $$, base, load, rememberObject, scrollBehavior, session, stateKey, store } from './core.js';
import { initEdgeLists } from './edgelist.js';
import { activeCy, fitView, graphs, relayout, resizeAll, showCanvas, wasFitted } from './exploregraphs.js';
import { addEdge, clearGraph, graphChanged, hopFromOpen, initInstanceGraph, onGraph, removeEdge, removeLeaves, undo, updateCount, updateEmpty } from './instancegraph.js';
import { picker } from './pickers.js';
import { focusType, initSchemaGraph, showAllTypes, updatePrompt } from './schemagraph.js';
import { onKey } from './shortcuts.js';
import { initTypeFinder } from './typefinder.js';
import { initResizer, tabsOf } from './ui.js';

/** @typedef {{ open(): void, remember(t: string): void }} TypeFinder <fga-type-finder> (typefinder.js) */
/** @returns {TypeFinder | null} */
const typeFinder = () => /** @type {any} */ ($('fga-type-finder:defined'));

/** @typedef {import('./instancegraph.js').EdgeData} EdgeData */

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
  const cy = which === 'schema' ? graphs.schema : graphs.inst;
  if (cy) { cy.resize(); if (!wasFitted(cy) && cy.elements(':visible').length) fitView(cy); }
  updateEmpty();
  updatePrompt();
  saveExplore();
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
  focusType(t);
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
  const det = /** @type {HTMLDetailsElement | null} */ ($('#type-panel .type-details'));
  if (det) det.open = false;
  panel?.scrollIntoView({ block: 'start', behavior: scrollBehavior() });
  saveExplore();
}

function startPicking() {
  const p = picker('#type-panel');
  if (p) { p.open(); return; }
  const finder = typeFinder();
  if (finder) { finder.open(); return; }
  $$('.type-chip').forEach((c) => { c.classList.remove('nudge'); void c.offsetWidth; c.classList.add('nudge'); });
}

/**
 * Closes the instance open in the panel: the side panel shows the type
 * section again; the instance stays on the graph, no longer marked open.
 */
function closeInstance() {
  const panel = $('#instance-panel');
  if (!panel || !$('.instance', panel)) return;
  panel.innerHTML = '';
  graphs.inst?.nodes().removeClass('current');
  setInstanceFocus(false);
  const url = new URL(location.href);
  url.searchParams.delete('id');
  url.searchParams.delete('relation');
  history.replaceState(null, '', url);
  updateEmpty();
  saveExplore();
  /** @type {HTMLElement | null} */ ($('#type-panel .ptitle'))?.focus();
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
    const btn = /** @type {HTMLElement | null} */ ($(`[data-chooser-btn="${CSS.escape(d.chooser)}"]`));
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
  const sc = /** @type {HTMLElement | null} */ ($('#schema-canvas'));
  if (!graphs.inst || !sc) return;
  session.set(stateKey('explore'), {
    type: $('#type-panel [data-selected-type]')?.dataset.selectedType || '',
    object: $('#instance-panel .instance')?.dataset.object || '',
    canvas: sc.hidden ? 'instances' : 'schema',
    elements: graphs.inst.elements().jsons(),
  });
}

function restoreExplore() {
  const p = new URLSearchParams(location.search);
  if (p.get('type') || p.get('id')) return false; // an explicit link wins
  const saved = session.get(stateKey('explore'));
  if (!saved) return false;
  if (saved.elements?.length) { graphs.inst.add(saved.elements); updateCount(); }
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
      case 'clear': clearGraph(); break;
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
  const sc = /** @type {HTMLElement | null} */ ($('#schema-canvas'));
  if (!sc) return;
  document.addEventListener('fgax:type-chosen', (/** @type {any} */ e) => { selectType(e.detail.type); showCanvas('schema'); });
  initTypeFinder();
  toggleSide(store.get('sideCollapsed', false));
  $('.canvas-wrap > .panel-rail')?.addEventListener('click', () => toggleSide(false));
  const setSideW = (/** @type {number} */ w) => { document.documentElement.style.setProperty('--side-w', `${Math.round(Math.min(Math.max(w, 320), window.innerWidth * 0.7))}px`); return w; };
  initResizer($('.side-resizer'), (x) => setSideW(window.innerWidth - x), setSideW, 'sideWidth', resizeAll);
  // Opening something from the graph fills the panel: show it.
  initInstanceGraph(/** @type {HTMLElement} */ ($('#instance-canvas')), {
    changed: saveExplore,
    open: (obj) => { toggleSide(false); loadInstance(obj); },
    chooser: (d) => { toggleSide(false); openChooser(d); },
    edge: (edge) => { toggleSide(false); tapInstanceEdge(edge); },
  });
  fetch(`${base()}/api/schema`).then((r) => r.json()).then((data) => {
    // Choosing a type shows its details; it never opens the instance picker
    // over them (only "Pick … instance" and "/" do).
    initSchemaGraph(sc, data, {
      type: (t) => { toggleSide(false); selectType(t); },
      edge: (edge) => { toggleSide(false); tapTypeEdge(edge); },
    });
    const tp = /** @type {HTMLElement} */ ($('#type-panel'));
    const ip = /** @type {HTMLElement} */ ($('#instance-panel'));
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
    if (box) box.open(); else /** @type {HTMLElement | null} */ ($('.type-chip'))?.focus();
  });
}

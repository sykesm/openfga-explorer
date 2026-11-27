// The check trace view: the evaluation graph, its replay, the highlighted
// granting path, folding of failed branches, the text trace, and exports.
// The server computes what to show (granting path, folds, node details);
// this module draws it and handles interaction.
import { $, $$, baseOpts, color, dagre, esc, fitCapped, labelHeight, labelWidth, mix, pal, reducedMotion, scrollBehavior, session, stateKey, store, whenLoaded } from './core.js';
import { onEscape, onKey } from './shortcuts.js';
import { initResizer } from './ui.js';

/**
 * @typedef {{ id: string, parent: string, label: string, hidden: string[] }} Fold
 * @typedef {{ phase: string, node: string, from?: string }} Step
 * @typedef {{ elements: any[], steps: Step[], root: string, folds?: Fold[] }} GraphData
 */

// Node labels carry an outcome mark (✓ ✗ ? ·) so outcomes never depend on color.
/** @type {Record<string, string>} */
const MARK = { allowed: '✓', denied: '✗', unknown: '?', error: '!', skipped: '·' };
// Skipped branches with a server verdict show it, marked as not walked.
/** @type {Record<string, string>} */
const HINT = { allowed: '(✓)', denied: '(✗)' };

/** @param {any} e @param {boolean} [short] */
function markedLabel(e, short = false) {
  const d = e.data();
  const text = short ? (d.short || d.label) : d.label;
  if (d.kind === 'subject' || d.kind === 'more' || d.kind === 'fold' || !MARK[d.outcome]) return text;
  if (d.kind === 'skipped' && HINT[d.hint]) return `${HINT[d.hint]} ${text}`;
  return `${MARK[d.outcome]} ${text}`;
}

/** @param {ReturnType<typeof pal>} p */
function traceStyle(p) {
  /** @type {Record<string, string>} */
  const oc = { allowed: p.allowed, denied: p.denied, unknown: p.unknown, error: p.error, skipped: p.skipped };
  const s = [
    { selector: 'node', style: {
      shape: 'round-rectangle', label: (/** @type {any} */ e) => markedLabel(e), 'text-wrap': 'wrap', 'font-size': 11, color: p.fg, 'text-valign': 'center',
      'background-color': p.panel, 'border-width': 2, 'border-color': p.line,
      width: (/** @type {any} */ e) => labelWidth(e.data('label')), height: (/** @type {any} */ e) => labelHeight(e.data('label')),
      'transition-property': 'opacity, background-color, border-color', 'transition-duration': reducedMotion() ? '0ms' : '180ms',
    } },
    { selector: 'node.union, node.intersection, node.exclusion', style: { shape: 'ellipse', 'font-size': 12, width: 64, height: 34 } },
    { selector: 'node.direct', style: { shape: 'tag', 'font-size': 10 } },
    { selector: 'node.ttu', style: { shape: 'cut-rectangle', 'font-size': 10 } },
    { selector: 'node.subject', style: { shape: 'round-diamond', 'background-color': p.accent, color: '#fff', 'font-weight': 700, height: 44, width: (/** @type {any} */ e) => labelWidth(e.data('label')) + 30 } },
    { selector: 'node.blocked', style: { shape: 'octagon' } },
    { selector: 'node.cycle', style: { 'border-style': 'double', 'border-width': 4 } },
    { selector: 'node.more', style: { 'border-style': 'dashed', color: p.muted, 'font-style': 'italic', 'font-size': 10 } },
    // De-emphasis never uses opacity on labels: faded text drops below
    // readable contrast (and multiplies with .dim). Skipped nodes are dashed,
    // italic, on the page background, in solid muted colors.
    { selector: 'node.skipped', style: { 'border-style': 'dashed', 'background-color': p.bg, color: p.muted, 'font-style': 'italic' } },
    { selector: 'node.root', style: { 'border-width': 4, 'font-weight': 700 } },
    { selector: 'edge', style: {
      'curve-style': 'bezier', 'target-arrow-shape': 'triangle', width: 1.6, 'line-color': p.line, 'target-arrow-color': p.line,
      label: 'data(label)', 'font-size': 9, color: p.muted, 'text-background-color': p.bg, 'text-background-opacity': 0.9,
      'text-background-padding': 2, 'text-wrap': 'ellipsis', 'text-max-width': 200,
      'transition-property': 'opacity, line-color', 'transition-duration': reducedMotion() ? '0ms' : '180ms',
    } },
    { selector: 'edge.skipped', style: { 'line-style': 'dashed', width: 1.2 } },
    // Failed edges are dashed so allowed/denied never relies on color alone.
    { selector: 'edge.denied, edge.error', style: { 'line-style': 'dashed', 'line-dash-pattern': [6, 4] } },
    { selector: 'node.fold', style: {
      shape: 'round-rectangle', 'border-style': 'dashed', 'border-color': p.denied, color: p.muted, 'font-style': 'italic',
      'background-color': p.panel, 'font-size': 10.5, cursor: 'pointer',
    } },
    { selector: 'edge.fold', style: { 'line-style': 'dashed', 'line-color': p.denied, 'target-arrow-color': p.denied, opacity: 0.7 } },
    // Zoomed out: short labels that stay legible.
    { selector: 'node.far', style: { label: (/** @type {any} */ e) => markedLabel(e, true), 'font-size': 18, 'font-weight': 700 } },
    { selector: 'edge.far', style: { label: '' } },
    { selector: 'edge.blocking', style: { width: 3.5, 'line-style': 'dashed' } },
    { selector: 'edge.probe', style: { 'line-style': 'dotted', width: 2.5 } },
    { selector: 'edge.more', style: { 'line-style': 'dotted', 'target-arrow-shape': 'none' } },
  ];
  for (const [k, c] of Object.entries(oc)) {
    s.push({ selector: `node.${k}`, style: { 'border-color': c } });
    s.push({ selector: `edge.${k}`, style: { 'line-color': c, 'target-arrow-color': c } });
  }
  s.push(
    { selector: 'edge.blocking', style: { 'line-color': p.denied, 'target-arrow-color': p.denied, color: p.denied } },
    // Cytoscape accepts neither :not() nor color-mix(): tint with a data selector and a precomputed blend.
    { selector: 'node.allowed[kind != "subject"]', style: { 'background-color': mix(p.allowed, p.panel, 0.14) } },
    { selector: 'node.denied[kind != "subject"]', style: { 'background-color': mix(p.denied, p.panel, 0.12) } },
    { selector: 'node.mismatch', style: { 'border-color': p.mismatch, 'border-width': 5 } },
    { selector: '.pending', style: { opacity: 0.1 } },
    // Off the granting path while it is highlighted: shapes recede, labels stay legible.
    { selector: 'node.dim', style: { 'border-opacity': 0.45, 'background-opacity': 0.5, color: p.muted } },
    { selector: 'edge.dim', style: { 'line-color': p.skipped, 'target-arrow-color': p.skipped, width: 1.2, color: p.muted } },
    // Skipped nodes are already de-emphasized (dashed, italic): don't fade them twice.
    { selector: 'node.skipped.dim', style: { 'border-opacity': 1, 'background-opacity': 1 } },
    { selector: 'edge.flow', style: {
      'line-style': 'dashed', 'line-dash-pattern': [10, 6], width: 3.6, 'line-color': p.allowed, 'target-arrow-color': p.allowed,
      'arrow-scale': 1.25, color: p.allowed, 'z-index': 20, opacity: 1,
    } },
    { selector: 'node.flow', style: {
      'border-width': 3, 'border-color': p.allowed, 'z-index': 20, opacity: 1,
      'underlay-color': p.allowed, 'underlay-padding': 8, 'underlay-opacity': 0, 'underlay-shape': 'round-rectangle',
    } },
    // "N granting tuples" pressed: the tuples that grant access, labeled, on every path.
    { selector: 'edge.grant-on', style: {
      'line-style': 'solid', width: 6, 'line-color': p.allowed, 'target-arrow-color': p.allowed, 'arrow-scale': 1.3,
      label: 'data(tuple)', 'font-size': 10, 'font-weight': 700, color: p.fg, 'text-background-color': p.bg, 'text-background-opacity': 1,
      'text-border-width': 1, 'text-border-color': p.allowed, 'text-border-opacity': 1, 'text-max-width': 360, 'z-index': 40,
    } },
    { selector: 'edge.tuple-on', style: {
      label: 'data(tuple)', 'font-size': 9.5, color: p.fg, 'text-background-color': p.bg, 'text-background-opacity': 1, 'text-max-width': 360, 'z-index': 30,
    } },
    { selector: 'node.visiting', style: { 'overlay-color': p.accent, 'overlay-opacity': 0.25, 'overlay-padding': 8, opacity: 1 } },
    { selector: 'node:selected', style: { 'overlay-color': p.accent, 'overlay-opacity': 0.18, 'overlay-padding': 6 } },
    { selector: '.hl', style: { 'overlay-color': p.accent, 'overlay-opacity': 0.22, 'overlay-padding': 6 } },
  );
  return s;
}

// ---- folds (computed by the server, see trace.Fold) ----

/** @param {any} cy @param {Fold[]} folds */
function applyFolds(cy, folds) {
  cy.batch(() => {
    for (const f of folds) {
      cy.add([
        { group: 'nodes', data: { id: f.id, kind: 'fold', label: f.label, hidden: f.hidden }, classes: 'fold' },
        { group: 'edges', data: { id: `${f.id}-e`, source: f.parent, target: f.id, label: '' }, classes: 'fold' },
      ]);
    }
    for (const f of folds) for (const id of f.hidden) cy.getElementById(id).style('display', 'none');
  });
}

/** @param {any} cy @param {any} fold */
function expandFold(cy, fold) {
  cy.batch(() => {
    for (const id of fold.data('hidden') || []) cy.getElementById(id).style('display', 'element');
    fold.remove();
  });
}

/** Expands whatever folds hide the element (outermost first). @param {any} cy @param {string} id */
function reveal(cy, id) {
  const fold = cy.nodes('.fold').filter((/** @type {any} */ f) => (f.data('hidden') || []).includes(id))[0];
  if (!fold) return false;
  if (fold.style('display') === 'none') reveal(cy, fold.id());
  expandFold(cy, fold);
  return true;
}

/** The general layout: granting-path edges weigh heavily so dagre keeps the path short. @param {any} cy */
const dagreLayout = (cy) => cy.elements(':visible').layout(dagre('TB', {
  nodeSep: 18, rankSep: 44, fit: false,
  edgeWeight: (/** @type {any} */ e) => (e.hasClass('flowpath') ? 20 : 1),
})).run();

/**
 * The spine of the granting path: the longest chain of granting edges from
 * the root, or null when nothing was granted. Other granting branches (e.g.
 * the second side of an "and") are laid out beside it.
 * @param {any} cy
 */
function grantingChain(cy) {
  const root = cy.nodes('.root.flowpath:visible');
  if (!root.length) return null;
  /** @type {Map<string, number>} */
  const longest = new Map();
  /** @param {any} n @param {Set<string>} seen */
  const depth = (n, seen) => {
    if (longest.has(n.id())) return longest.get(n.id()) || 0;
    if (seen.has(n.id())) return 0;
    seen.add(n.id());
    let best = 0;
    n.outgoers('edge.flowpath').targets(':visible').forEach((/** @type {any} */ c) => { best = Math.max(best, depth(c, seen)); });
    longest.set(n.id(), best + 1);
    return best + 1;
  };
  depth(root[0], new Set());
  const chain = [root[0]];
  for (let n = root[0]; ;) {
    let next = null;
    n.outgoers('edge.flowpath').targets(':visible').forEach((/** @type {any} */ c) => {
      if (!chain.includes(c) && (!next || (longest.get(c.id()) || 0) > (longest.get(next.id()) || 0))) next = c;
    });
    if (!next) break;
    chain.push(next);
    n = next;
  }
  return chain.length > 1 ? chain : null;
}

/**
 * Lays the trace out with the granting path as a straight vertical spine
 * (object at the top, user at the bottom); each spine node's other branches
 * (including other granting routes) are laid out beside it, alternating
 * sides. Falls back to dagre when nothing was granted.
 * @param {any} cy
 */
function traceLayout(cy) {
  const chain = grantingChain(cy);
  if (!chain) { dagreLayout(cy); return; }
  const onChain = new Set(chain.map((n) => n.id()));
  const placed = new Set(onChain);
  const gap = 44, side = 70;
  let y = 0;
  let right = true;
  cy.batch(() => {
    for (const p of chain) {
      // The branches hanging off this path node (not yet placed elsewhere).
      let block = cy.collection();
      const queue = [p];
      while (queue.length) {
        const n = queue.shift();
        n.outgoers('node:visible').forEach((/** @type {any} */ c) => {
          if (placed.has(c.id())) return;
          placed.add(c.id());
          block = block.union(c);
          queue.push(c);
        });
      }
      const h = p.outerHeight();
      p.position({ x: 0, y: y + h / 2 });
      let slot = h;
      if (block.length) {
        const sub = block.union(block.edgesWith(block));
        sub.layout(dagre('TB', { nodeSep: 14, rankSep: 36, fit: false })).run();
        const bb = block.boundingBox();
        const dx = right ? side + p.outerWidth() / 2 - bb.x1 : -side - p.outerWidth() / 2 - bb.x2;
        const dy = y - bb.y1;
        block.forEach((/** @type {any} */ n) => { const pos = n.position(); n.position({ x: pos.x + dx, y: pos.y + dy }); });
        slot = Math.max(h, bb.h);
        right = !right;
      }
      y += slot + gap;
    }
    // Anything not reached from the path goes underneath.
    const rest = cy.nodes(':visible').filter((/** @type {any} */ n) => !placed.has(n.id()));
    if (rest.length) {
      rest.union(rest.edgesWith(rest)).layout(dagre('TB', { nodeSep: 18, rankSep: 44, fit: false })).run();
      const bb = rest.boundingBox();
      rest.forEach((/** @type {any} */ n) => { const pos = n.position(); n.position({ x: pos.x - bb.x1 - bb.w / 2, y: pos.y - bb.y1 + y + gap }); });
    }
  });
}

/**
 * PathFlow keeps the focus on how access was granted: once the replay ends
 * with an allowed decision, the granting path (elements the server marked
 * "flowpath") gets dashes flowing in the direction of the arrows and a glow
 * travelling from the object down to the user. The rest is only dimmed.
 */
class PathFlow {
  /** @param {any} cy @param {Element} tree */
  constructor(cy, tree) {
    this.cy = cy;
    this.tree = tree;
    this.raf = 0;
    this.focus = true;
    this.started = false;
    // Set once the person takes charge of the view (zooms, pans, fits,
    // selects, or steps): from then on the view is theirs and is not reframed.
    this.userView = false;
    this.edges = cy.edges('.flowpath');
    this.nodes = cy.nodes('.flowpath');
  }
  get active() { return this.edges.length > 0; }
  start() {
    this.stop();
    if (!this.active) return;
    this.started = true;
    this.cy.batch(() => {
      this.edges.addClass('flow');
      this.nodes.addClass('flow');
      this.cy.elements().not(this.edges).not(this.nodes).addClass('dim');
    });
    this.frame();
    this.nodes.forEach((/** @type {any} */ n) => {
      for (const l of $$(`.tl[data-node="${CSS.escape(n.id())}"]`, this.tree)) l.classList.add('onpath');
    });
    if (reducedMotion()) { this.nodes.style('underlay-opacity', 0.22); return; }
    const t0 = performance.now();
    const maxDepth = Math.max(1, ...this.nodes.map((/** @type {any} */ n) => n.data('depth') || 0));
    /** @param {number} now */
    const tick = (now) => {
      if (this.cy.destroyed() || !this.cy.container()?.isConnected) { this.raf = 0; return; }
      const t = (now - t0) / 1000;
      this.cy.batch(() => {
        this.edges.style('line-dash-offset', -t * 22);
        // A glow wave that travels root → user, then repeats.
        const wave = (t * 1.6) % (maxDepth + 2.5);
        this.nodes.forEach((/** @type {any} */ n) => {
          const dist = Math.abs(wave - (n.data('depth') || 0));
          n.style('underlay-opacity', 0.05 + 0.32 * Math.max(0, 1 - dist));
        });
      });
      this.raf = requestAnimationFrame(tick);
    };
    this.raf = requestAnimationFrame(tick);
  }
  /** Zooms to the granting path so it stays the focus even in wide graphs. */
  frame() {
    if (this.userView) return;
    const eles = this.nodes.union(this.edges);
    this.cy.stop();
    this.cy.animate({ fit: { eles, padding: 60 }, duration: reducedMotion() ? 0 : 450, easing: 'ease-in-out-cubic',
      complete: () => { if (this.cy.zoom() > 1.4) { this.cy.zoom(1.4); this.cy.center(eles); } } });
  }
  stop() {
    cancelAnimationFrame(this.raf);
    this.raf = 0;
    this.started = false;
    if (this.cy.destroyed()) return;
    this.cy.batch(() => {
      this.edges.removeStyle('line-dash-offset').removeClass('flow');
      this.nodes.removeStyle('underlay-opacity').removeClass('flow');
      this.cy.elements().removeClass('dim');
    });
    $$('.tl.onpath', this.tree).forEach((l) => l.classList.remove('onpath'));
  }
}

/** Replays the walk's steps on the graph and the text trace. */
class Player {
  /** @param {any} cy @param {Step[]} steps @param {Element} tree @param {PathFlow} flow */
  constructor(cy, steps, tree, flow) {
    this.cy = cy;
    this.steps = steps || [];
    this.tree = tree;
    this.flow = flow;
    this.i = 0;
    /** @type {number | null} */
    this.timer = null;
    this.speed = 6;
    this.current = '';
    /** @type {Map<string, Element[]>} */
    /** @type {number[] | null} */
    this.stopEnds = null;
    // Opening a fold changes which steps look alike.
    cy.on('remove', 'node.fold', () => { this.stopEnds = null; });
    this.lines = new Map();
    for (const l of $$('.tl', tree)) {
      const k = l.dataset.node;
      if (!this.lines.has(k)) this.lines.set(k, []);
      this.lines.get(k)?.push(l);
    }
    // Edges by "source>target", so a step finds its edge without scanning them all.
    /** @type {Map<string, any>} */
    this.byEnds = new Map();
    cy.edges().forEach((/** @type {any} */ e) => {
      const k = `${e.data('source')}>${e.data('target')}`;
      this.byEnds.set(k, (this.byEnds.get(k) || cy.collection()).union(e));
    });
    /** The node lit as being evaluated, and its text trace lines marked current. */
    this.visiting = cy.collection();
    /** @type {Element[]} */
    this.cur = [];
    /** When the status line was last rewritten (it is a live region: not every tick). */
    this.said = 0;
  }
  reset() {
    this.pause();
    this.flow.stop();
    this.cy.elements().addClass('pending').removeClass('visiting');
    this.visiting = this.cy.collection();
    this.cur = [];
    this.tree.classList.add('animating');
    $$('.tl', this.tree).forEach((l) => l.classList.remove('seen', 'cur'));
    this.i = 0;
    this.update();
  }
  /** @param {string} node @param {string} [cls] */
  mark(node, cls) {
    const lines = this.lines.get(node) || [];
    if (cls === 'cur') { this.cur.forEach((l) => l.classList.remove('cur')); this.cur = lines; }
    for (const l of lines) { l.classList.add('seen'); if (cls) l.classList.add(cls); }
  }
  /** @param {Step} s */
  apply(s) {
    const n = this.shown(s.node);
    switch (s.phase) {
      case 'enter':
        this.visiting.removeClass('visiting');
        n.removeClass('pending').addClass('visiting');
        this.visiting = n;
        this.mark(s.node, 'cur');
        this.current = String(n.data('label') || '').replace(/\n[∪∩∖]$/, '').replace(/\n/g, '');
        break;
      case 'exit':
        n.removeClass('pending visiting');
        break;
      case 'link':
        this.byEnds.get(`${s.from}>${s.node}`)?.removeClass('pending');
        n.removeClass('pending');
        this.mark(s.node);
        break;
      case 'revisit':
        n.removeClass('pending').addClass('visiting');
        setTimeout(() => n.removeClass('visiting'), 300);
        break;
      default:
    }
  }
  /**
   * The element a step shows on: the node, or the fold hiding it (a walk into
   * a folded branch lights the fold rather than an invisible node).
   * @param {string} id
   */
  shown(id) {
    let n = this.cy.getElementById(id);
    while (n.nonempty() && n.style('display') === 'none') {
      const fold = this.cy.nodes('.fold').filter((/** @type {any} */ f) => (f.data('hidden') || []).includes(n.id()))[0];
      if (!fold) break;
      n = fold;
    }
    return n;
  }
  /**
   * Moves on one visible step: the recorded events (edges followed, nodes
   * left) up to and including entering the next node, applied together so no
   * step shows only an arrow. The replay's tick, and Step's.
   */
  advance() {
    const next = this.nextStop();
    if (next === undefined) { this.final(); return false; }
    while (this.i < next) this.apply(this.steps[this.i++]);
    return true;
  }
  /** Where the next visible step ends, or undefined at the end of the walk. */
  nextStop() { return this.stops().find((e) => e > this.i); }
  /**
   * The visible steps: the event index just after each node is entered,
   * skipping entries that light the same element as the one before (nodes
   * inside one folded branch all light the fold). Stepping, going back, and
   * the step count all use these, so every step changes what is shown.
   * Recomputed when a fold opens.
   * @returns {number[]}
   */
  stops() {
    if (this.stopEnds) return this.stopEnds;
    /** @type {number[]} */
    const out = [];
    let last = '';
    this.steps.forEach((s, k) => {
      if (s.phase !== 'enter') return;
      const id = this.shown(s.node).id() || s.node;
      if (id !== last) out.push(k + 1);
      last = id;
    });
    this.stopEnds = out;
    return out;
  }
  /** The number of visible steps shown so far. */
  shownSteps() {
    if (this.i >= this.steps.length) return this.stops().length;
    return this.stops().filter((e) => e <= this.i).length;
  }
  /**
   * One visible step, for the Step button and →, keeping the current node
   * in view. From the finished result, it starts the walk over.
   */
  step() {
    this.flow.userView = true;
    if (this.i >= this.steps.length || (this.i === 0 && !this.tree.classList.contains('animating'))) this.reset();
    if (!this.advance()) return false;
    this.follow();
    this.update();
    return true;
  }
  /** Keeps the current node (and its text trace line) in view while stepping. */
  follow() {
    const n = this.visiting[0];
    if (n && !n.removed()) {
      const e = this.cy.extent();
      const p = n.position();
      if (p.x < e.x1 || p.x > e.x2 || p.y < e.y1 || p.y > e.y2) this.cy.animate({ center: { eles: n }, duration: reducedMotion() ? 0 : 200 });
    }
    $('.tl.cur', this.tree)?.scrollIntoView({ block: 'nearest', behavior: scrollBehavior() });
  }
  /**
   * The replay takes a fixed total time (about 8s at the default speed),
   * however many steps: a long walk moves several steps a frame (see play).
   */
  delay() {
    const total = 15000 - this.speed * 1100;
    return Math.min(600, total / Math.max(1, this.stops().length));
  }
  /**
   * Plays on animation frames: each frame applies the steps that are due, in
   * one batch, so a long walk (whose steps are due faster than frames) moves
   * several steps a frame instead of queuing timers it cannot keep up with.
   */
  play() {
    if (this.timer) { this.pause(); return; }
    if (this.i === 0 || this.i >= this.steps.length) this.reset();
    let due = performance.now() + 120;
    const tick = (/** @type {number} */ now) => {
      this.timer = null;
      if (now >= due) {
        // The last step stays on screen for its turn, like the others; the
        // walk finishes when the next step would be due.
        if (this.nextStop() === undefined) { this.final(); return; }
        this.cy.startBatch();
        try {
          while (now >= due && this.nextStop() !== undefined) { this.advance(); due += this.delay(); }
        } finally {
          this.cy.endBatch();
        }
      }
      this.timer = requestAnimationFrame(tick);
      this.update();
    };
    this.timer = requestAnimationFrame(tick);
    this.update();
  }
  pause() { if (this.timer) cancelAnimationFrame(this.timer); this.timer = null; this.update(); }
  /** Goes back one visible step (←), replaying silently up to it. */
  back() {
    this.flow.userView = true;
    // The last step that ends before where the replay is (from the finished
    // result, that is the last step).
    const target = this.stops().filter((e) => e < this.i).pop() ?? 0;
    this.reset();
    for (let k = 0; k < target; k++) this.apply(this.steps[k]);
    this.i = target;
    this.follow();
    this.update();
  }
  final() {
    this.pause();
    this.cy.elements().removeClass('pending visiting');
    this.tree.classList.remove('animating');
    $$('.tl.cur', this.tree).forEach((l) => l.classList.remove('cur'));
    this.visiting = this.cy.collection();
    this.cur = [];
    this.i = this.steps.length;
    if (this.flow.focus) this.flow.start();
    this.update();
  }
  update() {
    const done = this.i >= this.steps.length;
    const playing = !!this.timer;
    const n = this.stops().length;
    const i = this.shownSteps();
    // Called every frame while playing: write only what changed.
    setText($('.player .step-count'), `step ${i} / ${n}`);
    setText($('.player [data-act="play"]'), playing ? '❚❚ Pause' : (this.i > 0 && !done ? '▶ Resume' : '▶ Replay walk'));
    const status = $('.player-status');
    if (!status) return;
    if (playing) {
      // A live region: rewrite it about once a second, not on every frame.
      const now = performance.now();
      if (now - this.said < 1000) return;
      this.said = now;
      status.textContent = `Replaying the walk: step ${i} of ${n}${this.current ? ` — evaluating ${this.current}` : ''}`;
    } else if (!done && this.i > 0) status.textContent = `Paused at step ${i} of ${n}. “Step ›” advances one step; “Resume” continues; “Skip to end” shows the result.`;
    else if (done) status.textContent = this.doneText();
  }
  /** The server phrases the closing status; this picks the variant for the highlight state. */
  doneText() {
    const d = $('.verdict')?.dataset || {};
    if (this.flow.active && this.flow.started) return d.donePath || '';
    if (this.flow.active) return d.doneNofocus || '';
    return d.doneOther || '';
  }
}

/** Sets an element's text if it differs. @param {Element | null} el @param {string} text */
function setText(el, text) {
  if (el && el.textContent !== text) el.textContent = text;
}

/**
 * Highlights (or clears) the tuples on the granting paths: the granting
 * tuples (edges the server marked "grant", which reach the user) in bold, the
 * other tuples on the paths labeled; marks their text trace lines; lists
 * them in the details; and brings them into view.
 * @param {any} cy @param {Element} tree @param {HTMLButtonElement} btn @param {PathFlow} flow
 */
function toggleGrants(cy, tree, btn, flow) {
  const on = btn.getAttribute('aria-pressed') !== 'true';
  btn.setAttribute('aria-pressed', String(on));
  const grants = cy.edges('.grant');
  const others = cy.edges('.flowpath').filter((/** @type {any} */ e) => e.data('tuple') && !e.hasClass('grant'));
  cy.batch(() => { grants.toggleClass('grant-on', on); others.toggleClass('tuple-on', on); });
  const lineIds = (/** @type {any} */ edges) => new Set(edges.map((/** @type {any} */ e) => e.target().id()));
  const g = lineIds(grants);
  const o = lineIds(others);
  for (const l of $$('.tl', tree)) {
    l.classList.toggle('grant-line', on && g.has(l.dataset.node));
    l.classList.toggle('tuple-line', on && !g.has(l.dataset.node) && o.has(l.dataset.node));
  }
  const box = $('#node-detail');
  if (!on) { if (box && $('.grant-list', box)) clearDetail(); return; }
  $('.tl.grant-line', tree)?.scrollIntoView({ block: 'nearest', behavior: scrollBehavior() });
  // Bring the granting tuples into view (the list finds the others).
  const shown = grants.union(grants.connectedNodes());
  flow.userView = true;
  cy.stop();
  cy.animate({ fit: { eles: shown, padding: 80 }, duration: reducedMotion() ? 0 : 300,
    complete: () => { if (cy.zoom() > 1.2) { cy.zoom(1.2); cy.center(shown); } } });
  if (!box) return;
  /** @param {any} edges @returns {string} */
  const list = (edges) => {
    /** @type {Map<string, string>} tuple → edge id */
    const m = new Map();
    edges.forEach((/** @type {any} */ e) => { if (!m.has(e.data('tuple'))) m.set(e.data('tuple'), e.id()); });
    return `<ul class="grant-list">${[...m].map(([t, id]) => `<li><button type="button" class="linkish" data-edge="${esc(id)}"><code>${esc(t)}</code></button></li>`).join('')}</ul>`;
  };
  const n = new Set(grants.map((/** @type {any} */ e) => e.data('tuple'))).size;
  box.innerHTML = `<button type="button" class="close" aria-label="Clear the selection">×</button>
    <h3 id="node-detail-title">${n} granting tuple${n === 1 ? '' : 's'}</h3>
    <p class="muted sm">The tuples that reach the user at the end of a granting path. Select one to find it in the graph.</p>
    ${list(grants)}
    ${others.length ? `<h4 class="sm">Also on the granting paths</h4>
    <p class="muted sm">Tuples the paths go through on the way (parents, group memberships, …).</p>${list(others)}` : ''}`;
  togglePanel(false);
}

/** Shows the server-computed detail for a node. @param {any} node */
function showDetail(node) {
  const box = $('#node-detail');
  const d = node.data('detail');
  if (!box || !d) return;
  /** @param {any} l */
  const link = (l) => (l.act
    ? `<button type="button" class="btn sm" data-act="${esc(l.act)}" data-order="${esc(l.order)}">${esc(l.text)}</button>`
    : l.href ? `<a class="btn sm${l.ghost ? ' ghost' : ''}" href="${esc(l.href)}">${esc(l.text)}</a>` : `<span class="muted">${esc(l.text)}</span>`);
  const rows = (d.rows || []).map((/** @type {any} */ r) => `<dt>${esc(r.key)}</dt><dd>${r.items
    ? `<ul class="checks">${r.items.map((/** @type {any} */ i) => `<li>${i.href ? `<a href="${esc(i.href)}">${esc(i.text)}</a>` : esc(i.text)}${i.note ? ` <span class="muted">${esc(i.note)}</span>` : ''}</li>`).join('')}</ul>`
    : esc(r.value)}</dd>`).join('');
  box.innerHTML = `<button type="button" class="close" aria-label="Clear the selection">×</button>
    <h3 id="node-detail-title">${esc(d.title)}</h3><dl>${rows}</dl><div class="links">${(d.links || []).map(link).join(' ')}</div>`;
  // Selecting a node is a request to see its details: open the panel.
  togglePanel(false);
}

const EMPTY_DETAIL = '<p class="nd-empty muted sm">Select a node in the graph or a line of the text trace to see what it means and where to go next.</p>';

/** Clears the selection and its details. */
function clearDetail() {
  const box = $('#node-detail');
  if (box && !$('.nd-empty', box)) box.innerHTML = EMPTY_DETAIL;
  view?.cy.elements().unselect();
  $$('.tl.sel').forEach((l) => l.classList.remove('sel'));
}

/** @param {HTMLButtonElement} btn */
function copyMarkdown(btn) {
  const v = $('.verdict');
  const facts = $$('.facts li', v).map((li) => `${li.dataset.kind}: ${$('.ft', li).textContent.trim()}`);
  const md = [
    `**${$('.big', v).textContent.trim()}**: ${$('.headline', v)?.textContent.trim() || ''}`,
    ...(facts.length ? ['', 'Why:', ...facts.map((f, i) => `${i + 1}. ${f}`)] : []),
    '', '```text', $('#trace-plain')?.textContent.trim() || '', '```',
    '', `Permalink: ${new URL($('.permalink', v).getAttribute('href'), location.href)}`,
  ].join('\n');
  navigator.clipboard?.writeText(md).then(() => {
    const old = btn.textContent;
    btn.textContent = 'Copied';
    setTimeout(() => { btn.textContent = old; }, 1200);
  });
}

/** @param {any} cy */
function savePNG(cy) {
  const blob = cy.png({ output: 'blob', full: true, scale: 2, bg: color('--panel') });
  const a = document.createElement('a');
  a.href = URL.createObjectURL(blob);
  a.download = `trace-${($('.verdict')?.dataset.user || 'user').replace(/[^\w.-]+/g, '_')}.png`;
  a.click();
  setTimeout(() => URL.revokeObjectURL(a.href), 2000);
}

/** @type {{ cy: any, player: Player } | null} */
let view = null;

const fitTrace = () => { if (view) { view.player.flow.userView = true; fitCapped(view.cy, undefined, 24, 1.3); } };

/**
 * Collapses or opens the side panel (details, text trace, who else).
 * @param {boolean} [collapse] collapse (true) or open (false); toggles when omitted
 */
function togglePanel(collapse) {
  const tv = $('.trace-views');
  if (!tv) return;
  const was = tv.classList.contains('panel-collapsed');
  const now = collapse ?? !was;
  tv.classList.toggle('panel-collapsed', now);
  for (const b of $$('[data-act="toggle-panel"]')) b.setAttribute('aria-expanded', String(!now));
  store.set('panelCollapsed', now);
  if (view && now !== was) resizeGraph(view);
}

/**
 * Resizes the graph to its box. A view the person set keeps its zoom and the
 * point at its center; otherwise the graph is framed (or fitted) again.
 * @param {{ cy: any, player: Player }} v
 */
function resizeGraph(v) {
  const { cy } = v;
  const { flow } = v.player;
  if (!flow.userView) {
    cy.resize();
    if (flow.started) flow.frame(); else fitCapped(cy, undefined, 24, 1.3);
    return;
  }
  const e = cy.extent();
  const mid = { x: (e.x1 + e.x2) / 2, y: (e.y1 + e.y2) / 2 };
  cy.resize();
  const z = cy.zoom();
  cy.pan({ x: cy.width() / 2 - mid.x * z, y: cy.height() / 2 - mid.y * z });
}

/** Sizes the check view to reach the bottom of the window on wide screens (CSS applies it). */
/**
 * Where the trace views' sizes (--trace-h, --text-w) are set: the result
 * container, which a re-run keeps while swapping its content, so the new views
 * take the old sizes in their first frame instead of jumping until this script
 * runs. The views themselves when there is no such container.
 * @param {HTMLElement} tv
 */
const sizeHolder = (tv) => /** @type {HTMLElement} */ (tv.closest('#check-result') || tv);

function sizeTraceViews() {
  const tv = /** @type {HTMLElement | null} */ ($('.trace-views'));
  if (!tv) return;
  const top = tv.getBoundingClientRect().top + window.scrollY;
  const h = `${Math.max(620, Math.floor(window.innerHeight - top - 16))}px`;
  const holder = sizeHolder(tv);
  if (holder.style.getPropertyValue('--trace-h') === h) return false;
  holder.style.setProperty('--trace-h', h);
  return true;
}

// Resize when the window changes, or when what sits above the graph does
// (the form collapsing to its summary, “Why” opening).
let resizeTimer = 0;
const relayout = () => {
  clearTimeout(resizeTimer);
  resizeTimer = window.setTimeout(() => {
    sizeTraceViews();
    if (!view || view.cy.destroyed()) return;
    const box = view.cy.container();
    if (box && (view.cy.width() !== box.clientWidth || view.cy.height() !== box.clientHeight)) resizeGraph(view);
  }, 120);
};
window.addEventListener('resize', relayout);
// Also watch the graph's own box, which the layout settles after drawing.
const observer = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(relayout);
/** @param {Element} container */
function observeLayout(container) {
  observer?.disconnect();
  observer?.observe(document.body);
  observer?.observe(container);
}



/** Selects a node from the text trace or the graph. @param {any} cy @param {any} n @param {{ zoom?: boolean }} [opts] */
function selectNode(cy, n, { zoom = false } = {}) {
  if (view?.cy === cy) view.player.flow.userView = true;
  if (n.style('display') === 'none' && reveal(cy, n.id())) traceLayout(cy);
  cy.elements().unselect();
  n.select();
  cy.animate({ center: { eles: n }, ...(zoom ? { zoom: Math.max(cy.zoom(), 0.9) } : {}), duration: reducedMotion() ? 0 : 220 });
  showDetail(n);
}

/** Text trace as a tree: ↑/↓/Home/End move, Enter shows details. @param {Element} tree @param {any} cy */
function initTreeKeys(tree, cy) {
  const lines = $$('.tl', tree);
  if (!lines.length) return;
  lines[0].tabIndex = 0;
  tree.addEventListener('keydown', (/** @type {any} */ e) => {
    const cur = e.target.closest('.tl');
    if (!cur) return;
    const i = lines.indexOf(cur);
    /** @type {Record<string, number>} */
    const moves = { ArrowDown: Math.min(lines.length - 1, i + 1), ArrowUp: Math.max(0, i - 1), Home: 0, End: lines.length - 1 };
    const next = lines[moves[e.key] ?? -1];
    if (next) {
      e.preventDefault();
      cur.tabIndex = -1; next.tabIndex = 0; next.focus();
      cy.elements('.hl').removeClass('hl');
      cy.getElementById(next.dataset.node).addClass('hl');
    }
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      const n = cy.getElementById(cur.dataset.node);
      if (n.length) selectNode(cy, n);
    }
  });
}

// ---- keeping the view (zoom, pan, opened folds, selection) ----
// Leaving the Check page (for Model, say) and coming back re-runs the check;
// the view the person had is kept per tab and put back if the result is the same.

/** @typedef {{ sig: string, zoom: number, pan: { x: number, y: number }, open: string[], sel: string }} SavedView */

const viewKey = () => stateKey('checkView');

/** Identifies a result: the same check with the same graph. @param {string} graph */
const viewSig = (graph) => `${$('#check-result .verdict')?.dataset.summary || ''}|${graph.length}`;

/** @param {any} cy @param {Fold[]} folds @param {string} sig */
function keepView(cy, folds, sig) {
  let timer = 0;
  const save = () => {
    clearTimeout(timer);
    timer = window.setTimeout(() => {
      if (cy.destroyed()) return;
      /** @type {SavedView} */
      const v = { sig, zoom: cy.zoom(), pan: cy.pan(), open: folds.map((f) => f.id).filter((id) => cy.getElementById(id).empty()),
        sel: cy.$('node:selected').id() || '' };
      session.set(viewKey(), v);
    }, 150);
  };
  cy.on('viewport select unselect remove', save);
}

/**
 * Puts a kept view back: opens the same folds (before layout), then restores
 * zoom, pan and selection (after the final frame, replacing its fit).
 * @param {any} cy @param {SavedView} v @param {'folds' | 'viewport'} phase
 */
function restoreView(cy, v, phase) {
  if (phase === 'folds') {
    for (const id of v.open) { const f = cy.getElementById(id); if (f.nonempty()) expandFold(cy, f); }
    return;
  }
  cy.stop();
  cy.viewport({ zoom: v.zoom, pan: v.pan });
  const n = v.sel ? cy.getElementById(v.sel) : null;
  if (n?.nonempty() && n.visible()) {
    n.select();
    $$(`.tl[data-node="${CSS.escape(v.sel)}"]`).forEach((l) => l.classList.add('sel'));
    if (!$('.trace-views')?.classList.contains('panel-collapsed')) showDetail(n);
  }
}

/**
 * <fga-trace>: a check's result as the server renders it (the verdict, the
 * evaluation graph with its replay controls, the side panel, and the graph
 * data in #trace-data). It draws itself when it arrives, once htmx has put
 * it in place, and stops its replay when it is removed.
 *
 * Before drawing, it sends "fgax:trace" (detail: { restore: false }). The page
 * sets detail.restore when the result is a check being restored (coming back
 * to the page), so the kept view comes back instead of a replay.
 */
class FgaTrace extends HTMLElement {
  connectedCallback() {
    if (this.started) return;
    this.started = true;
    whenLoaded(this, () => {
      if (!this.isConnected) return;
      const detail = { restore: false };
      this.dispatchEvent(new CustomEvent('fgax:trace', { bubbles: true, detail }));
      renderTrace({ replay: !detail.restore, restore: detail.restore });
    });
  }

  disconnectedCallback() {
    view?.player.pause();
    view?.player.flow.stop();
    view = null;
  }
}

/**
 * Draws the trace on the page.
 * @param {{ replay?: boolean, restore?: boolean }} [opts] replay: animate the walk if the viewer allows it;
 *   restore: put back the view kept for this result (coming back to the page)
 */
function renderTrace({ replay = true, restore = false } = {}) {
  const dataEl = $('#trace-data');
  const container = $('#trace-canvas');
  if (!dataEl || !container) return;
  /** @type {GraphData} */
  const data = JSON.parse(dataEl.textContent);
  const sig = viewSig(dataEl.textContent);
  /** @type {SavedView | null} */
  const saved = restore ? session.get(viewKey()) : null;
  const kept = saved?.sig === sig ? saved : null;
  view?.player.flow.stop();
  sizeTraceViews();
  observeLayout(container);
  const cy = cytoscape({ container, elements: data.elements, style: traceStyle(pal()), layout: { name: 'null' }, ...baseOpts });
  applyFolds(cy, data.folds || []);
  if (kept) restoreView(cy, kept, 'folds');
  traceLayout(cy);
  fitCapped(cy, undefined, 24, 1.3);
  // Semantic zoom: short, larger labels when zoomed out.
  const farZoom = () => cy.elements().toggleClass('far', cy.zoom() < 0.55);
  cy.on('zoom', farZoom);
  farZoom();
  const tree = $('#rpanel-trace .tree');
  const flow = new PathFlow(cy, tree);
  cy.on('scrollzoom pinchzoom dragpan', () => { flow.userView = true; });
  const player = new Player(cy, data.steps, tree, flow);
  view = { cy, player };

  const focus = $('.player [data-act="focus"]');
  if (focus) {
    focus.checked = store.get('focusPath', true);
    flow.focus = focus.checked;
    focus.closest('label').classList.toggle('hidden', !flow.active);
    focus.addEventListener('change', () => {
      store.set('focusPath', focus.checked);
      flow.focus = focus.checked;
      if (player.i >= player.steps.length) {
        if (focus.checked) flow.start(); else { flow.stop(); fitTrace(); }
      }
      player.update();
    });
  }
  const speed = $('.player [data-act="speed"]');
  const auto = $('.player [data-act="autoplay"]');
  // Replaying is motion: off by default when the viewer asks for less.
  auto.checked = store.get('autoplay', !reducedMotion());
  speed.value = store.get('speed', 6);
  player.speed = +speed.value;
  if (replay && auto.checked) player.play(); else player.final();
  speed.addEventListener('input', () => { player.speed = +speed.value; store.set('speed', player.speed); });
  auto.addEventListener('change', () => store.set('autoplay', auto.checked));

  $('.player').addEventListener('click', (/** @type {any} */ e) => {
    switch (e.target.closest('[data-act]')?.dataset.act) {
      case 'play': player.play(); break;
      case 'step': player.pause(); player.step(); break;
      case 'final': player.final(); break;
      case 'fit': fitTrace(); break;
      case 'copy-md': copyMarkdown(e.target.closest('button')); break;
      case 'png': savePNG(cy); break;
      case 'toggle-panel': togglePanel(); break;
      default:
    }
  });

  cy.on('tap', 'node.fold', (/** @type {any} */ e) => { expandFold(cy, e.target); traceLayout(cy); });
  cy.on('tap', 'node', (/** @type {any} */ e) => {
    if (e.target.hasClass('fold')) return;
    showDetail(e.target);
    $$('.tl.sel', tree).forEach((l) => l.classList.remove('sel'));
    const lines = $$(`.tl[data-node="${CSS.escape(e.target.id())}"]`, tree);
    lines.forEach((l) => l.classList.add('sel'));
    lines[0]?.scrollIntoView({ block: 'nearest', behavior: scrollBehavior() });
  });
  cy.on('tap', (/** @type {any} */ e) => { if (e.target === cy) clearDetail(); });
  tree.addEventListener('mouseover', (/** @type {any} */ e) => {
    const l = e.target.closest('.tl');
    cy.elements('.hl').removeClass('hl');
    if (l) cy.getElementById(l.dataset.node).addClass('hl');
  });
  tree.addEventListener('mouseleave', () => cy.elements('.hl').removeClass('hl'));
  tree.addEventListener('click', (/** @type {any} */ e) => {
    const l = e.target.closest('.tl');
    const n = l && cy.getElementById(l.dataset.node);
    if (n?.length) selectNode(cy, n, { zoom: true });
  });
  $('#node-detail')?.addEventListener('click', (/** @type {any} */ e) => {
    if (e.target.closest('.close')) clearDetail();
    // A granting tuple from the list: find it in the graph.
    const item = e.target.closest('[data-edge]');
    const edge = item && cy.getElementById(item.dataset.edge);
    if (edge?.nonempty()) {
      flow.userView = true;
      cy.elements('.hl').removeClass('hl');
      edge.addClass('hl');
      cy.animate({ center: { eles: edge }, zoom: Math.max(cy.zoom(), 0.9), duration: reducedMotion() ? 0 : 220 });
    }
  });
  const grantsBtn = /** @type {HTMLButtonElement | null} */ ($('.verdict [data-act="show-grants"]'));
  grantsBtn?.addEventListener('click', () => toggleGrants(cy, tree, grantsBtn, flow));
  $('.inspector')?.addEventListener('click', (/** @type {any} */ e) => {
    if (e.target.closest('.insp-head [data-act="toggle-panel"]')) togglePanel();
  });
  $('.panel-rail')?.addEventListener('click', () => togglePanel(false));
  initTreeKeys(tree, cy);
  // On phones the panel is a bottom sheet: closed until asked for (or a node is selected).
  togglePanel(store.get('panelCollapsed', matchMedia('(max-width: 700px)').matches));
  const setTextW = (/** @type {number} */ w) => { const tv = $('.trace-views'); if (tv) sizeHolder(tv).style.setProperty('--text-w', `${Math.round(Math.max(w, 260))}px`); return w; };
  initResizer($('.trace-views .splitter'), (x) => {
    const r = $('.trace-views').getBoundingClientRect();
    return setTextW(Math.min(r.right - x, r.width * 0.7));
  }, setTextW, 'textWidth', () => cy.resize());
  // Last, so nothing above (panel width, panel state) refits the restored view.
  if (kept) { restoreView(cy, kept, 'viewport'); flow.userView = true; }
  keepView(cy, data.folds || [], sig);
}

/** Defines <fga-trace> and registers the trace's keyboard shortcuts (they act on the trace shown). */
export function initTrace() {
  if (!customElements.get('fga-trace')) customElements.define('fga-trace', FgaTrace);
  const player = () => ($('#trace-canvas') && view ? view.player : null);
  onKey('play', () => { const p = player(); if (!p) return false; p.play(); return true; });
  onKey('step', () => { const p = player(); if (!p) return false; p.pause(); p.step(); return true; });
  onKey('back', () => { const p = player(); if (!p) return false; p.pause(); p.back(); return true; });
  // Only on the check page: Explore registers its own panel and fit keys.
  if ($('#check-form')) {
    onKey('toggleText', () => { if (!$('.trace-views')) return false; togglePanel(); return true; });
    onKey('fit', () => { fitTrace(); });
  }
  onEscape(clearDetail);
}

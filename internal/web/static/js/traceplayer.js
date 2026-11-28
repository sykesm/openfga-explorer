// @ts-check
// The check trace's replay: stepping through the walk on the graph and the
// text trace (Player), and the flowing highlight of the granting path that
// follows it (PathFlow).
import { $, $$, reducedMotion, scrollBehavior } from './core.js';

/** @typedef {import('./tracegraph.js').Step} Step */

/**
 * PathFlow keeps the focus on how access was granted: once the replay ends
 * with an allowed decision, the granting path (elements the server marked
 * "flowpath") gets dashes flowing in the direction of the arrows and a glow
 * travelling from the object down to the user. The rest is only dimmed.
 */
export class PathFlow {
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
export class Player {
  /**
   * @param {any} cy @param {Step[]} steps @param {Element} tree @param {PathFlow} flow
   * @param {Element} root the trace, whose player bar and verdict it updates
   */
  constructor(cy, steps, tree, flow, root) {
    this.cy = cy;
    this.steps = steps || [];
    this.tree = tree;
    this.flow = flow;
    this.root = root;
    this.i = 0;
    /** @type {number | null} */
    this.timer = null;
    this.speed = 6;
    this.current = '';
    /** @type {number[] | null} */
    this.stopEnds = null;
    // Opening a fold changes which steps look alike.
    cy.on('remove', 'node.fold', () => { this.stopEnds = null; });
    /** @type {Map<string, Element[]>} */
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
    setText($('.player .step-count', this.root), `step ${i} / ${n}`);
    setText($('.player [data-act="play"]', this.root), playing ? '❚❚ Pause' : (this.i > 0 && !done ? '▶ Resume' : '▶ Replay walk'));
    const status = $('.player-status', this.root);
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
    const d = $('.verdict', this.root)?.dataset || {};
    if (this.flow.active && this.flow.started) return d.donePath || '';
    if (this.flow.active) return d.doneNofocus || '';
    return d.doneOther || '';
  }
}

/** Sets an element's text if it differs. @param {Element | null} el @param {string} text */
function setText(el, text) {
  if (el && el.textContent !== text) el.textContent = text;
}

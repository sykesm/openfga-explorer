// @ts-check
// Shared helpers: DOM queries, escaping, storage, motion preference, colors,
// and Cytoscape defaults. No feature logic lives here.

/** The store/model URL prefix of the current page, e.g. "/s/<store>/m/latest". */
export const base = () => document.body.dataset.base || '';

/**
 * The first element matching sel (in root, or the page), or null. Typed as
 * an HTMLElement, which the page's elements are; cast to a narrower type
 * (HTMLInputElement, …) where its properties are needed.
 * @param {string} sel
 * @param {ParentNode} [root]
 * @returns {HTMLElement | null}
 */
export const $ = (sel, root = document) => /** @type {HTMLElement | null} */ (root.querySelector(sel));

/**
 * Every element matching sel (in root, or the page).
 * @param {string} sel
 * @param {ParentNode} [root]
 * @returns {HTMLElement[]}
 */
export const $$ = (sel, root = document) => /** @type {HTMLElement[]} */ (Array.from(root.querySelectorAll(sel)));

/** @param {unknown} s */
export const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c] || c);

/** Per-tab state, so switching views (Check → Model → Check) keeps your work. */
export const session = {
  /** @param {string} k */
  get(k) { try { const v = sessionStorage.getItem('fgax.' + k); return v === null ? null : JSON.parse(v); } catch { return null; } },
  /** @param {string} k @param {unknown} v */
  set(k, v) { try { sessionStorage.setItem('fgax.' + k, JSON.stringify(v)); } catch { /* private mode or quota */ } },
  /** @param {string} k */
  del(k) { try { sessionStorage.removeItem('fgax.' + k); } catch { /* ignore */ } },
};

/** Per-browser preferences (speed, panel widths, recents). */
export const store = {
  /** @param {string} k @param {any} d */
  get(k, d) { try { const v = localStorage.getItem('fgax.' + k); return v === null ? d : JSON.parse(v); } catch { return d; } },
  /** @param {string} k @param {unknown} v */
  set(k, v) { try { localStorage.setItem('fgax.' + k, JSON.stringify(v)); } catch { /* private mode */ } },
};

/** @param {string} view */
export const stateKey = (view) => `${view}:${base()}`;

// ---- recently used objects ----
// Kept in this browser (the server keeps nothing about a person's work) and
// sent with each ID search, which lists the type's recent ones first.
const RECENT_MAX = 30;
const recentKey = () => `recentObjects:${base()}`;

/** The objects used recently in this store, most recent first. @returns {string[]} */
export const recentObjects = () => store.get(recentKey(), []);

/**
 * Records an object ("type:id"; a userset's "#relation" is dropped) as the
 * most recent. Record only objects known to be in tuples, so typos never
 * come back as suggestions.
 * @param {string} obj
 */
export function rememberObject(obj) {
  const colon = obj.indexOf(':');
  if (colon <= 0) return;
  const hash = obj.indexOf('#', colon);
  const o = hash > 0 ? obj.slice(0, hash) : obj;
  if (o.endsWith(':*')) return;
  store.set(recentKey(), [o, ...recentObjects().filter((x) => x !== o)].slice(0, RECENT_MAX));
}

export const reducedMotion = () => matchMedia('(prefers-reduced-motion: reduce)').matches;
/** @returns {ScrollBehavior} */
export const scrollBehavior = () => (reducedMotion() ? 'auto' : 'smooth');

/**
 * "a" or "an" for a type name. Words starting with a "you" sound (user,
 * unit, usage) take "a", other vowels "an".
 * @param {string} w
 */
export const article = (w) => (/^(?:[aeio]|u(?!ni|s[aeiou]|r[aeiou]|t[aeiou]))/i.test(w) ? 'an' : 'a');

// ---- colors ----
/** @param {string} name a CSS custom property, e.g. "--fg" */
export function color(name) {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
}

/** The palette tokens Cytoscape styles need, read from CSS. */
export function pal() {
  return {
    fg: color('--fg'), muted: color('--muted'), bg: color('--bg'), panel: color('--panel'), line: color('--line'),
    accent: color('--accent'), allowed: color('--allowed'), denied: color('--denied'), unknown: color('--unknown'),
    error: color('--error'), skipped: color('--skipped'), mismatch: color('--mismatch'), probe: color('--probe'),
    ttu: color('--ttu'),
  };
}

// Types get colors from a fixed categorical palette, in model order, that
// avoids the green/vermillion used for allowed/denied.
const TYPE_COLORS = {
  light: ['#3b6ea5', '#a1662f', '#7b4fa3', '#2f7f86', '#9a7d0a', '#6b6f75', '#b04e8a', '#4f5bd5'],
  dark: ['#7aa7d9', '#e0a466', '#b393dd', '#6cc3c9', '#e3c45a', '#a8adb4', '#e48cc0', '#949cf2'],
};
/** The i-th categorical color for the current theme. @param {number} i */
export function categoricalColor(i) {
  const p = TYPE_COLORS[matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'];
  return p[((i % p.length) + p.length) % p.length];
}

/** @type {string[] | null} */
let typeOrder = null;
/** @param {string} t */
export function typeColor(t) {
  typeOrder ??= (document.body.dataset.types || '').split(',').filter(Boolean);
  const dark = matchMedia('(prefers-color-scheme: dark)').matches;
  const p = TYPE_COLORS[dark ? 'dark' : 'light'];
  let i = typeOrder.indexOf(t);
  if (i < 0) { i = 0; for (const c of String(t || '')) i = (i * 31 + c.charCodeAt(0)) >>> 0; }
  return p[i % p.length];
}

/** @type {CanvasRenderingContext2D | null} */
let mixCtx = null;
/** @param {string} c */
function rgbOf(c) {
  mixCtx ??= /** @type {CanvasRenderingContext2D} */ (document.createElement('canvas').getContext('2d'));
  mixCtx.fillStyle = '#000';
  mixCtx.fillStyle = c;
  const v = String(mixCtx.fillStyle);
  if (v.startsWith('#')) return [1, 3, 5].map((i) => parseInt(v.slice(i, i + 2), 16));
  return (v.match(/[\d.]+/g) || []).slice(0, 3).map(Number);
}
/**
 * mix blends color a into b by weight w (0..1) as rgb(), which Cytoscape
 * understands (it accepts neither color-mix() nor custom properties).
 * @param {string} a @param {string} b @param {number} w
 */
export function mix(a, b, w) {
  const x = rgbOf(a), y = rgbOf(b);
  return `rgb(${x.map((v, i) => Math.round(v * w + y[i] * (1 - w))).join(', ')})`;
}

// ---- Cytoscape defaults ----
/** @param {unknown} l @param {number} [px] */
export const labelWidth = (l, px = 11) => Math.max(40, Math.max(...String(l || '').split('\n').map((s) => s.length)) * px * 0.6 + 22);
/** @param {unknown} l @param {number} [px] */
export const labelHeight = (l, px = 11) => String(l || '').split('\n').length * (px + 4) + 14;

export const baseOpts = { wheelSensitivity: 0.25, minZoom: 0.08, maxZoom: 2.5, boxSelectionEnabled: false };

/**
 * Fit to the viewport without blowing small graphs up to giant zoom levels.
 * @param {any} cy @param {any} [eles] @param {number} [pad] @param {number} [cap]
 */
export function fitCapped(cy, eles, pad = 30, cap = 1.15) {
  if (!cy) return;
  cy.fit(eles, pad);
  if (cy.zoom() > cap) { cy.zoom(cap); cy.center(eles); }
}

/** @param {string} rankDir @param {object} [extra] */
export const dagre = (rankDir, extra = {}) => ({ name: 'dagre', rankDir, nodeSep: 24, rankSep: 56, edgeSep: 10, padding: 24, animate: false, ...extra });

// ---- loading content with htmx ----

/**
 * Runs fn once htmx has finished putting el in place, or at once if el is
 * not being swapped in by htmx. A custom element that starts work its new
 * content depends on calls this from connectedCallback: htmx inserts new
 * content (marked htmx-added) before it settles it (putting back the server's
 * class and style on elements with an id) and processes its hx-* attributes,
 * and fires htmx:load on the inserted element when it is done.
 * @param {Element} el
 * @param {() => void} fn
 */
export function whenLoaded(el, fn) {
  const added = el.closest('.htmx-added');
  if (added) added.addEventListener('htmx:load', fn, { once: true });
  else fn();
}

/**
 * Loads url into target (innerHTML) with htmx. Unlike htmx.ajax, which
 * resolves as soon as the content is swapped in, this resolves once the new
 * content has settled and htmx has processed it (so its hx-* attributes work),
 * or when the request fails.
 * @param {string} url
 * @param {Element} target
 * @returns {Promise<void>}
 */
export function load(url, target) {
  return new Promise((resolve) => {
    /** @param {Event} e */
    const settled = (e) => {
      if (e.target !== target) return; // a swap inside target, not this one
      stop();
      resolve();
    };
    /** @param {any} e htmx:afterRequest (on the body, for htmx.ajax) */
    const failed = (e) => { if (e.detail.target === target && !e.detail.successful) { stop(); resolve(); } };
    const stop = () => {
      target.removeEventListener('htmx:afterSettle', settled);
      document.removeEventListener('htmx:afterRequest', failed);
    };
    target.addEventListener('htmx:afterSettle', settled);
    document.addEventListener('htmx:afterRequest', failed);
    htmx.ajax('GET', url, { target, swap: 'innerHTML' });
  });
}

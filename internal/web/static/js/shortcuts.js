// Keyboard shortcuts and the shortcuts help dialog. Feature modules register
// what each key does on their page, so this module knows no feature state.
import { $ } from './core.js';
import { ModalDialog } from './ui.js';

/**
 * @typedef {'play' | 'step' | 'back' | 'fit' | 'toggleText' | 'edit' | 'find'} Action
 * @type {Partial<Record<Action, (e: KeyboardEvent) => boolean | void>>}
 */
const actions = {};
/** @type {(() => void)[]} */
const escapes = [];

/**
 * Registers the handler for a shortcut action on this page. A handler may
 * return false to let the key through (e.g. no player on the page).
 * @param {Action} name @param {(e: KeyboardEvent) => boolean | void} fn
 */
export function onKey(name, fn) { actions[name] = fn; }

/** Registers something to close on Escape (menus, popovers, details). @param {() => void} fn */
export function onEscape(fn) { escapes.push(fn); }

/** @type {Record<string, Action>} */
const KEYS = { ' ': 'play', ArrowRight: 'step', ArrowLeft: 'back', f: 'fit', F: 'fit', t: 'toggleText', T: 'toggleText', e: 'edit', E: 'edit', '/': 'find' };

export function initShortcuts() {
  const el = $('.help-overlay');
  const help = el ? new ModalDialog(el) : null;
  document.addEventListener('click', (e) => {
    if (e.target instanceof Element && e.target.closest('[data-act="help"]')) help?.open();
    if (e.target instanceof Element && e.target.closest('[data-act="help-close"]')) help?.close();
  });
  document.addEventListener('keydown', (e) => {
    const target = e.target instanceof Element ? e.target : document.body;
    if (e.key === 'Escape') { escapes.forEach((fn) => fn()); return; }
    if (help?.isOpen) return;
    if (target.closest('input, textarea, select, [contenteditable]') || e.metaKey || e.ctrlKey || e.altKey) return;
    if (e.key === '?') { help?.open(); return; }
    // Space and arrows belong to focused buttons, tabs, and the text trace.
    if ((e.key === ' ' && target.closest('button, [role="tab"], .tree')) || (e.key.startsWith('Arrow') && target.closest('.tree, [role="tab"], [role="separator"]'))) return;
    const name = KEYS[e.key];
    const fn = name && actions[name];
    if (fn && fn(e) !== false) e.preventDefault();
  });
}

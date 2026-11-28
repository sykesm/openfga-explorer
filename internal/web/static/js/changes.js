// @ts-check
// The Changes page: the browser's timezone for custom ranges, and showing
// the custom range fields only when chosen. Paging and live updates are
// plain htmx (the server renders each page and the poller).
import { $, $$ } from './core.js';

export function initChanges() {
  const form = $('#changes-form');
  if (!form) return;
  // Custom times are entered (and shown) in the browser's timezone.
  for (const tz of $$('.tz-offset')) tz.value = String(new Date().getTimezoneOffset());
  const sync = () => {
    const range = $('input[name="range"]:checked', form)?.value;
    const custom = $('.custom-range', form);
    if (custom) custom.hidden = range !== 'custom';
    for (const l of $$('.seg-radio label', form)) l.classList.toggle('on', !!$('input:checked', l));
  };
  form.addEventListener('change', (e) => {
    sync();
    // Presets apply at once; a custom range waits for its times and "Show".
    const t = /** @type {HTMLInputElement} */ (e.target);
    if (t.name === 'range' && t.value !== 'custom') htmx.trigger(form, 'submit');
  });
  sync();
}

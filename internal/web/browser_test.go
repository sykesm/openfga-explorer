package web

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/input"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/sykesm/openfga-explorer/internal/config"
	"github.com/sykesm/openfga-explorer/internal/fgatest"
)

// Browser smoke tests: the explorer in headless Chrome against an in-process
// OpenFGA. They cover the flows that have regressed before (pickers, edge
// selection mirroring the graph, check results, keyboard access, the type
// combobox) and fail on any JavaScript exception or CSP violation. They need
// Chrome or Chromium and are skipped with -short or when none is installed.

type browser struct {
	t    *testing.T
	x    *testExplorer
	ctx  context.Context
	mu   sync.Mutex
	errs []string
}

func chromePath() string {
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	if runtime.GOOS == "darwin" {
		p := "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
		if _, err := exec.LookPath(p); err == nil {
			return p
		}
	}
	return ""
}

func newBrowser(t *testing.T, x *testExplorer) *browser {
	t.Helper()
	if testing.Short() {
		t.Skip("browser tests skipped with -short")
	}
	path := chromePath()
	if path == "" {
		t.Skip("no Chrome or Chromium found")
	}
	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(path), chromedp.WindowSize(1440, 900))
	actx, cancelA := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancelC := chromedp.NewContext(actx, chromedp.WithErrorf(func(string, ...any) {}))
	ctx, cancelT := context.WithTimeout(ctx, 3*time.Minute)
	t.Cleanup(func() { cancelT(); cancelC(); cancelA() })
	b := &browser{t: t, x: x, ctx: ctx}
	chromedp.ListenTarget(ctx, func(ev any) {
		switch e := ev.(type) {
		case *cdpruntime.EventExceptionThrown:
			b.fail("JS exception: %s %s", e.ExceptionDetails.Text, e.ExceptionDetails.Exception.Description)
		case *cdpruntime.EventConsoleAPICalled:
			if e.Type == cdpruntime.APITypeError {
				var parts []string
				for _, a := range e.Args {
					parts = append(parts, string(a.Value)+a.Description)
				}
				b.fail("console.error: %s", strings.Join(parts, " "))
			}
		}
	})
	// Reduced motion: results show at once instead of replaying the walk.
	b.run(emulation.SetEmulatedMedia().WithFeatures([]*emulation.MediaFeature{{Name: "prefers-reduced-motion", Value: "reduce"}}))
	return b
}

func (b *browser) fail(format string, args ...any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.errs = append(b.errs, fmt.Sprintf(format, args...))
}

// checkClean fails the test on JS exceptions, console errors, or CSP violations.
func (b *browser) checkClean(where string) {
	b.t.Helper()
	var csp []string
	b.run(chromedp.Evaluate(`window.__csp || []`, &csp))
	b.mu.Lock()
	errs := append(b.errs, csp...)
	b.errs = nil
	b.mu.Unlock()
	for _, e := range errs {
		b.t.Errorf("%s: %s", where, e)
	}
}

func (b *browser) run(actions ...chromedp.Action) {
	b.t.Helper()
	if err := chromedp.Run(b.ctx, actions...); err != nil {
		b.t.Fatal(err)
	}
}

// open navigates to a path under the store and waits for the modules.
func (b *browser) open(path string) {
	b.t.Helper()
	b.run(
		chromedp.Navigate(b.x.url(path)),
		chromedp.WaitReady(`html[data-ready="true"]`, chromedp.ByQuery),
		// Count CSP violations from here on.
		chromedp.Evaluate(`window.__csp = []; document.addEventListener('securitypolicyviolation', (e) => window.__csp.push('CSP: ' + e.violatedDirective + ' ' + e.blockedURI + ' at ' + e.sourceFile + ':' + e.lineNumber + ':' + e.columnNumber + ' sample=' + e.sample + ' on ' + (e.target?.outerHTML || '').slice(0, 160)))`, nil),
	)
}

// eval returns the value of a JS expression.
func eval[T any](b *browser, js string) T {
	b.t.Helper()
	var v T
	b.run(chromedp.Evaluate(js, &v))
	return v
}

// waitFor polls a JS condition.
func (b *browser) waitFor(what, js string) {
	b.t.Helper()
	ctx, cancel := context.WithTimeout(b.ctx, 15*time.Second)
	defer cancel()
	if err := chromedp.Run(ctx, chromedp.Poll("!!("+js+")", nil, chromedp.WithPollingInterval(50*time.Millisecond))); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			b.t.Fatalf("timed out waiting for %s (%s)", what, js)
		}
		b.t.Fatal(err)
	}
}

// settled waits until htmx has no request in flight and nothing settling,
// so swapped-in content has its handlers.
func (b *browser) settled() {
	b.t.Helper()
	b.waitFor("htmx to settle", `!document.querySelector('.htmx-request, .htmx-settling, .htmx-swapping, .htmx-added')`)
}

func (b *browser) key(sel string, keys string) {
	b.t.Helper()
	b.run(chromedp.SendKeys(sel, keys, chromedp.ByQuery))
}

func TestBrowserExplore(t *testing.T) {
	x := newTestExplorer(t)
	b := newBrowser(t, x)
	b.open("~/explore")
	b.waitFor("type graph", `document.querySelector('#schema-canvas')._cyreg?.cy?.nodes().length > 0`)

	// Choosing a type loads its panel; the instance picker is one click away.
	b.run(chromedp.Click(`.type-chip[data-type="group"]`, chromedp.ByQuery))
	b.waitFor("type panel", `document.querySelector('#type-panel [data-selected-type="group"]')`)
	b.settled()
	b.run(chromedp.Click(`#type-panel .picker-q`, chromedp.ByQuery))

	// An exact ID opens the instance panel and adds its edges to the graph.
	b.key(`#type-panel .picker-q`, "platform")
	b.waitFor("search results", `document.querySelector('#type-panel .pick[data-value="group:platform"]')`)
	b.key(`#type-panel .picker-q`, kb.Enter)
	b.waitFor("instance panel", `document.querySelector('#instance-panel .instance[data-object="group:platform"]')`)
	b.waitFor("instance graph", `document.querySelector('#instance-canvas')._cyreg.cy.nodes().length > 1`)

	// Tabs follow the ARIA pattern: arrow keys move and select.
	b.run(chromedp.Focus(`#itab-edges`, chromedp.ByQuery))
	b.key(`#itab-edges`, kb.ArrowRight)
	b.waitFor("derived tab", `document.querySelector('#itab-derived').getAttribute('aria-selected') === 'true' && !document.querySelector('#ipanel-derived').hidden`)
	if got := eval[string](b, `document.activeElement.id`); got != "itab-derived" {
		t.Errorf("focus after ArrowRight on a tab = %q", got)
	}
	b.run(chromedp.Click(`#itab-edges`, chromedp.ByQuery))

	// Walking to a peer (document:doc-0007, which has inline edges).
	b.open("~/explore?type=document&id=doc-0007")
	b.waitFor("doc panel", `document.querySelector('#instance-panel .instance[data-object="document:doc-0007"]')`)
	b.waitFor("restored graph", `document.querySelector('#instance-canvas')._cyreg?.cy?.nodes().length > 1`)

	// Edge toggles mirror the graph: "−" removes the edge, "＋" adds it back.
	edges := `document.querySelector('#instance-canvas')._cyreg.cy.edges().length`
	before := eval[int](b, edges)
	b.waitFor("an edge on the graph", `document.querySelector('#instance-panel .add-edge.on')`)
	b.run(chromedp.Click(`#instance-panel .add-edge.on`, chromedp.ByQuery))
	b.waitFor("edge removed", fmt.Sprintf(`%s < %d`, edges, before))
	b.run(chromedp.Click(`#instance-panel .add-edge:not(.on)`, chromedp.ByQuery))
	b.waitFor("edge restored", fmt.Sprintf(`%s === %d`, edges, before))

	// The edge chooser keeps focus while filtering (a past regression).
	b.open("~/explore?type=group&id=platform")
	b.waitFor("group panel", `document.querySelector('#instance-panel .instance[data-object="group:platform"] [data-chooser-btn="out-member"]')`)
	b.settled()
	b.run(chromedp.Click(`[data-chooser-btn="out-member"]`, chromedp.ByQuery))
	b.waitFor("chooser", `document.querySelector('.chooser input[name="filter"]')`)
	b.key(`.chooser input[name="filter"]`, "plat-00")
	b.waitFor("filtered chooser", `[...document.querySelectorAll('.chooser [data-edge]')].length > 0 && [...document.querySelectorAll('.chooser [data-edge]')].every((li) => li.textContent.includes('plat-00'))`)
	if !eval[bool](b, `document.activeElement?.name === 'filter'`) {
		t.Error("chooser filter lost focus")
	}
	// "Add shown" puts every listed edge on the graph; the boxes follow.
	b.run(chromedp.Click(`.chooser .add-shown`, chromedp.ByQuery))
	b.waitFor("bulk add", `[...document.querySelectorAll('.chooser .edge-check')].every((c) => c.checked)`)
	added := eval[int](b, edges)
	b.run(chromedp.Click(`.chooser .edge-check`, chromedp.ByQuery))
	b.waitFor("unchecked edge leaves the graph", fmt.Sprintf(`%s === %d`, edges, added-1))
	b.checkClean("explore")
}

func TestBrowserCheck(t *testing.T) {
	x := newTestExplorer(t)
	b := newBrowser(t, x)
	b.open(`~/check?user=user:eng-002&relation=can_view&object=document:doc-0007&context=` +
		`%7B%22current_time%22%3A%222026-11-24T12%3A00%3A00Z%22%2C%22user_ip%22%3A%2210.1.2.3%22%7D`)
	b.waitFor("result", `document.querySelector('#check-result .verdict.v-allowed')`)
	b.waitFor("graph", `document.querySelector('#trace-canvas')?._cyreg?.cy?.nodes().length > 0`)
	if !strings.HasPrefix(eval[string](b, `document.querySelector('.player .step-count').textContent`), "step ") ||
		eval[bool](b, `document.querySelector('.trace-text .tree').classList.contains('animating')`) {
		t.Error("with reduced motion the result should not replay")
	}

	// The form collapses to the server's summary.
	b.waitFor("collapsed form", `document.querySelector('#check-form').classList.contains('collapsed')`)
	if got := eval[string](b, `document.querySelector('.qs-text').textContent`); got != "user:eng-002 → can_view → document:doc-0007 · with context" {
		t.Errorf("summary = %q", got)
	}
	// The granting path (from the server) is highlighted; failed branches are folded.
	if n := eval[int](b, `document.querySelector('#trace-canvas')._cyreg.cy.edges('.flowpath.flow').length`); n == 0 {
		t.Error("granting path not highlighted")
	}
	// The longest granting route is laid out as a straight vertical spine.
	if n := eval[int](b, `(() => { const cy = document.querySelector('#trace-canvas')._cyreg.cy;
		const x = cy.nodes('.root').position('x');
		return cy.nodes('.flowpath:visible').filter((n) => Math.abs(n.position('x') - x) < 1).length; })()`); n < 10 {
		t.Errorf("only %d granting-path nodes on the spine", n)
	}
	// Node details come from the server, including skipped branches' verdicts.
	detail := eval[string](b, `(() => { const cy = document.querySelector('#trace-canvas')._cyreg.cy;
		const n = cy.nodes('.skipped.hint-denied')[0]; if (!n) return 'no hinted node'; n.emit('tap');
		return document.querySelector('#node-detail').innerText; })()`)
	for _, want := range []string{"this branch does not hold", "does not hold", "Explore object"} {
		if !strings.Contains(detail, want) {
			t.Errorf("skipped node detail missing %q:\n%s", want, detail)
		}
	}
	if eval[string](b, `document.querySelector('.player-status').textContent`) == "" {
		t.Error("no closing status")
	}

	// "Show model order" re-runs in the other evaluation order.
	b.run(chromedp.Click(`.order-note [data-act="order"]`, chromedp.ByQuery))
	b.waitFor("model order", `document.querySelector('.order-note')?.textContent.includes('order the model lists')`)
	if !strings.Contains(eval[string](b, `location.search`), "order=model") {
		t.Error("URL does not record the order")
	}

	// The help dialog: focus moves in, is trapped, and returns on Escape.
	b.run(chromedp.Evaluate(`document.activeElement.blur()`, nil))
	b.run(chromedp.KeyEvent("?"))
	b.waitFor("help", `!document.querySelector('.help-overlay').hidden`)
	if !eval[bool](b, `document.activeElement?.dataset.act === 'help-close'`) {
		t.Error("help dialog did not take focus")
	}
	b.run(chromedp.KeyEvent(kb.Tab))
	if !eval[bool](b, `!!document.activeElement?.closest('.help-card')`) {
		t.Error("focus left the modal dialog")
	}
	b.run(chromedp.KeyEvent(kb.Escape))
	b.waitFor("help closed", `document.querySelector('.help-overlay').hidden`)

	// Keyboard: "e" reopens the form for editing.
	b.run(chromedp.Evaluate(`document.activeElement.blur()`, nil), chromedp.KeyEvent("e"))
	b.waitFor("form expanded", `!document.querySelector('#check-form').classList.contains('collapsed')`)
	b.checkClean("check")
}

func TestBrowserTypeCombobox(t *testing.T) {
	srv := fgatest.Start(t)
	var dsl strings.Builder
	dsl.WriteString("model\n  schema 1.1\ntype user\n")
	for i := range 20 {
		fmt.Fprintf(&dsl, "type kind_%02d\n  relations\n    define owner: [user]\n", i)
		if i%7 == 3 {
			dsl.WriteString("    define can_approve: [user]\n")
		}
	}
	st, err := srv.CreateStore(context.Background(), "many-types", dsl.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	x := explorerFor(t, st)
	b := newBrowser(t, x)
	b.open("~/check")
	b.waitFor("combobox", `document.querySelector('fga-picker[data-field="object"] .tcombo-q')`)
	obj := `fga-picker[data-field="object"] .tcombo-q`
	b.run(chromedp.Click(obj, chromedp.ByQuery))
	b.waitFor("list open", `document.querySelector('#tc-object:not([hidden]) li[data-value]')`)
	b.key(obj, "approve")
	b.waitFor("relation filter", `[...document.querySelectorAll('#tc-object li[data-value]')].map((l) => l.dataset.value).join() === 'kind_03,kind_10,kind_17'`)
	b.key(obj, kb.ArrowDown+kb.Enter)
	b.waitFor("chosen", `document.querySelector('select[name="object_type"]').value === 'kind_10'`)
	b.waitFor("relations refreshed", `[...document.querySelectorAll('#relation-select option')].some((o) => o.value === 'can_approve')`)
	if got := eval[string](b, `document.querySelector('`+obj+`').value`); got != "kind_10" {
		t.Errorf("combobox shows %q", got)
	}
	// Recents come first next time.
	b.run(chromedp.Click(obj, chromedp.ByQuery))
	b.waitFor("recents", `document.querySelector('#tc-object li.sec')?.textContent === 'recent' && document.querySelector('#tc-object li[data-value]').dataset.value === 'kind_10'`)
	b.key(obj, kb.Escape)
	b.waitFor("closed", `document.querySelector('#tc-object').hidden`)

	// A picker sets itself up wherever it is added, not only on the first page.
	b.run(chromedp.Evaluate(`(() => {
		const p = document.querySelector('fga-picker[data-field="object"]').cloneNode(true);
		p.dataset.field = 'later';
		p.querySelector('.tcombo-list').id = 'tc-later';
		p.querySelector('.tcombo-q').setAttribute('aria-controls', 'tc-later');
		document.querySelector('main').append(p);
	})()`, nil))
	later := `fga-picker[data-field="later"] .tcombo-q`
	b.run(chromedp.Click(later, chromedp.ByQuery))
	b.waitFor("added picker's list", `document.querySelector('#tc-later:not([hidden]) li[data-value]')`)
	b.key(later, kb.Escape)

	// Explore offers the type finder, matching relations too.
	b.open("~/explore")
	b.waitFor("finder", `document.querySelector('.type-q')`)
	b.key(`.type-q`, "approve")
	b.waitFor("results", `[...document.querySelectorAll('#type-results [role="option"]')].map((o) => o.dataset.value).join() === 'kind_03,kind_10,kind_17'`)
	if got := eval[string](b, `document.querySelector('.type-count').textContent`); got != "3 matching types" {
		t.Errorf("count = %q", got)
	}
	// 21 types is not a large model: the whole type graph is drawn.
	if eval[bool](b, `!!document.querySelector('#schema-empty')`) {
		t.Error("a 21-type model should not hide its type graph")
	}
	b.checkClean("combobox")
}

func TestBrowserUnreachableServer(t *testing.T) {
	x := newTestExplorer(t)
	b := newBrowser(t, x)
	b.open("~/explore")
	b.waitFor("type graph", `document.querySelector('#schema-canvas')._cyreg?.cy?.nodes().length > 0`)
	x.srv.CloseClientConnections()
	x.srv.Close()
	b.run(chromedp.Click(`.type-chip[data-type="document"]`, chromedp.ByQuery))
	b.waitFor("alert", `!document.querySelector('#page-alert').hidden`)
	if got := eval[string](b, `document.querySelector('#page-alert .pa-text').textContent`); !strings.Contains(got, "Could not reach") {
		t.Errorf("alert = %q", got)
	}
}

// graphContrastJS measures what the trace graph actually draws: each
// element's computed color, composited with its opacities onto the panel,
// as a WCAG contrast ratio. It returns the worst label and line ratios for
// skipped and dimmed (off-path) elements.
const graphContrastJS = `(() => {
  const cy = document.querySelector('#trace-canvas')._cyreg.cy;
  const ctx = document.createElement('canvas').getContext('2d');
  const rgb = (c) => { ctx.fillStyle = '#000'; ctx.fillStyle = c; const v = ctx.fillStyle;
    return v.startsWith('#') ? [1, 3, 5].map((i) => parseInt(v.slice(i, i + 2), 16)) : v.match(/[\d.]+/g).slice(0, 3).map(Number); };
  const lum = (c) => { const [r, g, b] = c.map((x) => { x /= 255; return x <= 0.03928 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4; });
    return 0.2126 * r + 0.7152 * g + 0.0722 * b; };
  const panel = rgb(getComputedStyle(document.documentElement).getPropertyValue('--panel').trim());
  const shown = (fg, a) => fg.map((v, i) => v * a + panel[i] * (1 - a));
  const ratio = (fg, a) => { const l = [lum(shown(fg, a)), lum(panel)].sort((x, y) => y - x); return (l[0] + 0.05) / (l[1] + 0.05); };
  const worst = { skippedLabel: 99, skippedBorder: 99, dimLabel: 99, dimLine: 99 };
  const take = (k, v) => { worst[k] = Math.min(worst[k], v); };
  cy.nodes(':visible').forEach((n) => {
    const op = n.effectiveOpacity();
    const label = ratio(rgb(n.style('color')), op * +n.style('text-opacity'));
    if (n.hasClass('skipped')) {
      take('skippedLabel', label);
      take('skippedBorder', ratio(rgb(n.style('border-color')), op * +n.style('border-opacity')));
    }
    if (n.hasClass('dim')) take('dimLabel', label);
  });
  cy.edges(':visible').forEach((e) => {
    if (e.hasClass('dim')) take('dimLine', ratio(rgb(e.style('line-color')), e.effectiveOpacity()));
  });
  return worst;
})()`

// TestBrowserGraphContrast: de-emphasized parts of the trace graph (skipped
// branches, and everything off the granting path while it is highlighted)
// stay legible in both themes: labels at 4.5:1, lines and borders at 3:1.
func TestBrowserGraphContrast(t *testing.T) {
	x := newTestExplorer(t)
	b := newBrowser(t, x)
	for _, scheme := range []string{"light", "dark"} {
		t.Run(scheme, func(t *testing.T) {
			b.t = t
			b.run(emulation.SetEmulatedMedia().WithFeatures([]*emulation.MediaFeature{
				{Name: "prefers-color-scheme", Value: scheme}, {Name: "prefers-reduced-motion", Value: "reduce"}}))
			b.open(`~/check?user=user:eng-002&relation=can_view&object=document:doc-0007&context=` +
				`%7B%22current_time%22%3A%222026-11-24T12%3A00%3A00Z%22%2C%22user_ip%22%3A%2210.1.2.3%22%7D`)
			b.waitFor("highlighted path", `document.querySelector('#trace-canvas')?._cyreg?.cy?.edges('.flow').length > 0`)
			// Expand folds so every skipped and dimmed element is measured.
			b.run(chromedp.Evaluate(`(() => { const cy = document.querySelector('#trace-canvas')._cyreg.cy; cy.nodes('.fold').forEach((f) => f.emit('tap')); })()`, nil))
			got := eval[map[string]float64](b, graphContrastJS)
			for k, min := range map[string]float64{"skippedLabel": 4.5, "skippedBorder": 3, "dimLabel": 4.5, "dimLine": 3} {
				if got[k] == 99 {
					t.Errorf("%s: no elements measured", k)
				} else if got[k] < min {
					t.Errorf("%s contrast %.2f:1, want at least %.1f:1", k, got[k], min)
				}
			}
			t.Logf("%s: %v", scheme, got)
			b.checkClean(scheme)
		})
	}
}

// TestBrowserInspector covers the check view's side panel: node details,
// the Text trace / Who else? tabs, collapsing, and the phone bottom sheet.
func TestBrowserInspector(t *testing.T) {
	x := newTestExplorer(t)
	b := newBrowser(t, x)
	b.open(`~/check?user=user:eng-002&relation=can_view&object=document:doc-0007&context=` +
		`%7B%22current_time%22%3A%222026-11-24T12%3A00%3A00Z%22%2C%22user_ip%22%3A%2210.1.2.3%22%7D`)
	b.waitFor("graph", `document.querySelector('#trace-canvas')?._cyreg?.cy?.nodes().length > 0`)
	b.settled()
	if !eval[bool](b, `!!document.querySelector('.inspector #node-detail .nd-empty') && !document.querySelector('#trace-graph #node-detail')`) {
		t.Error("details should start empty, in the side panel (not over the graph)")
	}

	// Selecting a node fills the details; Escape clears them.
	b.run(chromedp.Evaluate(`void document.querySelector('#trace-canvas')._cyreg.cy.nodes('.root').emit('tap')`, nil))
	b.waitFor("details", `document.querySelector('#node-detail h3')?.textContent.includes('document:doc-0007')`)
	b.run(chromedp.Evaluate(`document.activeElement.blur()`, nil), chromedp.KeyEvent(kb.Escape))
	b.waitFor("cleared", `document.querySelector('#node-detail .nd-empty')`)

	// Who else? lists holders; tracing one re-lists in the same tab.
	b.run(chromedp.Click(`#rtab-others`, chromedp.ByQuery))
	b.waitFor("others tab", `!document.querySelector('#rpanel-others').hidden && document.querySelector('#rpanel-trace').hidden`)
	b.run(chromedp.Click(`#rpanel-others form.whohas button[type="submit"]`, chromedp.ByQuery))
	b.waitFor("holders", `document.querySelector('#rpanel-others .lu-trace:not(.current)')`)
	b.run(chromedp.Click(`#rpanel-others .lu-trace:not(.current)`, chromedp.ByQuery))
	b.waitFor("re-traced", `!document.querySelector('.verdict')?.dataset.summary.startsWith('user:eng-002')`)
	// The user picker shows the traced user.
	if got := eval[string](b, `(() => { const p = document.querySelector('#check-form fga-picker[data-field="user"]'); return p.querySelector('.picker-value').value + ' ' + p.querySelector('.picker-type').value + ' ' + p.querySelector('.picker-q').value; })()`); !strings.HasPrefix(got, "user:") || strings.HasPrefix(got, "user:eng-002 ") || !strings.Contains(got, " user ") {
		t.Errorf("user picker after tracing a listed user: %q", got)
	}
	b.waitFor("re-listed", `!document.querySelector('#rpanel-others').hidden && document.querySelector('#rpanel-others .lu-trace')`)

	// Collapsing gives the graph the width; selecting a node reopens the panel.
	width := `document.querySelector('#trace-canvas').getBoundingClientRect().width`
	before := eval[float64](b, width)
	b.run(chromedp.Evaluate(`document.activeElement.blur()`, nil), chromedp.KeyEvent("t"))
	b.waitFor("collapsed", `document.querySelector('.trace-views').classList.contains('panel-collapsed')`)
	if after := eval[float64](b, width); after <= before {
		t.Errorf("graph width %.0f → %.0f after collapsing", before, after)
	}
	if !eval[bool](b, `getComputedStyle(document.querySelector('.panel-rail')).display !== 'none' && document.querySelector('.panel-rail').getAttribute('aria-expanded') === 'false'`) {
		t.Error("collapsed panel should leave a rail to reopen it")
	}
	b.run(chromedp.Evaluate(`void document.querySelector('#trace-canvas')._cyreg.cy.nodes('.root').emit('tap')`, nil))
	b.waitFor("reopened", `!document.querySelector('.trace-views').classList.contains('panel-collapsed')`)

	// Phones: a bottom sheet that collapses to its header.
	b.run(emulation.SetDeviceMetricsOverride(390, 844, 2, true))
	b.waitFor("sheet", `getComputedStyle(document.querySelector('.inspector')).position === 'fixed'`)
	b.run(chromedp.Click(`.insp-head [data-act="toggle-panel"]`, chromedp.ByQuery))
	b.waitFor("sheet collapsed", `document.querySelector('.inspector').getBoundingClientRect().height < 60`)
	b.run(emulation.ClearDeviceMetricsOverride())
	b.checkClean("inspector")
}

// TestBrowserExploreSidePanel: the explore side panel collapses to give the
// graph the width, stays collapsed across reloads, and reopens when the
// viewer opens something from the graph.
func TestBrowserExploreSidePanel(t *testing.T) {
	x := newTestExplorer(t)
	b := newBrowser(t, x)
	b.open("~/explore?type=document")
	b.waitFor("type graph", `document.querySelector('#schema-canvas')._cyreg?.cy?.nodes().length > 0`)
	b.waitFor("type panel", `document.querySelector('#type-panel [data-selected-type="document"]')`)
	width := `document.querySelector('#schema-canvas').getBoundingClientRect().width`
	before := eval[float64](b, width)

	b.run(chromedp.Click(`.canvas-bar [data-act="toggle-side"]`, chromedp.ByQuery))
	b.waitFor("collapsed", `document.querySelector('.explore-layout').classList.contains('side-collapsed')`)
	if after := eval[float64](b, width); after <= before {
		t.Errorf("graph width %.0f → %.0f after collapsing", before, after)
	}
	if !eval[bool](b, `getComputedStyle(document.querySelector('#explore-side')).display === 'none' && getComputedStyle(document.querySelector('.canvas-wrap > .panel-rail')).display !== 'none'`) {
		t.Error("collapsed: side panel hidden and a rail to reopen it")
	}
	if eval[string](b, `document.querySelector('.canvas-bar [data-act="toggle-side"]').getAttribute('aria-expanded')`) != "false" {
		t.Error("toggle aria-expanded should be false when collapsed")
	}

	// Restoring the page (and its selected type) keeps the panel collapsed.
	b.open("~/explore")
	b.waitFor("type graph", `document.querySelector('#schema-canvas')._cyreg?.cy?.nodes().length > 0`)
	b.settled()
	if !eval[bool](b, `document.querySelector('.explore-layout').classList.contains('side-collapsed')`) {
		t.Error("side panel reopened on reload")
	}

	// Choosing a type in the graph reopens it; T toggles it.
	b.run(chromedp.Evaluate(`void document.querySelector('#schema-canvas')._cyreg.cy.getElementById('group').emit('tap')`, nil))
	b.waitFor("reopened", `!document.querySelector('.explore-layout').classList.contains('side-collapsed') && document.querySelector('#type-panel [data-selected-type="group"]')`)
	b.run(chromedp.Evaluate(`document.activeElement.blur()`, nil), chromedp.KeyEvent("t"))
	b.waitFor("T collapses", `document.querySelector('.explore-layout').classList.contains('side-collapsed')`)
	b.run(chromedp.Click(`.canvas-wrap > .panel-rail`, chromedp.ByQuery))
	b.waitFor("rail reopens", `!document.querySelector('.explore-layout').classList.contains('side-collapsed')`)
	b.checkClean("explore side panel")
}

// TestBrowserLargeModel: a model too large to draw starts with a prompt, the
// side panel offers search, recent types (removable), and well-connected
// starting points, and toolbars wrap instead of overlapping.
func TestBrowserLargeModel(t *testing.T) {
	srv := fgatest.Start(t)
	var dsl strings.Builder
	dsl.WriteString("model\n  schema 1.1\ntype user\ntype team\n  relations\n    define member: [user]\n")
	for i := range 60 {
		fmt.Fprintf(&dsl, "type kind_%02d\n  relations\n    define owner: [user, team#member]\n", i)
		if i > 0 {
			fmt.Fprintf(&dsl, "    define parent: [kind_%02d]\n", i-1)
		}
	}
	st, err := srv.CreateStore(context.Background(), "large", dsl.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	x := explorerFor(t, st)
	b := newBrowser(t, x)
	b.open("~/explore")
	b.waitFor("type graph data", `document.querySelector('#schema-canvas')._cyreg?.cy?.nodes().length === 62`)
	if !eval[bool](b, `!document.querySelector('#schema-empty').hidden && document.querySelector('#schema-canvas')._cyreg.cy.nodes(':visible').length === 0`) {
		t.Error("a large model should start with the prompt, not the whole graph")
	}
	// Starting points: the most connected types (user and team reach every kind).
	if got := eval[string](b, `[...document.querySelectorAll('.type-hubs .type-chip')].slice(0, 2).map((c) => c.dataset.type).join()`); got != "team,user" {
		t.Errorf("most connected = %q", got)
	}
	if eval[bool](b, `!!document.querySelector('.type-list')`) {
		t.Error("no alphabetical wall of chips in a large model")
	}

	// Search lists matches; Enter opens the first and shows its neighborhood.
	b.key(`.type-q`, "kind_42")
	b.waitFor("results", `[...document.querySelectorAll('#type-results [role="option"]')].map((o) => o.dataset.value).join() === 'kind_42'`)
	b.key(`.type-q`, kb.Enter)
	b.waitFor("type panel", `document.querySelector('#type-panel [data-selected-type="kind_42"]')`)
	if !eval[bool](b, `document.querySelector('#type-results').hidden && document.querySelector('.type-q').value === ''`) {
		t.Error("choosing a result should clear the search")
	}
	b.waitFor("neighborhood", `document.querySelector('#schema-empty').hidden && document.querySelector('#schema-canvas')._cyreg.cy.nodes(':visible').length > 1`)
	if n := eval[int](b, `document.querySelector('#schema-canvas')._cyreg.cy.nodes(':visible').length`); n > 20 {
		t.Errorf("neighborhood shows %d types", n)
	}
	// Recents: chosen types are listed and can be removed one by one or all at once.
	b.run(chromedp.Click(`.type-hubs .type-chip[data-type="user"]`, chromedp.ByQuery))
	b.waitFor("two recents", `document.querySelectorAll('.type-recent .type-chip').length === 2`)
	// A hub links to every type: it shows its best-connected neighbors, laid
	// out (no two nodes stacked), with a note saying the view is partial.
	distinct := `(() => { const v = document.querySelector('#schema-canvas')._cyreg.cy.nodes(':visible');
		return v.length + '/' + new Set(v.map((n) => Math.round(n.position('x')) + ',' + Math.round(n.position('y')))).size; })()`
	b.waitFor("hub neighborhood", `document.querySelector('#type-panel [data-selected-type="user"]')`)
	if got := eval[string](b, distinct); got != "41/41" {
		t.Errorf("hub neighborhood visible/distinct positions = %s, want 41/41", got)
	}
	if !eval[bool](b, `!document.querySelector('#schema-note').hidden && document.querySelector('#schema-note').textContent.includes('best connected')`) {
		t.Error("capped hub neighborhood should say so")
	}
	// Drawing every type lays out the whole model.
	b.run(chromedp.Click(`.canvas-bar [data-act="show-all-types"]`, chromedp.ByQuery))
	b.waitFor("all drawn", `document.querySelector('#schema-canvas')._cyreg.cy.nodes(':visible').length === 62`)
	if got := eval[string](b, distinct); got != "62/62" {
		t.Errorf("all types visible/distinct positions = %s, want 62/62", got)
	}
	if !eval[bool](b, `document.querySelector('#schema-note').hidden`) {
		t.Error("note should go when every type is drawn")
	}
	b.run(chromedp.Click(`.type-recent [data-remove="kind_42"]`, chromedp.ByQuery))
	b.waitFor("removed", `document.querySelectorAll('.type-recent .type-chip').length === 1`)
	b.run(chromedp.Click(`[data-act="clear-recent-types"]`, chromedp.ByQuery))
	b.waitFor("cleared", `document.querySelector('.type-recent').hidden`)

	// Toolbars wrap rather than overlap in a narrow window.
	b.run(emulation.SetDeviceMetricsOverride(1100, 900, 1, false))
	b.waitFor("narrow", `innerWidth === 1100`)
	overlaps := eval[string](b, `(() => {
		const els = [...document.querySelectorAll('.canvas-bar > :not(.spacer):not(.hidden), .canvas-bar .seg button')].filter((e) => e.offsetParent);
		const bad = [];
		for (const e of els) if (e.scrollWidth > e.clientWidth + 1) bad.push('clipped ' + e.textContent.trim());
		const boxes = els.filter((e) => !e.matches('.seg')).map((e) => [e, e.getBoundingClientRect()]);
		for (let i = 0; i < boxes.length; i++) for (let j = i + 1; j < boxes.length; j++) {
			const [a, r] = boxes[i], [c, q] = boxes[j];
			if (a.contains(c) || c.contains(a)) continue;
			if (r.left < q.right - 1 && q.left < r.right - 1 && r.top < q.bottom - 1 && q.top < r.bottom - 1) bad.push(a.textContent.trim() + ' / ' + c.textContent.trim());
		}
		return bad.join('; ');
	})()`)
	if overlaps != "" {
		t.Errorf("toolbar: %s", overlaps)
	}
	b.run(emulation.ClearDeviceMetricsOverride())
	b.checkClean("large model")
}

// TestBrowserModules: the model page's file tabs, and opening a type from
// its module group in Explore.
func TestBrowserModules(t *testing.T) {
	srv := fgatest.Start(t)
	st, err := srv.CreateModularStore(context.Background(), "modular-demo", fgatest.SeedDir()+"/modular", nil)
	if err != nil {
		t.Fatal(err)
	}
	x := explorerFor(t, st)
	b := newBrowser(t, x)
	b.open("~/model")
	b.waitFor("first file", `!document.querySelector('#mfile-0').hidden && document.querySelector('#mfile-all').hidden`)
	b.run(chromedp.Click(`#mtab-mod`, chromedp.ByQuery))
	b.waitFor("fga.mod", `!document.querySelector('#mfile-mod').hidden && document.querySelector('#mfile-0').hidden`)
	b.run(chromedp.Focus(`#mtab-mod`, chromedp.ByQuery))
	b.key(`#mtab-mod`, kb.ArrowRight)
	b.waitFor("combined via arrow key", `!document.querySelector('#mfile-all').hidden && document.activeElement.id === 'mtab-all'`)
	if !eval[bool](b, `document.querySelector('#mfile-all').textContent.includes('module: compliance')`) {
		t.Error("the combined model should be annotated with modules")
	}

	b.open("~/explore")
	b.waitFor("module groups", `document.querySelectorAll('.by-module .mod-group').length === 3`)
	b.run(chromedp.Click(`.mod-group .type-chip.ext[data-type="document"]`, chromedp.ByQuery))
	b.waitFor("document panel", `document.querySelector('#type-panel [data-selected-type="document"] .modtag')?.textContent === 'module docs'`)
	if n := eval[int](b, `document.querySelectorAll('#type-panel .modtag.ext').length`); n != 3 {
		t.Errorf("relations tagged as added by another module = %d, want 3 (policy, can_audit, can_view)", n)
	}

	// Optional coloring by module: types of one module share a color; a legend names them.
	color := func(typ string) string {
		return eval[string](b, `document.querySelector('#schema-canvas')._cyreg.cy.getElementById('`+typ+`').style('border-color')`)
	}
	if color("document") == color("folder") && color("folder") == color("user") {
		t.Fatal("types should start with their own colors")
	}
	b.run(chromedp.Click(`[data-act="color-modules"]`, chromedp.ByQuery))
	b.waitFor("legend", `document.querySelectorAll('.mod-legend span').length === 3 && !document.querySelector('.mod-legend').hidden`)
	if color("document") != color("folder") || color("document") == color("user") || color("user") != color("group") {
		t.Errorf("by module: document %s folder %s user %s group %s", color("document"), color("folder"), color("user"), color("group"))
	}
	b.checkClean("modules")
}

// TestBrowserScenarios: run a scenario from the editor, link checks to the
// tuples that granted them, and keep the draft across reloads.
func TestBrowserScenarios(t *testing.T) {
	x := newTestExplorer(t)
	b := newBrowser(t, x)
	sample, err := os.ReadFile(fgatest.SeedDir() + "/scenarios.fga.yaml")
	if err != nil {
		t.Fatal(err)
	}
	b.open("~/scenarios")
	b.waitFor("editor", `document.querySelector('#scn-doc')?.value.includes('name: My scenario')`)
	b.run(chromedp.SetValue(`#scn-doc`, string(sample), chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('#scn-doc').dispatchEvent(new Event('input', {bubbles: true}))`, nil))
	b.settled()
	b.run(chromedp.Click(`#scenario-form button[type="submit"]`, chromedp.ByQuery))
	b.waitFor("results", `document.querySelector('.scn-summary h2')?.textContent.includes('All 12 passed')`)

	// Pointing at anne's viewer check highlights the three tuples that granted it.
	b.run(chromedp.Evaluate(`void document.querySelector('tr[data-grants="0 3 4 "]').dispatchEvent(new MouseEvent('mouseover', {bubbles: true}))`, nil))
	if got := eval[string](b, `[...document.querySelectorAll('.scn-test')[0].querySelectorAll('[data-tuple].hl')].map((l) => l.dataset.tuple).join()`); got != "0,3,4" {
		t.Errorf("highlighted tuples = %q", got)
	}

	// The draft survives a reload; Ctrl+Enter runs it.
	b.open("~/scenarios")
	b.waitFor("draft restored", `document.querySelector('#scn-doc').value.includes('name: Document access')`)
	b.run(chromedp.Focus(`#scn-doc`, chromedp.ByQuery), chromedp.KeyEvent(kb.Enter, chromedp.KeyModifiers(input.ModifierCtrl)))
	b.waitFor("ran via keyboard", `document.querySelector('.scn-summary h2')?.textContent.includes('All 12 passed')`)
	b.checkClean("scenarios")
}

// TestBrowserChanges: presets apply at once and update the URL; the custom
// range shows its fields; pages advance in place.
func TestBrowserChanges(t *testing.T) {
	x := newTestExplorer(t) // the seed store: thousands of recent writes
	b := newBrowser(t, x)
	b.open("~/changes")
	if !eval[bool](b, `document.querySelector('.custom-range').hidden`) {
		t.Error("custom fields should be hidden until chosen")
	}
	b.run(chromedp.Click(`.seg-radio input[value="24h"]`, chromedp.ByQuery))
	b.waitFor("first page", `document.querySelectorAll('#change-rows tr.op-write').length === 50 && location.search.includes('range=24h')`)
	if eval[string](b, `document.querySelector('.tz-offset').value`) == "" {
		t.Error("the browser's timezone should be sent")
	}
	first := eval[string](b, `document.querySelector('#change-rows tr td:nth-child(3)').textContent`)
	b.run(chromedp.Click(`.pager a`, chromedp.ByQuery))
	b.waitFor("second page", `location.search.includes('token=') && document.querySelector('#change-rows tr td:nth-child(3)').textContent !== `+fmt.Sprintf("%q", first))
	b.run(chromedp.Click(`.seg-radio input[value="custom"]`, chromedp.ByQuery))
	b.waitFor("custom fields", `!document.querySelector('.custom-range').hidden`)
	b.checkClean("changes")
}

// TestBrowserPalette: Ctrl+K finds pages and types (by relation too) and
// goes there; Escape returns focus.
func TestBrowserPalette(t *testing.T) {
	x := newTestExplorer(t)
	b := newBrowser(t, x)
	b.open("~/check")
	b.run(chromedp.Evaluate(`document.activeElement.blur()`, nil), chromedp.KeyEvent("k", chromedp.KeyModifiers(input.ModifierCtrl)))
	b.waitFor("palette", `!document.querySelector('.palette-overlay').hidden && document.activeElement?.classList.contains('palette-q')`)
	b.key(`.palette-q`, "can_dele")
	b.waitFor("type by relation", `document.querySelector('#palette-list [aria-selected="true"]')?.textContent.startsWith('document')`)
	if got := eval[string](b, `document.querySelector('#palette-list [aria-selected="true"] .muted').textContent`); got != "can_delete" {
		t.Errorf("relation tag = %q", got)
	}
	b.key(`.palette-q`, kb.Enter)
	b.run(chromedp.WaitReady(`#schema-canvas`, chromedp.ByQuery), chromedp.WaitReady(`html[data-ready="true"]`, chromedp.ByQuery))
	if got := eval[string](b, `location.pathname.split('/').pop() + location.search`); got != "explore?type=document" {
		t.Errorf("went to %q", got)
	}

	// Pages, from the header button; Escape closes and returns focus.
	b.open("~/explore")
	b.run(chromedp.Click(`[data-act="palette"]`, chromedp.ByQuery))
	b.waitFor("open", `!document.querySelector('.palette-overlay').hidden`)
	b.key(`.palette-q`, "chang")
	b.waitFor("changes page", `document.querySelector('#palette-list [aria-selected="true"]')?.textContent.startsWith('Changes')`)
	b.run(chromedp.KeyEvent(kb.Escape))
	b.waitFor("closed, focus back", `document.querySelector('.palette-overlay').hidden && document.activeElement?.dataset.act === 'palette'`)
	b.checkClean("palette")
}

// TestBrowserAccess: an admin opens an instance's Access tab, picks a type
// and relations, and lists what it can access.
func TestBrowserAccess(t *testing.T) {
	x := explorerFor(t, fgatest.Seed(t), func(c *config.Config) {
		c.LiveQueries.Enabled = true
		c.Auth.DangerouslyTreatAnonymousAsAdmin = true
	})
	b := newBrowser(t, x)
	b.open("~/explore?type=user&id=anne")
	b.waitFor("access tab", `document.querySelector('#itab-access')`)
	b.settled()
	b.run(chromedp.Click(`#itab-access`, chromedp.ByQuery))
	b.waitFor("form", `document.querySelector('#ipanel-access .access-form select[name="type"]')`)
	b.settled()
	b.run(chromedp.SetValue(`#ipanel-access select[name="type"]`, "group", chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('#ipanel-access select[name="type"]').dispatchEvent(new Event('change', {bubbles: true}))`, nil))
	b.waitFor("group relations", `document.querySelector('#ipanel-access input[name="rel"][value="member"]')`)
	b.settled()
	b.run(chromedp.Click(`#ipanel-access button[name="run"]`, chromedp.ByQuery))
	b.waitFor("results", `[...document.querySelectorAll('#ipanel-access .access-objects code')].some((c) => c.textContent === 'group:eng')`)
	b.checkClean("access")
}

// TestBrowserCapture: "Add to scenario" on a check result puts the test,
// with renamed IDs, into the Scenarios draft.
func TestBrowserCapture(t *testing.T) {
	x := newTestExplorer(t)
	b := newBrowser(t, x)
	b.open(`~/check?user=user:eng-002&relation=can_view&object=document:doc-0007&context=` +
		`%7B%22current_time%22%3A%222026-11-24T12%3A00%3A00Z%22%2C%22user_ip%22%3A%2210.1.2.3%22%7D`)
	b.waitFor("result", `document.querySelector('.verdict [data-act="capture"]')`)
	if !eval[bool](b, `document.querySelector('[data-act="capture-mask"]').checked`) {
		t.Error("renaming IDs should be on by default")
	}
	b.run(chromedp.Click(`.verdict [data-act="capture"]`, chromedp.ByQuery))
	b.waitFor("added", `document.querySelector('.capture-msg')?.textContent.startsWith('Added “user:user-1 can_view document:document-1”')`)
	b.open("~/scenarios")
	b.waitFor("draft", `document.querySelector('#scn-doc').value.includes('user:user-1 can_view document:document-1')`)
	if eval[bool](b, `document.querySelector('#scn-doc').value.includes('eng-002')`) {
		t.Error("the masked draft contains a real ID")
	}
	b.run(chromedp.Click(`#scenario-form button[type="submit"]`, chromedp.ByQuery))
	b.waitFor("passes", `document.querySelector('.scn-summary h2')?.textContent.includes('All 1 passed')`)
	b.checkClean("capture")
}

// TestBrowserCheckKeepsView: leaving the Check page (for Model) and coming
// back re-runs the check; the zoom, pan, opened folds, and selected node the
// person had are put back rather than refitting the graph.
func TestBrowserCheckKeepsView(t *testing.T) {
	x, check := foldedCheck(t)
	b := newBrowser(t, x)
	b.open(check)
	b.waitFor("graph", `document.querySelector('#trace-canvas')?._cyreg?.cy?.nodes().length > 0`)
	b.settled()
	const cy = `document.querySelector('#trace-canvas')._cyreg.cy`
	// Open a fold, select a node, then zoom and pan.
	fold := eval[string](b, `(() => { const f = `+cy+`.nodes('.fold')[0]; if (!f) return ''; const id = f.id(); f.emit('tap'); return id; })()`)
	if fold == "" {
		t.Fatal("no folded branch to open")
	}
	sel := eval[string](b, `(() => { const n = `+cy+`.nodes(':visible').not('.fold').filter((n) => n.data('detail'))[1]; n.emit('tap'); n.select(); return n.id(); })()`)
	b.run(chromedp.Evaluate(`void `+cy+`.viewport({ zoom: 1.7, pan: { x: 123, y: 45 } })`, nil))
	b.waitFor("view saved", `(JSON.parse(Object.entries(sessionStorage).find(([k]) => k.startsWith('fgax.checkView:'))?.[1] || '{}').zoom === 1.7)`)

	b.open("~/model")
	b.open("~/check")
	b.waitFor("restored graph", `document.querySelector('#trace-canvas')?._cyreg?.cy?.nodes().length > 0`)
	b.settled()
	b.waitFor("view restored", `Math.abs(`+cy+`.zoom() - 1.7) < 0.001`)
	if got := eval[[]float64](b, `[`+cy+`.pan().x, `+cy+`.pan().y]`); len(got) != 2 || got[0] != 123 || got[1] != 45 {
		t.Errorf("pan = %v, want [123 45]", got)
	}
	if eval[bool](b, cy+`.getElementById('`+fold+`').nonempty()`) {
		t.Error("the opened fold is closed again")
	}
	if !eval[bool](b, cy+`.getElementById('`+sel+`').selected()`) || !eval[bool](b, `!document.querySelector('#node-detail .nd-empty')`) {
		t.Error("the selected node and its details were not restored")
	}
	b.checkClean("check keeps view")
}

// foldedCheck is a store whose check graph has a folded branch: a union of
// six relations, none of which holds (FoldMin is 5 failed siblings), each
// with a nested failure to step into.
func foldedCheck(t *testing.T) (*testExplorer, string) {
	t.Helper()
	srv := fgatest.Start(t)
	model := "model\n  schema 1.1\ntype user\ntype group\n  relations\n    define member: [user]\ntype doc\n  relations\n"
	var rels []string
	var tuples []*openfgav1.TupleKey
	for i := 1; i <= 6; i++ {
		model += fmt.Sprintf("    define r%d: [user, group#member]\n", i)
		rels = append(rels, fmt.Sprintf("r%d", i))
		tuples = append(tuples, &openfgav1.TupleKey{User: fmt.Sprintf("group:g%d#member", i), Relation: fmt.Sprintf("r%d", i), Object: "doc:d"})
	}
	model += "    define viewer: " + strings.Join(rels, " or ") + "\n"
	st, err := srv.CreateStore(context.Background(), "folded", model, tuples)
	if err != nil {
		t.Fatal(err)
	}
	return explorerFor(t, st), "~/check?user=user:anne&relation=viewer&object=doc:d&order=all"
}

// TestBrowserCheckStep: "Step ›" works from the finished result (it starts
// the walk over); every press (and → / ←) moves exactly one counted step and
// visibly changes what is lit, lighting the fold once when the walk goes into
// a folded branch.
func TestBrowserCheckStep(t *testing.T) {
	x, check := foldedCheck(t)
	b := newBrowser(t, x)
	b.open(check)
	b.waitFor("graph", `document.querySelector('#trace-canvas')?._cyreg?.cy?.nodes('.fold').length > 0`)
	b.settled()
	const cy = `document.querySelector('#trace-canvas')._cyreg.cy`
	const count = `document.querySelector('.player .step-count').textContent.match(/\d+/g).map(Number)`
	const lit = `(` + cy + `.nodes('.visiting:visible').map((n) => n.id()).join(','))`
	// A zoom the person set survives stepping, including the last step.
	b.run(chromedp.Evaluate(`void `+cy+`.zoom(1.7)`, nil))
	total := eval[int](b, `(() => { const [i, n] = `+count+`; return i === n ? n : -1; })()`)
	if total <= 0 {
		t.Fatalf("the result should show the finished walk")
	}
	prevLit, sawFold := "", false
	var lits []string
	for want := 1; want <= total; want++ {
		b.run(chromedp.Click(`.player [data-act="step"]`, chromedp.ByQuery))
		if i := eval[[]int](b, count)[0]; i != want {
			t.Fatalf("press %d: the count shows step %d", want, i)
		}
		l := eval[string](b, lit)
		if strings.Contains(l, ",") || l == "" || l == prevLit {
			t.Fatalf("step %d: lit %q after %q; each step should light one new element", want, l, prevLit)
		}
		prevLit = l
		lits = append(lits, l)
		sawFold = sawFold || eval[bool](b, cy+`.nodes('.fold.visiting').length > 0`)
		if want == 1 && !strings.Contains(eval[string](b, `document.querySelector('.player [data-act="play"]').textContent`), "Resume") {
			t.Error("after the first step the replay should be resumable")
		}
	}
	if !sawFold {
		t.Error("stepping into the folded branch never lit the fold")
	}
	// The keys move by the same steps: ← goes back one, → forward one.
	b.run(chromedp.Evaluate(`document.activeElement.blur()`, nil), chromedp.KeyEvent(kb.ArrowLeft))
	if i, l := eval[[]int](b, count)[0], eval[string](b, lit); i != total-1 || l != lits[total-2] {
		t.Errorf("← : step %d lit %q, want step %d lit %q", i, l, total-1, lits[total-2])
	}
	b.run(chromedp.KeyEvent(kb.ArrowRight))
	if i, l := eval[[]int](b, count)[0], eval[string](b, lit); i != total || l != lits[total-1] {
		t.Errorf("→ : step %d lit %q, want step %d lit %q", i, l, total, lits[total-1])
	}
	// One more step finishes the walk.
	b.run(chromedp.Click(`.player [data-act="step"]`, chromedp.ByQuery))
	if eval[bool](b, `document.querySelector('.trace-text .tree').classList.contains('animating')`) {
		t.Error("the walk did not finish")
	}
	// From the finished result, ← shows the last step, then the one before.
	for _, want := range []int{total, total - 1} {
		b.run(chromedp.KeyEvent(kb.ArrowLeft))
		if i, l := eval[[]int](b, count)[0], eval[string](b, lit); i != want || l != lits[want-1] {
			t.Errorf("← from the end: step %d lit %q, want step %d lit %q", i, l, want, lits[want-1])
		}
	}
	// The replay moves by the same steps: each tick counts one and lights a node
	// (never an arrow alone).
	b.run(chromedp.Evaluate(`window.__ticks = []; new MutationObserver(() => window.__ticks.push([`+count+`[0], `+lit+`, document.querySelector('.trace-text .tree').classList.contains('animating')]))
		.observe(document.querySelector('.player .step-count'), { childList: true, characterData: true, subtree: true })`, nil),
		chromedp.Evaluate(`const s = document.querySelector('.player [data-act="speed"]'); s.value = s.max; s.dispatchEvent(new Event('input'))`, nil),
		chromedp.Click(`.player [data-act="final"]`, chromedp.ByQuery), chromedp.Click(`.player [data-act="play"]`, chromedp.ByQuery))
	b.waitFor("replay finished", `!document.querySelector('.trace-text .tree').classList.contains('animating') && window.__ticks.length > 1`)
	ticks := eval[[][]any](b, `window.__ticks`)
	prevStep := 0
	for k, tk := range ticks {
		i := int(tk[0].(float64))
		if running := tk[2].(bool); !running || i == 0 {
			continue
		}
		if tk[1] == "" || i != prevStep+1 {
			t.Errorf("replay tick %d: step %d lit %q after step %d", k, i, tk[1], prevStep)
		}
		prevStep = i
	}
	if prevStep != total {
		t.Errorf("the replay showed %d of %d steps", prevStep, total)
	}
	if z := eval[float64](b, cy+`.zoom()`); z != 1.7 {
		t.Errorf("stepping changed the zoom to %v", z)
	}
	b.checkClean("check step")
}

// TestBrowserCheckFillsWindow: on a large screen the graph and side panel
// reach the bottom of the window, also after the form above collapses, and
// resizing the window (or the side panel) keeps a zoom the person set.
func TestBrowserCheckFillsWindow(t *testing.T) {
	x := newTestExplorer(t)
	b := newBrowser(t, x)
	b.run(emulation.SetDeviceMetricsOverride(2560, 1400, 1, false))
	b.open(`~/check?user=user:eng-002&relation=can_view&object=document:doc-0007&context=` +
		`%7B%22current_time%22%3A%222026-11-24T12%3A00%3A00Z%22%2C%22user_ip%22%3A%2210.1.2.3%22%7D`)
	b.waitFor("graph", `document.querySelector('#trace-canvas')?._cyreg?.cy?.nodes().length > 0`)
	b.waitFor("collapsed form", `document.querySelector('#check-form').classList.contains('collapsed')`)
	const gap = `(innerHeight - document.querySelector('#trace-graph').getBoundingClientRect().bottom)`
	b.waitFor("graph reaching the window bottom", gap+` >= 0 && `+gap+` <= 24`)
	if d := eval[float64](b, `Math.abs(document.querySelector('#trace-graph').getBoundingClientRect().bottom - document.querySelector('#inspector').getBoundingClientRect().bottom)`); d > 2 {
		t.Errorf("side panel and graph end %vpx apart", d)
	}
	const cy = `document.querySelector('#trace-canvas')._cyreg.cy`
	// The graph itself follows its box (it settles just after drawing).
	b.waitFor("graph filling its box", cy+`.height() > 900 && `+cy+`.height() === document.querySelector('#trace-canvas').clientHeight`)
	// Take charge of the view, then resize the window and toggle the panel.
	b.run(chromedp.Click(`.player [data-act="step"]`, chromedp.ByQuery), chromedp.Evaluate(`void `+cy+`.zoom(1.7)`, nil))
	b.run(emulation.SetDeviceMetricsOverride(2000, 1100, 1, false))
	b.waitFor("graph resized", gap+` >= 0 && `+gap+` <= 24`)
	b.run(chromedp.Click(`.insp-head [data-act="toggle-panel"]`, chromedp.ByQuery))
	b.waitFor("panel collapsed", `document.querySelector('.trace-views').classList.contains('panel-collapsed')`)
	if z := eval[float64](b, cy+`.zoom()`); z != 1.7 {
		t.Errorf("resizing changed the zoom to %v", z)
	}
	// Finishing the walk would frame the granting path; not over the person's view.
	b.run(chromedp.Click(`.panel-rail`, chromedp.ByQuery), chromedp.Click(`.player [data-act="final"]`, chromedp.ByQuery), chromedp.Sleep(300*time.Millisecond))
	if z := eval[float64](b, cy+`.zoom()`); z != 1.7 {
		t.Errorf("finishing the walk changed the zoom to %v", z)
	}
	b.checkClean("check fills window")
}

// TestBrowserTextTraceHighlightWidth: text trace lines that run past the
// side panel keep their highlight (selection, current step, hover) under the
// whole line, at any panel width.
func TestBrowserTextTraceHighlightWidth(t *testing.T) {
	x := newTestExplorer(t)
	b := newBrowser(t, x)
	b.open(`~/check?user=user:eng-002&relation=can_view&object=document:doc-0007&context=` +
		`%7B%22current_time%22%3A%222026-11-24T12%3A00%3A00Z%22%2C%22user_ip%22%3A%2210.1.2.3%22%7D`)
	b.waitFor("text trace", `document.querySelectorAll('#rpanel-trace .tl').length > 0`)
	// Every line box is at least as wide as its own text and the widest line,
	// and no narrower than the pane.
	const uncovered = `(() => { const pane = document.querySelector('#rpanel-trace'); const ls = [...pane.querySelectorAll('.tl')];
		const widest = Math.max(...ls.map((l) => l.scrollWidth));
		return ls.filter((l) => l.getBoundingClientRect().width + 1 < Math.max(widest, pane.clientWidth - 20)).length; })()`
	for _, w := range []int{320, 520} {
		b.run(chromedp.Evaluate(fmt.Sprintf(`document.querySelector('.trace-views').style.setProperty('--text-w', '%dpx')`, w), nil))
		if !eval[bool](b, `document.querySelector('#rpanel-trace').scrollWidth > document.querySelector('#rpanel-trace').clientWidth`) {
			t.Fatalf("at %dpx the text trace does not overflow; the test needs long lines", w)
		}
		if n := eval[int](b, uncovered); n != 0 {
			t.Errorf("at %dpx, %d lines are narrower than the text they hold", w, n)
		}
	}
	b.checkClean("text trace highlight")
}

// TestBrowserGrantingTuples: with every granting path shown, "N granting
// tuples" highlights those tuples in the graph and the text trace and lists
// them; it switches off again; and the result can go back to the shortest
// explanation.
func TestBrowserGrantingTuples(t *testing.T) {
	x := newTestExplorer(t)
	b := newBrowser(t, x)
	b.open(`~/check?user=user:eng-002&relation=can_view&object=document:doc-0007&order=all&context=` +
		`%7B%22current_time%22%3A%222026-11-24T12%3A00%3A00Z%22%2C%22user_ip%22%3A%2210.1.2.3%22%7D`)
	b.waitFor("graph", `document.querySelector('#trace-canvas')?._cyreg?.cy?.nodes().length > 0`)
	b.settled()
	const cy = `document.querySelector('#trace-canvas')._cyreg.cy`
	const btn = `document.querySelector('.verdict [data-act="show-grants"]')`
	n := eval[int](b, `new Set(`+cy+`.edges('.grant').map((e) => e.data('tuple'))).size`)
	if n == 0 {
		t.Fatal("no granting tuples marked in the graph")
	}
	if got := eval[string](b, btn+`?.textContent.trim() || ''`); got != fmt.Sprintf("%d granting tuple%s", n, map[bool]string{true: "", false: "s"}[n == 1]) {
		t.Errorf("button %q, graph marks %d tuples", got, n)
	}

	b.run(chromedp.Click(`.verdict [data-act="show-grants"]`, chromedp.ByQuery))
	if !eval[bool](b, btn+`.getAttribute('aria-pressed') === 'true' && `+cy+`.edges('.grant-on').length === `+cy+`.edges('.grant').length`) {
		t.Error("the granting tuples are not highlighted")
	}
	if eval[int](b, `document.querySelectorAll('#rpanel-trace .tl.grant-line').length`) == 0 {
		t.Error("no text trace line is highlighted")
	}
	// The other tuples on the granting paths are labeled and listed too.
	path := eval[int](b, `new Set(`+cy+`.edges('.flowpath').filter((e) => e.data('tuple')).map((e) => e.data('tuple'))).size`)
	if path <= n {
		t.Fatalf("the granting path has %d tuples; the test needs some besides the %d granting ones", path, n)
	}
	if got := eval[int](b, `document.querySelectorAll('#node-detail .grant-list [data-edge]').length`); got != path {
		t.Errorf("the lists have %d tuples, want %d (%d granting)", got, path, n)
	}
	if got := eval[int](b, `document.querySelectorAll('#node-detail .grant-list')[0].querySelectorAll('[data-edge]').length`); got != n {
		t.Errorf("the granting list has %d tuples, want %d", got, n)
	}
	if eval[int](b, cy+`.edges('.tuple-on').length`) == 0 {
		t.Error("the other path tuples are not labeled")
	}
	// Picking one from the list finds it in the graph.
	b.run(chromedp.Click(`#node-detail .grant-list [data-edge]`, chromedp.ByQuery))
	if !eval[bool](b, ``+cy+`.getElementById(document.querySelector('#node-detail [data-edge]').dataset.edge).hasClass('hl')`) {
		t.Error("the chosen tuple is not highlighted")
	}

	b.run(chromedp.Click(`.verdict [data-act="show-grants"]`, chromedp.ByQuery))
	if !eval[bool](b, btn+`.getAttribute('aria-pressed') === 'false' && `+cy+`.edges('.grant-on, .tuple-on').length === 0 && !document.querySelector('.tl.grant-line, .tl.tuple-line, .grant-list')`) {
		t.Error("switching off left highlights behind")
	}

	// Back to the shortest explanation.
	b.run(chromedp.Click(`.verdict [data-act="order"][data-order="guided"]`, chromedp.ByQuery))
	b.waitFor("shortest explanation", `document.querySelector('.verdict [data-act="all-paths"]')`)
	if !strings.Contains(eval[string](b, `location.search`), "order=guided") {
		t.Error("URL does not record the order")
	}
	b.checkClean("granting tuples")
}

// TestBrowserExploreTypeGraphKeepsDetails: selecting a type in the type graph
// (or with a type chip) shows its details without opening the instance
// picker over them; "/" still opens it.
func TestBrowserExploreTypeGraphKeepsDetails(t *testing.T) {
	x := newTestExplorer(t)
	b := newBrowser(t, x)
	b.open("~/explore")
	b.waitFor("type graph", `document.querySelector('#schema-canvas')._cyreg?.cy?.nodes().length > 0`)
	const pickerOpen = `(document.activeElement?.matches('#type-panel .picker-q') || !!document.querySelector('#type-panel .sr li'))`
	for _, typ := range []string{"folder", "document"} {
		b.run(chromedp.Evaluate(`void document.querySelector('#schema-canvas')._cyreg.cy.getElementById('`+typ+`').emit('tap')`, nil))
		b.waitFor(typ+" panel", `document.querySelector('#type-panel [data-selected-type="`+typ+`"]')`)
		b.settled()
		b.run(chromedp.Sleep(300 * time.Millisecond)) // the picker would open after settling
		if eval[bool](b, pickerOpen) {
			t.Errorf("tapping %s in the type graph opened the instance picker", typ)
		}
	}
	// Nor does a type chip.
	b.run(chromedp.Click(`.type-chip[data-type="group"]`, chromedp.ByQuery))
	b.waitFor("group panel", `document.querySelector('#type-panel [data-selected-type="group"]')`)
	b.settled()
	b.run(chromedp.Sleep(300 * time.Millisecond))
	if eval[bool](b, pickerOpen) {
		t.Error("clicking a type chip opened the instance picker")
	}
	// The picker is still one key away.
	b.run(chromedp.Evaluate(`document.activeElement.blur()`, nil), chromedp.KeyEvent("/"))
	b.waitFor("picker on /", `document.activeElement?.matches('#type-panel .picker-q')`)
	b.checkClean("type graph keeps details")
}

// TestBrowserCheckEditKeepsPickerClosed: "Edit" (and "e") re-opens the
// form without opening an instance list over it; focus starts at the form,
// and "/" still opens the picker.
func TestBrowserCheckEditKeepsPickerClosed(t *testing.T) {
	x := newTestExplorer(t)
	b := newBrowser(t, x)
	b.open(`~/check?user=user:eng-002&relation=can_view&object=document:doc-0007&context=` +
		`%7B%22current_time%22%3A%222026-11-24T12%3A00%3A00Z%22%2C%22user_ip%22%3A%2210.1.2.3%22%7D`)
	b.waitFor("collapsed form", `document.querySelector('#check-form').classList.contains('collapsed')`)
	b.settled()
	const listOpen = `[...document.querySelectorAll('#check-form .picker-results')].some((r) => r.children.length > 0 && r.offsetParent)`
	for _, how := range []string{"button", "key"} {
		if how == "button" {
			b.run(chromedp.Click(`#check-form [data-act="edit-check"]`, chromedp.ByQuery))
		} else {
			b.run(chromedp.Evaluate(`document.querySelector('#check-form').classList.add('collapsed'); document.activeElement.blur()`, nil), chromedp.KeyEvent("e"))
		}
		b.waitFor("form expanded", `!document.querySelector('#check-form').classList.contains('collapsed')`)
		b.settled()
		b.run(chromedp.Sleep(300 * time.Millisecond))
		if eval[bool](b, listOpen) {
			t.Errorf("edit (%s) opened an instance list", how)
		}
		if !eval[bool](b, `document.activeElement?.id === 'check-form'`) {
			t.Errorf("edit (%s): focus is on %s, want the form", how, eval[string](b, `document.activeElement?.outerHTML.slice(0, 80) || ''`))
		}
	}
	// "/" still opens the picker on purpose.
	b.run(chromedp.KeyEvent("/"))
	b.waitFor("picker on /", `document.activeElement?.matches('#check-form .picker-q')`)
	b.checkClean("check edit")
}

// TestBrowserCheckOptionHelp: every Check option has a "?" that shows a
// plain explanation (one at a time, toggled), linked to its control for
// screen readers, without acting on the control itself.
func TestBrowserCheckOptionHelp(t *testing.T) {
	x := newTestExplorer(t)
	b := newBrowser(t, x)
	b.open("~/check")
	b.run(chromedp.Evaluate(`document.querySelector('#check-form .more-opts').open = true`, nil))
	// Every option control is described by a help paragraph that exists.
	if bad := eval[[]string](b, `[...document.querySelectorAll('#check-form .more-opts textarea, #check-form .more-opts select, #check-form .more-opts input')]
		.filter((c) => !document.getElementById(c.getAttribute('aria-describedby') || '-')?.textContent.trim()).map((c) => c.name)`); len(bad) > 0 {
		t.Errorf("options without help: %v", bad)
	}
	buttons := eval[[]string](b, `[...document.querySelectorAll('#check-form [data-act="opt-help"]')].map((q) => q.getAttribute('aria-controls'))`)
	if len(buttons) != 8 {
		t.Fatalf("%d help buttons, want 8", len(buttons))
	}
	if small := eval[int](b, `[...document.querySelectorAll('#check-form .opt-q')].filter((q) => { const r = q.getBoundingClientRect(); return r.width < 24 || r.height < 24; }).length`); small > 0 {
		t.Errorf("%d help buttons are smaller than 24×24", small)
	}
	// Each option's caption (name and "?") sits above its control, not into it.
	for _, w := range []int64{1440, 1100} {
		b.run(emulation.SetDeviceMetricsOverride(w, 800, 1, false))
		if bad := eval[[]string](b, `[...document.querySelectorAll('#check-form .opts label')].filter((l) => {
			const cap = l.querySelector('.opt-cap').getBoundingClientRect(), c = l.querySelector('select, input').getBoundingClientRect();
			return cap.bottom > c.top + 1; }).map((l) => l.textContent.trim().split('\n')[0])`); len(bad) > 0 {
			t.Errorf("at %dpx these captions run into their controls: %v", w, bad)
		}
	}
	shown := `[...document.querySelectorAll('#check-form .opt-help')].filter((h) => !h.hidden).map((h) => h.id)`
	order := `document.querySelector('#check-form select[name="order"]')`
	before := eval[string](b, order+`.value`)
	for _, id := range buttons {
		b.run(chromedp.Click(`#check-form [aria-controls="`+id+`"]`, chromedp.ByQuery))
		if got := eval[[]string](b, shown); len(got) != 1 || got[0] != id {
			t.Errorf("after ? for %s, shown: %v", id, got)
		}
		if !eval[bool](b, `document.querySelector('#check-form [aria-controls="`+id+`"]').getAttribute('aria-expanded') === 'true'`) {
			t.Errorf("? for %s is not marked expanded", id)
		}
	}
	b.run(chromedp.Click(`#check-form [aria-controls="`+buttons[len(buttons)-1]+`"]`, chromedp.ByQuery))
	if got := eval[[]string](b, shown); len(got) != 0 {
		t.Errorf("pressing ? again left %v shown", got)
	}
	if eval[string](b, order+`.value`) != before || eval[bool](b, `document.activeElement?.name === 'order'`) {
		t.Error("pressing ? acted on the option's control")
	}
	b.checkClean("check option help")
}

// TestBrowserTraceReplacedMidReplay: a result replaced while its walk is
// replaying stops replaying; it does not drive the new result's controls.
func TestBrowserTraceReplacedMidReplay(t *testing.T) {
	x := newTestExplorer(t)
	b := newBrowser(t, x)
	b.open("~/check")
	// Replay on load, slowly (reduced motion turns it off by default).
	b.run(chromedp.Evaluate(`localStorage.setItem('fgax.autoplay', 'true'); localStorage.setItem('fgax.speed', '1')`, nil))
	b.open(`~/check?user=user:eng-002&relation=can_view&object=document:doc-0007`)
	b.waitFor("replaying", `document.querySelector('.player [data-act="play"]')?.textContent.includes('Pause')`)
	// Run it again, this time without a replay.
	b.run(chromedp.Evaluate(`localStorage.setItem('fgax.autoplay', 'false'); document.querySelector('fga-trace').dataset.old = '1'; htmx.trigger('#check-form', 'submit')`, nil))
	b.waitFor("new result", `document.querySelector('fga-trace:not([data-old]) .player .step-count')?.textContent`)
	b.settled()
	const controls = `document.querySelector('.player [data-act="play"]').textContent + ' | ' + document.querySelector('.player .step-count').textContent`
	first := eval[string](b, controls)
	b.run(chromedp.Sleep(1500 * time.Millisecond)) // several steps of the old replay
	if got := eval[string](b, controls); got != first || !strings.HasPrefix(got, "▶ Replay walk") {
		t.Errorf("the new result's controls changed from %q to %q: the old replay is still running", first, got)
	}
	b.checkClean("replaced mid-replay")
}

// TestBrowserExploreExpand: expanding in place (Shift+click, the node menu,
// +1 hop) adds edges without opening anything or moving the nodes already
// placed, says what it added, and can be undone.
func TestBrowserExploreExpand(t *testing.T) {
	x := newTestExplorer(t)
	b := newBrowser(t, x)
	b.open("~/explore?type=document&id=doc-0007")
	b.waitFor("instance panel", `document.querySelector('#instance-panel .instance[data-object="document:doc-0007"]')`)
	b.settled()
	const cy = `document.querySelector('#instance-canvas')._cyreg.cy`
	const count = cy + `.nodes().length`
	// Where a node is on the page, for real mouse clicks.
	at := func(id string) (float64, float64) {
		p := eval[[]float64](b, `(() => { const n = `+cy+`.getElementById('`+id+`'); const r = `+cy+`.container().getBoundingClientRect(); const q = n.renderedPosition(); return [r.left + q.x, r.top + q.y]; })()`)
		return p[0], p[1]
	}
	positions := `JSON.stringify(` + cy + `.nodes().map((n) => [n.id(), Math.round(n.position('x')), Math.round(n.position('y'))]))`
	note := `document.querySelector('#instance-note .note-text').textContent`

	// Shift+click a peer: its edges are added; nothing else changes.
	peer := eval[string](b, cy+`.nodes().filter((n) => n.data('kind') !== 'count' && !n.data('focus') && !n.hasClass('expanded'))[0].id()`)
	before, placed := eval[int](b, count), eval[string](b, positions)
	px, py := at(peer)
	b.run(chromedp.MouseClickXY(px, py, chromedp.ButtonModifiers(input.ModifierShift)))
	b.waitFor("expanded", note+`.startsWith('Added') || `+note+`.startsWith('Nothing new')`)
	if !eval[bool](b, cy+`.getElementById('`+peer+`').hasClass('expanded')`) {
		t.Errorf("%s is not marked expanded", peer)
	}
	if got := eval[string](b, `document.querySelector('#instance-panel .instance').dataset.object`); got != "document:doc-0007" {
		t.Errorf("Shift+click opened %s", got)
	}
	var kept []any
	for _, p := range regexp.MustCompile(`\["[^"]+",-?\d+,-?\d+\]`).FindAllString(placed, -1) {
		kept = append(kept, p)
	}
	after := eval[string](b, positions)
	for _, p := range kept {
		if !strings.Contains(after, p.(string)) {
			t.Errorf("node moved: %s", p)
		}
	}
	grown := eval[int](b, count)
	t.Logf("Shift+click %s: %d → %d nodes: %s", peer, before, grown, eval[string](b, note))

	// Undo puts the graph back.
	if grown > before {
		b.run(chromedp.Click(`#instance-note [data-act="undo-expand"]`, chromedp.ByQuery))
		b.waitFor("undone", count+` === `+fmt.Sprint(before))
		if eval[bool](b, cy+`.getElementById('`+peer+`').hasClass('expanded')`) {
			t.Errorf("%s still marked expanded after Undo", peer)
		}
	}

	// The panel's "Expand neighbors" (+1 hop) expands the open instance's neighbors.
	b.run(chromedp.Click(`#instance-panel [data-act="expand-hop"]`, chromedp.ByQuery))
	b.waitFor("hop", note+`.includes('around') && !`+note+`.endsWith('…')`)
	if n := eval[int](b, cy+`.getElementById('document:doc-0007').neighborhood().nodes().filter((n) => n.data('kind') !== 'count' && !n.hasClass('expanded') && !n.data('focus')).length`); n > 0 &&
		!strings.Contains(eval[string](b, note), "again for more") {
		t.Errorf("%d neighbors not expanded after +1 hop: %s", n, eval[string](b, note))
	}

	// Right-click a node: its menu, by keyboard: the last item removes it.
	// A leaf at the top of the graph, clear of other nodes' edges.
	victim := eval[string](b, cy+`.nodes().filter((n) => n.data('kind') !== 'count' && !n.hasClass('current') && n.degree() === 1).sort((a, b) => a.renderedPosition('y') - b.renderedPosition('y'))[0].id()`)
	vx, vy := at(victim)
	b.run(chromedp.MouseClickXY(vx, vy, chromedp.Button("right")))
	b.waitFor("menu", `!document.querySelector('#node-menu').hidden && document.activeElement?.matches('#node-menu [role="menuitem"]')`)
	if got := eval[string](b, `document.querySelector('#node-menu .nm-title').textContent`); got == "" {
		t.Error("the menu does not name its node")
	}
	b.run(chromedp.KeyEvent(kb.ArrowUp), chromedp.KeyEvent(kb.Enter)) // ↑ from the first wraps to the last
	b.waitFor("removed", `!`+cy+`.getElementById('`+victim+`').length && document.querySelector('#node-menu').hidden`)
	b.checkClean("expand")
}

// TestBrowserExploreExpandLimits: an expansion a limit cuts short says so;
// with expanding turned off, its controls are not offered.
func TestBrowserExploreExpandLimits(t *testing.T) {
	x := explorerFor(t, fgatest.Seed(t), func(c *config.Config) { c.GraphExpansion.MaxReads = 3 })
	b := newBrowser(t, x)
	b.open("~/explore?type=document&id=doc-0007")
	b.waitFor("instance panel", `document.querySelector('#instance-panel .instance[data-object="document:doc-0007"]')`)
	b.settled()
	note := `document.querySelector('#instance-note .note-text').textContent`
	b.run(chromedp.Click(`#instance-panel [data-act="expand-hop"]`, chromedp.ByQuery))
	b.waitFor("hop", `!`+note+`.endsWith('…') && `+note+`.includes('around')`)
	if got := eval[string](b, note); !strings.Contains(got, "only partly read: the read budget (3 reads) ran out") {
		t.Errorf("a cut-short expansion: %q", got)
	}
	b.checkClean("expand limits")

	off := explorerFor(t, fgatest.Seed(t), func(c *config.Config) { c.GraphExpansion.Enabled = false })
	b = newBrowser(t, off)
	b.open("~/explore?type=document&id=doc-0007")
	b.waitFor("instance panel", `document.querySelector('#instance-panel .instance')`)
	if n := eval[int](b, `document.querySelectorAll('[data-act="expand-hop"], [data-node-act="expand"], [data-node-act="hop"]').length`); n != 0 {
		t.Errorf("expanding is off, but %d of its controls are offered", n)
	}
	b.checkClean("expand off")
}

// TestBrowserExploreTabPerType: the instance panel reopens on the tab last
// used for that type; another type keeps its own (or the server's default).
func TestBrowserExploreTabPerType(t *testing.T) {
	x := newTestExplorer(t)
	b := newBrowser(t, x)
	b.open("~/explore?type=document&id=doc-0007")
	b.waitFor("instance panel", `document.querySelector('#instance-panel .instance[data-object="document:doc-0007"]')`)
	b.settled()
	b.run(chromedp.Click(`#itab-derived`, chromedp.ByQuery))
	b.waitFor("derived", `!document.querySelector('#ipanel-derived').hidden`)
	const selected = `document.querySelector('#instance-panel .itabs [aria-selected="true"]')?.dataset.tab + ' ' + [...document.querySelectorAll('#instance-panel .ipanel')].filter((p) => !p.hidden).map((p) => p.id).join()`
	peer := eval[string](b, `(() => { const a = [...document.querySelectorAll('#instance-panel a.peer[data-explore]')].find((a) => !a.dataset.explore.startsWith('document:')); a.click(); return a.dataset.explore; })()`)
	b.waitFor("peer", `document.querySelector('#instance-panel .instance[data-object="`+peer+`"]')`)
	b.settled()
	if got := eval[string](b, selected); strings.HasPrefix(got, "derived ") {
		t.Errorf("%s opened on the tab chosen for documents: %q", peer, got)
	}
	b.run(chromedp.Evaluate(`void document.querySelector('#instance-canvas')._cyreg.cy.getElementById('document:doc-0007').emit('tap')`, nil))
	b.waitFor("document again", `document.querySelector('#instance-panel .instance[data-object="document:doc-0007"]')`)
	b.settled()
	if got := eval[string](b, selected); got != "derived ipanel-derived" {
		t.Errorf("document reopened on %q, want derived ipanel-derived", got)
	}
	b.checkClean("tab per type")
}

// TestBrowserExploreEdgeTaps: tapping an edge works like tapping a node (it
// selects the type or opens the instance at the edge's object end) and goes
// to the relation the edge stands for in the side panel.
func TestBrowserExploreEdgeTaps(t *testing.T) {
	x := newTestExplorer(t)
	b := newBrowser(t, x)
	b.open("~/explore")
	b.waitFor("type graph", `document.querySelector('#schema-canvas')._cyreg?.cy?.edges().length > 0`)
	const marked = `[...document.querySelectorAll('.rel-target')].map((el) => el.id).sort().join()`

	// Type graph: an edge into document, merging the most relations.
	want := eval[string](b, `(() => {
		const e = document.querySelector('#schema-canvas')._cyreg.cy.edges('[target = "document"]').sort((a, b) => b.data('relations').length - a.data('relations').length)[0];
		e.emit('tap');
		return e.data('relations').map((r) => 'type-rel-' + r).sort().join();
	})()`)
	if !strings.Contains(want, ",") {
		t.Logf("type edge stands for one relation only: %s", want)
	}
	b.waitFor("document panel", `document.querySelector('#type-panel [data-selected-type="document"]')`)
	b.settled()
	b.waitFor("type relations marked", marked+` === '`+want+`'`)
	if got := eval[string](b, `document.activeElement?.id`); !strings.HasPrefix(got, "type-rel-") {
		t.Errorf("focus on %q, want the first marked relation", got)
	}

	// Instance graph: an edge into the open instance goes to its relation.
	b.open("~/explore?type=document&id=doc-0007")
	b.waitFor("instance panel", `document.querySelector('#instance-panel .instance[data-object="document:doc-0007"]')`)
	b.settled()
	const inst = `document.querySelector('#instance-canvas')._cyreg.cy`
	rel := eval[string](b, `(() => {
		const e = `+inst+`.edges('[target = "document:doc-0007"]').filter((e) => e.source().data('kind') !== 'count')[0];
		if (!e) return '';
		e.emit('tap');
		return e.data('relation');
	})()`)
	if rel == "" {
		t.Fatal("no edge into document:doc-0007 on the instance graph")
	}
	b.waitFor("instance relation marked", marked+` === 'rel-`+rel+`'`)

	// With another instance open, an edge into document:doc-0007 opens it again.
	peer := eval[string](b, `(() => {
		const e = `+inst+`.edges('[target = "document:doc-0007"][relation = "`+rel+`"]').filter((e) => e.source().data('kind') !== 'count')[0];
		e.source().emit('tap');
		return e.source().id();
	})()`)
	b.waitFor("peer instance", `document.querySelector('#instance-panel .instance[data-object="`+peer+`"]')`)
	b.settled()
	b.run(chromedp.Evaluate(`void `+inst+`.edges('[target = "document:doc-0007"][relation = "`+rel+`"]')[0].emit('tap')`, nil))
	b.waitFor("instance again", `document.querySelector('#instance-panel .instance[data-object="document:doc-0007"]')`)
	b.settled()
	b.waitFor("relation marked again", marked+` === 'rel-`+rel+`'`)
	b.checkClean("edge taps")
}

// TestBrowserExploreRelationLinks: a link from a computed relation to another
// relation switches tabs if needed, brings the target into view, marks it
// (one at a time), and moves focus to it.
func TestBrowserExploreRelationLinks(t *testing.T) {
	x := newTestExplorer(t)
	b := newBrowser(t, x)
	b.open("~/explore?type=document&id=doc-0007")
	b.waitFor("instance panel", `document.querySelector('#instance-panel .instance[data-object="document:doc-0007"]')`)
	b.settled()
	b.run(chromedp.Click(`#itab-derived`, chromedp.ByQuery))
	b.waitFor("derived tab", `!document.querySelector('#ipanel-derived').hidden`)
	// Links from computed relations to relations shown on the Edges tab.
	links := eval[[]string](b, `[...new Set([...document.querySelectorAll('#ipanel-derived a.anchor[href^="#rel-"]')].map((a) => a.getAttribute('href'))
		.filter((h) => document.querySelector(h)?.closest('#ipanel-edges')))]`)
	if len(links) < 2 {
		t.Fatalf("need two links into the Edges tab, found %v", links)
	}
	visible := func(id string) string {
		return `(() => { const el = document.getElementById('` + id + `'); const r = el.getBoundingClientRect();
			const hit = document.elementFromPoint(r.left + Math.min(r.width / 2, 40), r.top + Math.min(r.height / 2, 10));
			return !!hit && el.contains(hit); })()`
	}
	for i, href := range links[:2] {
		id := strings.TrimPrefix(href, "#")
		if i > 0 {
			b.run(chromedp.Click(`#itab-derived`, chromedp.ByQuery))
			b.waitFor("derived tab", `!document.querySelector('#ipanel-derived').hidden`)
		}
		b.run(chromedp.Click(`#ipanel-derived a.anchor[href="`+href+`"]`, chromedp.ByQuery))
		b.waitFor(id+" marked", `document.getElementById('`+id+`').classList.contains('rel-target')`)
		if eval[bool](b, `document.querySelector('#ipanel-edges').hidden`) {
			t.Errorf("%s: the Edges tab was not shown", id)
		}
		if n := eval[int](b, `document.querySelectorAll('.rel-target').length`); n != 1 {
			t.Errorf("%s: %d relations marked, want 1", id, n)
		}
		if !eval[bool](b, `document.activeElement?.id === '`+id+`'`) {
			t.Errorf("%s: focus is not on the relation", id)
		}
		b.waitFor(id+" in view", visible(id))
	}
	// Clicking another relation (or tabbing into one) moves the mark to it.
	other := eval[string](b, `document.querySelector('#ipanel-edges .relrow:not(.rel-target)').id`)
	b.run(chromedp.Click(`#`+other+` .rr-head`, chromedp.ByQuery))
	if got := eval[string](b, `[...document.querySelectorAll('.rel-target')].map((e) => e.id).join()`); got != other {
		t.Errorf("after clicking %s, marked = %q", other, got)
	}
	b.run(chromedp.Click(`#itab-derived`, chromedp.ByQuery))
	b.waitFor("derived tab", `!document.querySelector('#ipanel-derived').hidden`)
	derived := eval[string](b, `(() => { const li = document.querySelector('#ipanel-derived li[id^="rel-"] .whohas-d > summary'); li.focus(); return li.closest('[id^="rel-"]').id; })()`)
	if got := eval[string](b, `[...document.querySelectorAll('.rel-target')].map((e) => e.id).join()`); got != derived {
		t.Errorf("after tabbing into %s, marked = %q", derived, got)
	}
	b.checkClean("relation links")
}

// TestBrowserCheckTuplesRead: the result says how many tuples the walk read,
// and its "?" breaks the calls down and says what is not counted.
func TestBrowserCheckTuplesRead(t *testing.T) {
	x := newTestExplorer(t)
	b := newBrowser(t, x)
	b.open(`~/check?user=user:eng-002&relation=can_view&object=document:doc-0007&context=` +
		`%7B%22current_time%22%3A%222026-11-24T12%3A00%3A00Z%22%2C%22user_ip%22%3A%2210.1.2.3%22%7D`)
	b.waitFor("result", `document.querySelector('#check-result .verdict')`)
	stats := eval[string](b, `document.querySelector('.verdict .stats').textContent.replace(/\s+/g, ' ')`)
	m := regexp.MustCompile(`(\d+) tuples? read · (\d+) walk \+ \d+ verify calls`).FindStringSubmatch(stats)
	if m == nil || m[1] == "0" {
		t.Fatalf("no tuples-read count in %q", stats)
	}
	b.run(chromedp.Click(`.verdict [aria-controls="h-calls"]`, chromedp.ByQuery))
	b.waitFor("calls help", `!document.querySelector('#h-calls').hidden`)
	help := eval[string](b, `document.querySelector('#h-calls').textContent.replace(/\s+/g, ' ')`)
	for _, want := range []string{"which returned " + m[1] + " tuple", "Check call", "ListObjects call", "not counted"} {
		if !strings.Contains(help, want) {
			t.Errorf("calls help lacks %q:\n%s", want, help)
		}
	}
	if strings.Contains(stats, "cache") {
		t.Errorf("the first check mentions the cache: %q", stats)
	}
	// The kinds add up to the walk's calls.
	sum := eval[int](b, `[...document.querySelector('#h-calls').textContent.matchAll(/(\d+) (?:Read|Check|ListObjects) calls?\b/g)].slice(0, 3).reduce((s, x) => s + +x[1], 0)`)
	if fmt.Sprint(sum) != m[2] {
		t.Errorf("call kinds add up to %d, the result says %s walk calls:\n%s", sum, m[2], help)
	}

	// The same check again (within the read cache's lifetime): the reads that
	// the explorer's cache answered are counted apart from the store's.
	b.open(`~/check?user=user:eng-002&relation=can_view&object=document:doc-0007&context=` +
		`%7B%22current_time%22%3A%222026-11-24T12%3A00%3A00Z%22%2C%22user_ip%22%3A%2210.1.2.3%22%7D`)
	b.waitFor("result again", `document.querySelector('#check-result .verdict')`)
	stats = eval[string](b, `document.querySelector('.verdict .stats').textContent.replace(/\s+/g, ' ')`)
	c := regexp.MustCompile(`(\d+) tuples? read \((\d+) from the explorer's cache\)`).FindStringSubmatch(stats)
	if c == nil {
		t.Fatalf("the repeated check does not say what came from the cache: %q", stats)
	}
	total, cached := 0, 0
	fmt.Sscan(c[1], &total)
	fmt.Sscan(c[2], &cached)
	if cached == 0 || cached > total {
		t.Errorf("%d of %d tuples from the cache", cached, total)
	}
	help = eval[string](b, `document.querySelector('#h-calls').textContent.replace(/\s+/g, ' ')`)
	if want := fmt.Sprintf("%d came from the store", total-cached); !strings.Contains(help, want) || !strings.Contains(help, "30s") {
		t.Errorf("calls help lacks %q or the cache lifetime:\n%s", want, help)
	}
	b.checkClean("tuples read")
}

// TestBrowserExploreInstanceFocus: with an instance open, the side panel
// shows just the instance; "Type …" toggles the type section, "Pick
// another…" shows it with the picker open, and choosing a type in the graph
// brings it back. "who has it?" is a row action that keyboard focus reveals.
func TestBrowserExploreInstanceFocus(t *testing.T) {
	x := newTestExplorer(t)
	b := newBrowser(t, x)
	b.open("~/explore?type=document&id=doc-0007")
	b.waitFor("instance panel", `document.querySelector('#instance-panel .instance[data-object="document:doc-0007"]')`)
	b.settled()
	typesShown := `getComputedStyle(document.querySelector('#explore-types')).display !== 'none'`
	expanded := `document.querySelector('[data-act="show-type"]').getAttribute('aria-expanded')`
	if eval[bool](b, typesShown) || eval[string](b, expanded) != "false" {
		t.Error("with an instance open the type section should be hidden")
	}
	b.run(chromedp.Click(`[data-act="show-type"]`, chromedp.ByQuery))
	if !eval[bool](b, typesShown) || eval[string](b, expanded) != "true" {
		t.Error(`"Type document" did not show the type section`)
	}
	b.run(chromedp.Click(`[data-act="show-type"]`, chromedp.ByQuery))
	if eval[bool](b, typesShown) {
		t.Error(`"Type document" did not hide the type section again`)
	}
	b.run(chromedp.Click(`[data-act="change-instance"]`, chromedp.ByQuery))
	b.waitFor("picker", `document.activeElement?.matches('#type-panel .picker-q')`)
	if !eval[bool](b, typesShown) {
		t.Error(`"Pick another…" did not show the type section`)
	}

	// Opening an instance hides it again; choosing a type in the graph shows it.
	b.run(chromedp.Evaluate(`document.activeElement.blur()`, nil))
	b.open("~/explore?type=document&id=doc-0007")
	b.waitFor("instance panel", `document.querySelector('#instance-panel .instance[data-object="document:doc-0007"]')`)
	b.settled()
	if eval[bool](b, typesShown) {
		t.Error("opening an instance should hide the type section")
	}
	b.run(chromedp.Evaluate(`void document.querySelector('#schema-canvas')._cyreg.cy.getElementById('folder').emit('tap')`, nil))
	b.waitFor("type section on type choice", typesShown)

	// "who has it?" is on every relation, in both tabs; keyboard focus shows it.
	if n := eval[int](b, `document.querySelectorAll('#ipanel-edges .relrow > .whohas-d, #ipanel-derived .derived > li > .whohas-d').length`); n != 10 {
		t.Errorf("%d relations have a who-has-it action, want 10", n)
	}
	const sum = `#ipanel-edges .relrow > .whohas-d > summary`
	if eval[bool](b, `matchMedia('(hover: hover)').matches`) && eval[string](b, `getComputedStyle(document.querySelector('`+sum+`')).opacity`) != "0" {
		t.Error("the row action should stay out of sight until hover or focus")
	}
	b.run(chromedp.Focus(sum, chromedp.ByQuery), chromedp.Sleep(200*time.Millisecond))
	if got := eval[string](b, `getComputedStyle(document.querySelector('`+sum+`')).opacity`); got != "1" {
		t.Errorf("a focused row action has opacity %s", got)
	}
	b.checkClean("instance focus")
}

// The "Why" list tells stored tuples apart from what the model concludes.
func TestBrowserCheckWhyKinds(t *testing.T) {
	x := newTestExplorer(t)
	b := newBrowser(t, x)
	b.open(`~/check?user=user:eng-002&relation=can_view&object=document:doc-0007&context=` +
		`%7B%22current_time%22%3A%222026-11-24T12%3A00%3A00Z%22%2C%22user_ip%22%3A%2210.1.2.3%22%7D`)
	b.waitFor("result", `document.querySelector('#check-result .verdict .facts')`)
	sum := eval[string](b, `document.querySelector('.facts > summary').textContent.trim()`)
	m := regexp.MustCompile(`^Why: (\d+) tuples?, (\d+) rules?$`).FindStringSubmatch(sum)
	if m == nil {
		t.Fatalf("summary %q does not count tuples and rules", sum)
	}
	counts := eval[string](b, `['tuple', 'rule'].map((k) => document.querySelectorAll('.facts li.f-' + k).length).join(' ')`)
	if counts != m[1]+" "+m[2] {
		t.Errorf("summary %q, list has %s (tuples rules)", sum, counts)
	}
	// A tuple-to-userset step is a stored tuple followed by the rule using it.
	pair := eval[string](b, `(() => { const li = [...document.querySelectorAll('.facts li')];
		const i = li.findIndex((l) => / counts as /.test(l.textContent));
		return i > 0 ? li[i - 1].dataset.kind + ' ' + li[i].dataset.kind : ''; })()`)
	if pair != "tuple rule" {
		t.Errorf("inherited step reads %q, want a tuple then a rule", pair)
	}
	// The kinds look different, not only by their label.
	weights := eval[string](b, `['tuple', 'rule'].map((k) => getComputedStyle(document.querySelector('.facts li.f-' + k + ' .ft')).fontWeight).join(' ')`)
	if f := strings.Fields(weights); len(f) != 2 || f[0] == f[1] {
		t.Errorf("tuple and rule text share a style: %q", weights)
	}
	b.checkClean("why kinds")
}

// "show N more" under a who-has list extends the same list, which then
// scrolls as one, and can be folded back.
func TestBrowserWhoHasShowMore(t *testing.T) {
	x := newTestExplorer(t)
	b := newBrowser(t, x)
	b.open("~/explore?type=group&id=eng")
	b.waitFor("instance panel", `document.querySelector('#instance-panel .instance[data-object="group:eng"]')`)
	b.settled()
	b.run(chromedp.Click(`#rel-member .whohas-d > summary`, chromedp.ByQuery))
	b.run(chromedp.Click(`#rel-member form.whohas button[type="submit"]`, chromedp.ByQuery))
	b.waitFor("holders", `document.querySelector('#rel-member .lu-list li')`)
	const lists = `document.querySelectorAll('#rel-member .lu ul').length`
	if n := eval[int](b, lists); n != 1 {
		t.Fatalf("who-has renders %d lists, want 1", n)
	}
	visible := `[...document.querySelectorAll('#rel-member .lu-list > li')].filter((li) => !li.hidden).length`
	total := eval[int](b, `document.querySelectorAll('#rel-member .lu-list > li').length`)
	if v := eval[int](b, visible); v != 20 || total <= v {
		t.Fatalf("before: %d of %d shown, want the first 20 of more", v, total)
	}
	b.run(chromedp.Click(`#rel-member [data-act="lu-more"]`, chromedp.ByQuery))
	if v := eval[int](b, visible); v != total {
		t.Errorf("after show more: %d of %d shown", v, total)
	}
	scroll := eval[string](b, `(() => { const l = document.querySelector('#rel-member .lu-list');
		return getComputedStyle(l).overflowY + ' ' + (l.scrollHeight > l.clientHeight); })()`)
	if scroll != "auto true" {
		t.Errorf("the extended list does not scroll as one: %q", scroll)
	}
	if s := eval[string](b, `document.querySelector('#rel-member [data-act="lu-more"]').getAttribute('aria-expanded')`); s != "true" {
		t.Errorf("aria-expanded = %q after show more", s)
	}
	b.run(chromedp.Click(`#rel-member [data-act="lu-more"]`, chromedp.ByQuery))
	if v := eval[int](b, visible); v != 20 {
		t.Errorf("after show fewer: %d shown", v)
	}
	b.checkClean("who has")
}

// The side panel follows the canvas tab: back on the type graph it shows the
// types, not the instance that was open; the instance graph brings it back.
func TestBrowserExploreSideFollowsCanvas(t *testing.T) {
	x := newTestExplorer(t)
	b := newBrowser(t, x)
	b.open("~/explore?type=group&id=eng")
	b.waitFor("instance panel", `document.querySelector('#instance-panel .instance[data-object="group:eng"]')`)
	b.settled()
	const visible = `((s) => { const el = document.querySelector(s); return !!el && el.offsetParent !== null; })`
	state := func() string {
		return eval[string](b, `['#instance-panel .instance', '#explore-types'].map((s) => `+visible+`(s) ? 'shown' : 'hidden').join(' ')`)
	}
	if got := state(); got != "shown hidden" {
		t.Fatalf("with an instance open: instance, types = %q", got)
	}
	b.run(chromedp.Click(`#tab-schema`, chromedp.ByQuery))
	if got := state(); got != "hidden shown" {
		t.Errorf("on the type graph: instance, types = %q", got)
	}
	// Choosing a type there still shows its details.
	b.run(chromedp.Evaluate(`void document.querySelector('#schema-canvas')._cyreg.cy.getElementById('folder').emit('tap')`, nil))
	b.waitFor("folder panel", `document.querySelector('#type-panel [data-selected-type="folder"]')`)
	b.run(chromedp.Click(`#tab-instances`, chromedp.ByQuery))
	if got := state(); got != "shown hidden" {
		t.Errorf("back on the instance graph: instance, types = %q", got)
	}
	b.checkClean("side follows canvas")
}

// Opening another instance keeps its selected tab marked: htmx's settle step
// puts back the server's class attributes on the tabs (same ids as before).
func TestBrowserExploreNextInstanceTabMarked(t *testing.T) {
	x := newTestExplorer(t)
	b := newBrowser(t, x)
	b.open("~/explore?type=document&id=doc-0007")
	b.waitFor("instance panel", `document.querySelector('#instance-panel .instance[data-object="document:doc-0007"]')`)
	b.settled()
	marked := `[...document.querySelectorAll('#instance-panel .itabs [role="tab"]')].map((t) => (getComputedStyle(t).borderBottomColor !== 'rgba(0, 0, 0, 0)' ? '+' : '-') + t.dataset.tab + (t.getAttribute('aria-selected') === 'true' ? '*' : '')).join(' ')`
	first := eval[string](b, marked)
	if !strings.HasPrefix(first, "+edges* ") {
		t.Fatalf("first instance tabs: %q", first)
	}
	b.run(chromedp.Evaluate(`void document.querySelector('#instance-panel a.peer[data-explore]').click()`, nil))
	b.waitFor("next instance", `document.querySelector('#instance-panel .instance:not([data-object="document:doc-0007"])')`)
	b.settled()
	b.run(chromedp.Sleep(200 * time.Millisecond))
	if got := eval[string](b, marked); !strings.Contains(got, "+") || strings.Count(got, "+") != 1 || !regexp.MustCompile(`\+\w+\*`).MatchString(got) {
		t.Errorf("next instance tabs: %q, want the selected tab marked", got)
	}
	b.checkClean("next instance tab")
}

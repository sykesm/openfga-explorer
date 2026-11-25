package web

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
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

	// Choosing a type loads its panel and opens the instance picker.
	b.run(chromedp.Click(`.type-chip[data-type="group"]`, chromedp.ByQuery))
	b.waitFor("type panel", `document.querySelector('#type-panel [data-selected-type="group"]')`)
	b.waitFor("picker focus", `document.activeElement?.matches('#type-panel .picker-q')`)

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
	b.waitFor("combobox", `document.querySelector('.picker[data-field="object"] .tcombo-q')`)
	obj := `.picker[data-field="object"] .tcombo-q`
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

	// Explore offers the type finder, matching relations too.
	b.open("~/explore")
	b.waitFor("finder", `document.querySelector('.type-q')`)
	b.key(`.type-q`, "approve")
	b.waitFor("results", `[...document.querySelectorAll('#type-results [role="option"]')].map((o) => o.dataset.type).join() === 'kind_03,kind_10,kind_17'`)
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
	b.waitFor("results", `[...document.querySelectorAll('#type-results [role="option"]')].map((o) => o.dataset.type).join() === 'kind_42'`)
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

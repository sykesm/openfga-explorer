package web

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/sykesm/openfga-explorer/internal/fgatest"
)

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

	// Each control sits with what it affects: the stats in the verdict, the
	// walk order over the graph, and "Add to scenario" with its option in the
	// verdict's Save popover.
	if !eval[bool](b, `!!document.querySelector('.verdict .vtop .stats') && !!document.querySelector('#trace-graph .walkbar .order-seg')`) {
		t.Error("stats should be in the verdict and the walk order over the graph")
	}
	if !eval[bool](b, `!!document.querySelector('.verdict .save-menu .capture [data-act="capture"]') && !!document.querySelector('.verdict .save-menu .capture [data-act="capture-mask"]')`) {
		t.Error("the scenario option is not grouped with its button in the Save popover")
	}
	// The order switch shows the current order; choosing it again does nothing.
	const pressed = `document.querySelector('.order-seg [aria-pressed="true"]')?.dataset.order`
	if got := eval[string](b, pressed); got != "guided" {
		t.Errorf("order switch shows %q, want guided", got)
	}
	b.run(chromedp.Evaluate(`document.querySelector('.verdict').dataset.stale = '1'`, nil),
		chromedp.Click(`.order-seg [data-order="guided"]`, chromedp.ByQuery), chromedp.Sleep(300*time.Millisecond))
	if eval[string](b, `document.querySelector('.verdict').dataset.stale || ''`) != "1" {
		t.Error("choosing the current order re-ran the check")
	}
	// Model order re-runs in the other evaluation order.
	b.run(chromedp.Click(`.order-seg [data-order="model"]`, chromedp.ByQuery))
	b.waitFor("model order", `document.querySelector('.order-note')?.textContent.includes('order the model lists')`)
	if got := eval[string](b, pressed); got != "model" {
		t.Errorf("order switch shows %q, want model", got)
	}
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

// TestBrowserInspector covers the check view's reading pane and details:
// the Why / Text trace / Who else? tabs, the details card over the graph,
// collapsing the pane, and phones (the pane above the graph).
func TestBrowserInspector(t *testing.T) {
	x := newTestExplorer(t)
	b := newBrowser(t, x)
	b.open(`~/check?user=user:eng-002&relation=can_view&object=document:doc-0007&context=` +
		`%7B%22current_time%22%3A%222026-11-24T12%3A00%3A00Z%22%2C%22user_ip%22%3A%2210.1.2.3%22%7D`)
	b.waitFor("graph", `document.querySelector('#trace-canvas')?._cyreg?.cy?.nodes().length > 0`)
	b.settled()
	// The pane starts on Why, beside the graph (left of it).
	if got := eval[string](b, `[document.querySelector('#inspector [role="tab"][aria-selected="true"]')?.dataset.tab,
		document.querySelector('#inspector').getBoundingClientRect().right <= document.querySelector('#trace-graph').getBoundingClientRect().left + 1].join(' ')`); got != "why true" {
		t.Errorf("reading pane: %q (tab, left of the graph)", got)
	}
	const card = `getComputedStyle(document.querySelector('#node-detail')).display !== 'none'`
	if !eval[bool](b, `!!document.querySelector('#trace-graph #node-detail .nd-empty') && !(`+card+`)`) {
		t.Error("details should start empty and hidden, as a card over the graph")
	}

	// Selecting a node shows the card; Escape clears and hides it.
	b.run(chromedp.Evaluate(`void document.querySelector('#trace-canvas')._cyreg.cy.nodes('.root').emit('tap')`, nil))
	b.waitFor("details", `document.querySelector('#node-detail h3')?.textContent.includes('document:doc-0007') && `+card)
	b.run(chromedp.Evaluate(`document.activeElement.blur()`, nil), chromedp.KeyEvent(kb.Escape))
	b.waitFor("cleared", `document.querySelector('#node-detail .nd-empty') && !(`+card+`)`)

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

	// Collapsing gives the graph the width; selecting a node leaves it closed
	// (details are on the graph); the rail reopens it.
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
	if !eval[bool](b, `document.querySelector('.trace-views').classList.contains('panel-collapsed')`) {
		t.Error("selecting a node reopened the reading pane")
	}
	b.run(chromedp.Click(`.panel-rail`, chromedp.ByQuery))
	b.waitFor("reopened", `!document.querySelector('.trace-views').classList.contains('panel-collapsed')`)

	// Phones: a switch shows the pane or the graph (TestBrowserCheckPhone);
	// the pane is part of the page, not floating over it.
	b.run(emulation.SetDeviceMetricsOverride(390, 844, 2, true))
	b.waitFor("switch", `getComputedStyle(document.querySelector('.view-switch')).display !== 'none'`)
	if pos := eval[string](b, `getComputedStyle(document.querySelector('.inspector')).position`); pos == "fixed" {
		t.Error("the reading pane should not float over the page on phones")
	}
	b.run(emulation.ClearDeviceMetricsOverride())
	b.checkClean("inspector")
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
	st, err := srv.CreateStore(t.Context(), "folded", model, tuples)
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
// are as tall as the window (filling it once scrolled to), and
// resizing the window (or the side panel) keeps a zoom the person set.
func TestBrowserCheckFillsWindow(t *testing.T) {
	x := newTestExplorer(t)
	b := newBrowser(t, x)
	b.run(emulation.SetDeviceMetricsOverride(2560, 1400, 1, false))
	b.open(`~/check?user=user:eng-002&relation=can_view&object=document:doc-0007&context=` +
		`%7B%22current_time%22%3A%222026-11-24T12%3A00%3A00Z%22%2C%22user_ip%22%3A%2210.1.2.3%22%7D`)
	b.waitFor("graph", `document.querySelector('#trace-canvas')?._cyreg?.cy?.nodes().length > 0`)
	b.waitFor("collapsed form", `document.querySelector('#check-form').classList.contains('collapsed')`)
	// As tall as the window (less a margin), so scrolled to it fills the window.
	const fills = `Math.abs(document.querySelector('#trace-graph').getBoundingClientRect().height - (innerHeight - 24)) <= 2`
	b.waitFor("graph as tall as the window", fills)
	if d := eval[float64](b, `Math.abs(document.querySelector('#trace-graph').getBoundingClientRect().bottom - document.querySelector('#inspector').getBoundingClientRect().bottom)`); d > 2 {
		t.Errorf("side panel and graph end %vpx apart", d)
	}
	const cy = `document.querySelector('#trace-canvas')._cyreg.cy`
	// The graph itself follows its box (it settles just after drawing).
	b.waitFor("graph filling its box", cy+`.height() > 900 && `+cy+`.height() === document.querySelector('#trace-canvas').clientHeight`)
	// It all fits here: no note that part of it is out of view.
	b.run(chromedp.Sleep(300 * time.Millisecond))
	if !eval[bool](b, `document.querySelector('.graph-note').hidden`) {
		t.Error("the out-of-view note shows while the whole graph is in view")
	}
	// Take charge of the view, then resize the window and toggle the panel.
	b.run(chromedp.Click(`.player [data-act="step"]`, chromedp.ByQuery), chromedp.Evaluate(`void `+cy+`.zoom(1.7)`, nil))
	b.run(emulation.SetDeviceMetricsOverride(2000, 1100, 1, false))
	b.waitFor("graph resized", fills)
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
	b.settled()
	b.run(chromedp.Click(`#rtab-trace`, chromedp.ByQuery))
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
	const btn = `document.querySelector('#trace-graph [data-act="show-grants"]')`
	n := eval[int](b, `new Set(`+cy+`.edges('.grant').map((e) => e.data('tuple'))).size`)
	if n == 0 {
		t.Fatal("no granting tuples marked in the graph")
	}
	if got := eval[string](b, btn+`?.textContent.trim() || ''`); got != fmt.Sprintf("%d granting tuple%s", n, map[bool]string{true: "", false: "s"}[n == 1]) {
		t.Errorf("button %q, graph marks %d tuples", got, n)
	}

	b.run(chromedp.Click(`#trace-graph [data-act="show-grants"]`, chromedp.ByQuery))
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

	b.run(chromedp.Click(`#trace-graph [data-act="show-grants"]`, chromedp.ByQuery))
	if !eval[bool](b, btn+`.getAttribute('aria-pressed') === 'false' && `+cy+`.edges('.grant-on, .tuple-on').length === 0 && !document.querySelector('.tl.grant-line, .tl.tuple-line, .grant-list')`) {
		t.Error("switching off left highlights behind")
	}

	// Back to the shortest explanation.
	b.run(chromedp.Click(`#trace-graph [data-act="order"][data-order="guided"]`, chromedp.ByQuery))
	b.waitFor("shortest explanation", `document.querySelector('.order-seg [data-order="guided"]')?.getAttribute('aria-pressed') === 'true'`)
	if !strings.Contains(eval[string](b, `location.search`), "order=guided") {
		t.Error("URL does not record the order")
	}
	b.checkClean("granting tuples")
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

// TestBrowserTwoTraces: each <fga-trace> works from its own parts, so a
// second one on the page draws itself, and its controls drive its own replay
// without touching the first.
func TestBrowserTwoTraces(t *testing.T) {
	x := newTestExplorer(t)
	b := newBrowser(t, x)
	b.open("~/check")
	b.run(chromedp.Evaluate(`localStorage.setItem('fgax.autoplay', 'false')`, nil))
	b.open(`~/check?user=user:eng-002&relation=can_view&object=document:doc-0007`)
	b.waitFor("result", `document.querySelector('fga-trace .player .step-count')?.textContent`)
	b.settled()
	b.run(chromedp.Evaluate(`{
		const c = document.createElement('div');
		c.id = 'second';
		// A copy of the trace as the server sent it: without the drawn graph.
		const copy = document.querySelector('fga-trace').cloneNode(true);
		const canvas = copy.querySelector('#trace-canvas');
		canvas.replaceChildren();
		canvas.removeAttribute('style');
		canvas.className = 'canvas';
		c.append(copy);
		document.querySelector('#check-result').after(c);
	}`, nil))
	b.waitFor("second trace drawn", `document.querySelector('#second fga-trace')?.view?.cy.nodes().length > 0`)
	const counts = `[...document.querySelectorAll('fga-trace .player .step-count')].map((e) => e.textContent)`
	before := eval[[]string](b, counts)
	if len(before) != 2 || before[0] != before[1] {
		t.Fatalf("step counts %v: want two finished replays", before)
	}
	// Step the second trace from its start: only its count changes.
	b.run(chromedp.Click(`#second .player [data-act="play"]`, chromedp.ByQuery))
	b.run(chromedp.Click(`#second .player [data-act="step"]`, chromedp.ByQuery))
	after := eval[[]string](b, counts)
	if after[0] != before[0] || after[1] == before[1] {
		t.Errorf("step counts %v → %v: the second trace's controls should drive only its own replay", before, after)
	}
	b.checkClean("two traces")
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

// Re-running a check (e.g. "Show model order") and replaying it keeps the
// layout still: the new views take the old sizes in their first frame, and
// the player bar and status keep their size whether playing or finished.
func TestBrowserTraceRerunStable(t *testing.T) {
	x := newTestExplorer(t)
	b := newBrowser(t, x)
	b.run(emulation.SetEmulatedMedia().WithFeatures([]*emulation.MediaFeature{{Name: "prefers-reduced-motion", Value: "no-preference"}}))
	b.open(`~/check?user=user:eng-002&relation=can_view&object=document:doc-0007&context=` +
		`%7B%22current_time%22%3A%222026-11-24T12%3A00%3A00Z%22%2C%22user_ip%22%3A%2210.1.2.3%22%7D`)
	b.waitFor("result", `document.querySelector('#check-result .verdict')`)
	b.run(chromedp.Evaluate(`document.querySelector('.player [data-act="final"]').click()`, nil), chromedp.Sleep(300*time.Millisecond))
	b.run(chromedp.Evaluate(`window.__shifts = []; window.__sizes = {};
		new PerformanceObserver((l) => l.getEntries().forEach((e) => { if (!e.hadRecentInput) __shifts.push(e.value.toFixed(3) + ' ' +
			(e.sources || []).map((s) => s.node?.id || s.node?.className || s.node?.nodeName).join(', ')); })).observe({ type: 'layout-shift' });
		const watch = () => { for (const sel of ['#rpanel-trace', '.player', '.player-line', '#trace-canvas']) {
				const el = document.querySelector(sel); if (!el) continue;
				(__sizes[sel] ||= new Set()).add(Math.round(el.getBoundingClientRect().height)); }
			if (!window.__stop) requestAnimationFrame(watch); };
		requestAnimationFrame(watch);
		document.querySelector('[data-act="order"][data-order="model"]').click();`, nil))
	b.waitFor("replay", `/step [1-9]/.test(document.querySelector('.player .step-count')?.textContent || '') && document.querySelector('.player [data-act="play"]').textContent.includes('Pause')`)
	b.run(chromedp.Sleep(300*time.Millisecond), chromedp.Evaluate(`document.querySelector('.player [data-act="final"]').click()`, nil), chromedp.Sleep(400*time.Millisecond))
	var got struct {
		Shifts []string
		Sizes  map[string][]int
	}
	b.run(chromedp.Evaluate(`window.__stop = true; ({ Shifts: __shifts, Sizes: Object.fromEntries(Object.entries(__sizes).map(([k, v]) => [k, [...v]])) })`, &got))
	if len(got.Shifts) > 0 {
		t.Errorf("layout shifted while re-running and replaying: %q", got.Shifts)
	}
	for sel, hs := range got.Sizes {
		lo, hi := hs[0], hs[0]
		for _, h := range hs {
			lo, hi = min(lo, h), max(hi, h)
		}
		if hi-lo > 1 {
			t.Errorf("%s changed height while re-running and replaying: %v", sel, hs)
		}
	}
	b.checkClean("rerun")
}

// The "Why" list reads from the user up to the object, one step per line,
// each with the tuples and model rules it rests on.
func TestBrowserCheckWhyKinds(t *testing.T) {
	x := newTestExplorer(t)
	b := newBrowser(t, x)
	b.open(`~/check?user=user:eng-002&relation=can_view&object=document:doc-0007&context=` +
		`%7B%22current_time%22%3A%222026-11-24T12%3A00%3A00Z%22%2C%22user_ip%22%3A%2210.1.2.3%22%7D`)
	b.waitFor("result", `document.querySelector('#check-result .facts')`)
	b.run(chromedp.Evaluate(`void 0`, nil))
	sum := eval[string](b, `document.querySelector('.facts .facts-sum').textContent.trim()`)
	m := regexp.MustCompile(`^(\d+) steps?, from (\d+) tuples?$`).FindStringSubmatch(sum)
	if m == nil {
		t.Fatalf("summary %q does not count steps and tuples", sum)
	}
	counts := eval[string](b, `document.querySelectorAll('.facts ol.steps[data-reading="up"] > li.step').length + ' ' +
		new Set([...document.querySelectorAll('.facts ol.steps[data-reading="up"] .ev-tuple code.tup')].map((c) => c.textContent)).size`)
	if counts != m[1]+" "+m[2] {
		t.Errorf("summary %q, list has %s (steps tuples)", sum, counts)
	}
	steps := eval[[]string](b, `[...document.querySelectorAll('.facts ol.steps[data-reading="up"] > li.step > .ft')].map((e) => e.textContent)`)
	if len(steps) < 2 || steps[0] != "user:eng-002 has member on group:eng" || !strings.Contains(steps[len(steps)-1], "has can_view on document:doc-0007") {
		t.Errorf("steps do not run from the user to the object: %q", steps)
	}
	// step finds the step whose text matches, and runs expr on it (as s).
	step := func(re, expr string) string {
		return eval[string](b, `(() => { const s = [...document.querySelectorAll('.facts ol.steps[data-reading="up"] > li.step')]
			.find((l) => new RegExp(`+"`"+re+"`"+`).test(l.querySelector('.ft').textContent));
			return s ? String(`+expr+`) : 'no step'; })()`)
	}
	// Inheriting through a parent rests on the parent tuple and the rule.
	if got := step(`has viewer on folder:engineering`, `[...s.querySelectorAll('.ev')].map((e) => e.dataset.kind).join(' ')`); got != "tuple rule" {
		t.Errorf("inherited step rests on %q, want a tuple and a rule", got)
	}
	// A tuple is shown as stored, Zanzibar style.
	if got := step(`has viewer on folder:root`, `s.querySelector('.ev-tuple code.tup')?.textContent`); got != "folder:root#viewer@group:eng#member" {
		t.Errorf("tuple shows %q", got)
	}
	// The first mention of a relation shows its definition, with the part
	// used set off; later mentions show only that part.
	if got := step(`has viewer on document:doc-0007`, `s.querySelector('.ev-rule .fref b')?.textContent`); got != "viewer from parent" {
		t.Errorf("document#viewer highlights %q", got)
	}
	if got := step(`has viewer on folder:design-docs`, `s.querySelector('.ev-rule .fref code')?.textContent`); got != "= … viewer from parent" {
		t.Errorf("a later folder#viewer mention shows %q", got)
	}
	if got := step(`has “member from org” on document:doc-0007`, `s.querySelector('.ev-rule .fref')?.textContent.trim()`); got != "document#can_view = (viewer and member from org) but not blocked from org" {
		t.Errorf("document#can_view mention shows %q", got)
	}
	// Every piece of evidence links to its relation in Explore, and every
	// chip explains its kind.
	if n := eval[int](b, `[...document.querySelectorAll('.facts ol.steps[data-reading="up"] .ev')].filter((e) => !e.querySelector('.fref a[href*="/explore?"]') || !e.querySelector('.fk[title]')).length`); n != 0 {
		t.Errorf("%d pieces of evidence without a link or chip tooltip", n)
	}
	// The step is what you read; the evidence is smaller.
	if !eval[bool](b, `parseFloat(getComputedStyle(document.querySelector('.facts ol.steps[data-reading="up"] > li.step > .ft')).fontSize) > parseFloat(getComputedStyle(document.querySelector('.facts ol.steps[data-reading="up"] .ev')).fontSize)`) {
		t.Error("the evidence should be smaller than its step")
	}
	// Every kind's chip is the same width.
	widths := eval[string](b, `[...new Set([...document.querySelectorAll('.facts ol.steps[data-reading="up"] .fk')].map((e) => e.getBoundingClientRect().width.toFixed(1)))].join(' ')`)
	if strings.Contains(widths, " ") || widths == "0.0" {
		t.Errorf("chips differ in width: %q", widths)
	}
	// The other reading: from the object down to the user, nested; the choice
	// is remembered.
	b.run(chromedp.Click(`.reading-seg [data-reading="down"]`, chromedp.ByQuery))
	down := eval[string](b, `[document.querySelector('.facts ol.steps[data-reading="up"]').hidden, document.querySelector('.facts ul.steps-down').hidden,
		document.querySelector('.facts ul.steps-down > li > .ft').textContent,
		document.querySelector('.facts ul.steps-down > li > ul.steps-down > li > .ft')?.textContent].join(' | ')`)
	if down != "true | false | user:eng-002 has can_view on document:doc-0007, because they have what it needs and do not have “blocked from org” | user:eng-002 has both viewer and “member from org” on document:doc-0007" {
		t.Errorf("object-to-user reading: %q", down)
	}
	b.open(`~/check?user=user:eng-002&relation=can_view&object=document:doc-0007&context=` +
		`%7B%22current_time%22%3A%222026-11-24T12%3A00%3A00Z%22%2C%22user_ip%22%3A%2210.1.2.3%22%7D`)
	b.waitFor("remembered reading", `document.querySelector('.facts ul.steps-down')?.hidden === false && document.querySelector('.facts ol.steps[data-reading="up"]').hidden`)
	b.checkClean("why kinds")
}

// Copy puts the verdict, the numbered "Why" steps (once, from the user up),
// the text trace and the permalink on the clipboard as Markdown.
func TestBrowserCopyMarkdown(t *testing.T) {
	x := newTestExplorer(t)
	b := newBrowser(t, x)
	b.open(`~/check?user=user:eng-002&relation=can_view&object=document:doc-0007&context=` +
		`%7B%22current_time%22%3A%222026-11-24T12%3A00%3A00Z%22%2C%22user_ip%22%3A%2210.1.2.3%22%7D`)
	b.waitFor("result", `document.querySelector('#check-result .facts')`)
	b.settled()
	b.run(chromedp.Evaluate(`window.__copied = null;
		Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: (s) => { window.__copied = s; return Promise.resolve(); } } });
		document.querySelector('.player [data-act="copy-md"]').click()`, nil))
	b.waitFor("copied", `window.__copied !== null`)
	md := eval[string](b, `window.__copied`)
	steps := eval[int](b, `document.querySelectorAll('.facts ol.steps[data-reading="up"] > li.step').length`)
	for _, want := range []string{"**✓ Allowed**:", "\nWhy:\n1. user:eng-002 has member on group:eng\n", fmt.Sprintf("\n%d. ", steps), "```text\n", "document:doc-0007#can_view", "Permalink: http"} {
		if !strings.Contains(md, want) {
			t.Errorf("copied Markdown lacks %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, fmt.Sprintf("\n%d. ", steps+1)) || strings.Contains(md, "undefined") {
		t.Errorf("copied Markdown repeats steps or has undefined parts:\n%s", md)
	}
	b.checkClean("copy markdown")
}

// Pointing at a "Why" step lights up its path in the graph; its ◎ button
// pins it and selects the node it establishes; selecting a node marks the
// steps that cover it.
func TestBrowserCheckStepGraph(t *testing.T) {
	x := newTestExplorer(t)
	b := newBrowser(t, x)
	b.open(`~/check?user=user:eng-002&relation=can_view&object=document:doc-0007&context=` +
		`%7B%22current_time%22%3A%222026-11-24T12%3A00%3A00Z%22%2C%22user_ip%22%3A%2210.1.2.3%22%7D`)
	b.waitFor("graph", `document.querySelector('#trace-canvas')?._cyreg?.cy?.nodes().length > 0 && document.querySelector('.facts')`)
	b.run(chromedp.Evaluate(`document.querySelector('.player [data-act="final"]').click(); void 0`, nil))
	const cy = `document.querySelector('#trace-canvas')._cyreg.cy`
	const li = `[...document.querySelectorAll('.facts ol.steps[data-reading="up"] > li.step')].find((l) => l.querySelector('.ft').textContent.includes('has viewer on folder:root'))`
	if n := eval[string](b, `[...document.querySelectorAll('.facts li.step:not([data-nodes]) > .ft')].map((e) => e.textContent).join(' / ')`); n != "" {
		t.Errorf("steps without graph nodes: %s", n) // a repeat ("see above") points at the graph too
	}
	// Hover: the step's nodes and the edges joining them, nothing else.
	lit := `(() => { const c = ` + cy + `; return [c.nodes('.step-on').map((n) => n.id()).sort().join(' '), c.edges('.step-on').length].join(' | '); })()`
	want := eval[string](b, `(() => { const ids = (`+li+`).dataset.nodes.split(' '); return [ids.slice().sort().join(' '), ids.length - 1].join(' | '); })()`)
	b.run(chromedp.Evaluate(`(`+li+`).querySelector('.ft').dispatchEvent(new MouseEvent('mouseover', { bubbles: true }))`, nil))
	if got := eval[string](b, lit); got != want {
		t.Errorf("hovering lights %q, want %q", got, want)
	}
	b.run(chromedp.Evaluate(`document.querySelector('.facts').dispatchEvent(new MouseEvent('mouseleave'))`, nil))
	if got := eval[string](b, lit); got != " | 0" {
		t.Errorf("leaving the steps leaves %q lit", got)
	}
	// The button (keyboard too): pinned, the established node selected and detailed.
	// Headless, the page has no focus, so focus events need emulating.
	b.run(emulation.SetFocusEmulationEnabled(true))
	b.run(chromedp.Evaluate(`(`+li+`).querySelector('.step-loc').id = 'loc-test'`, nil), chromedp.Focus(`#loc-test`, chromedp.ByQuery))
	if got := eval[string](b, lit); got != want {
		t.Errorf("focusing the button lights %q, want %q", got, want)
	}
	b.run(chromedp.Evaluate(`(`+li+`).querySelector('.step-loc').click()`, nil))
	b.run(chromedp.Evaluate(`document.activeElement.blur()`, nil))
	got := eval[string](b, `(() => { const c = `+cy+`, l = `+li+`; return [l.classList.contains('pinned'), c.nodes(':selected').map((n) => n.id()).join(' ') === l.dataset.nodes.split(' ')[0],
		(document.querySelector('#node-detail-title')?.textContent || '').includes('folder:root')].join(' | '); })()`)
	if got != "true | true | true" {
		t.Errorf("pinning: %q (pinned | top node selected | detail)", got)
	}
	if got := eval[string](b, lit); got != want {
		t.Errorf("pinned step lights %q, want %q", got, want)
	}
	// Selecting the user's node marks the first step; the background clears it all.
	b.run(chromedp.Evaluate(`(() => { const c = `+cy+`; c.nodes('[kind = "subject"]')[0].emit('tap'); })()`, nil))
	if got := eval[string](b, `[...document.querySelectorAll('.facts ol.steps[data-reading="up"] > li.step.covers > .ft')].map((e) => e.textContent).join(' / ')`); got != "user:eng-002 has member on group:eng" {
		t.Errorf("the user's node covers %q", got)
	}
	b.run(chromedp.Evaluate(`(() => { const c = `+cy+`; c.emit('tap'); })()`, nil))
	if got := eval[string](b, lit+` + ' | ' + document.querySelectorAll('.facts li.step.pinned, .facts li.step.covers').length`); got != " | 0 | 0" {
		t.Errorf("tapping the background leaves %q", got)
	}
	b.checkClean("step graph")
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

// Following a link from a result to Explore and coming back keeps the result
// as it was left: no replay, "Why" still open, the same side panel tab and
// zoom. Coming back by the Check tab restores from the tab's session; coming
// back by Back may reload the result's URL (when the browser does not keep
// the page), which counts as a return when it is the check last run.
func TestBrowserCheckReturnKeepsView(t *testing.T) {
	x := newTestExplorer(t)
	b := newBrowser(t, x)
	const check = `~/check?user=user:eng-002&relation=can_view&object=document:doc-0007&context=` +
		`%7B%22current_time%22%3A%222026-11-24T12%3A00%3A00Z%22%2C%22user_ip%22%3A%2210.1.2.3%22%7D`
	const state = `JSON.stringify({ pane: document.querySelector('#rpanel-why')?.scrollTop, step: document.querySelector('.player .step-count')?.textContent,
		play: document.querySelector('.player [data-act="play"]')?.textContent, tab: document.querySelector('#inspector [role="tab"][aria-selected="true"]')?.dataset.tab,
		zoom: document.querySelector('#trace-canvas')?._cyreg?.cy?.zoom().toFixed(2) })`
	for _, how := range []string{"reloaded URL", "Check tab"} {
		b.open(check)
		b.waitFor("result", `document.querySelector('#check-result .facts')`)
		b.run(chromedp.Evaluate(`document.querySelector('.player [data-act="final"]').click(); document.querySelector('#rpanel-why').scrollTop = 90;
			document.querySelector('#trace-canvas')._cyreg.cy.zoom(1.37); void 0`, nil), chromedp.Sleep(500*time.Millisecond))
		var before, after, href string
		b.run(chromedp.Evaluate(state, &before), chromedp.Evaluate(`location.href`, &href))
		b.run(chromedp.Evaluate(`document.querySelector('.facts .fref a').click()`, nil))
		b.run(chromedp.Sleep(1500*time.Millisecond), chromedp.WaitReady(`html[data-ready="true"]`, chromedp.ByQuery))
		if how == "reloaded URL" {
			b.run(chromedp.Navigate(href))
		} else {
			b.run(chromedp.Click(`a[href$="/check"]:not([href*="?"])`, chromedp.ByQuery))
		}
		b.run(chromedp.Sleep(1000*time.Millisecond), chromedp.WaitReady(`html[data-ready="true"]`, chromedp.ByQuery))
		b.waitFor("result back", `document.querySelector('#check-result .facts') && document.querySelector('#trace-canvas')?._cyreg?.cy`)
		b.run(chromedp.Sleep(1500 * time.Millisecond))
		b.run(chromedp.Evaluate(state, &after))
		if after != before {
			t.Errorf("%s: came back to %s, left %s", how, after, before)
		}
	}
	b.checkClean("return keeps view")
}

// TestBrowserCheckPhone: on a phone the check page never scrolls sideways
// (everything wraps or scrolls inside its own box), and the graph gets
// most of the screen's height.
func TestBrowserCheckPhone(t *testing.T) {
	x := newTestExplorer(t)
	b := newBrowser(t, x)
	b.run(emulation.SetDeviceMetricsOverride(390, 844, 2, true))
	b.open(`~/check?user=user:eng-002&relation=can_view&object=document:doc-0007&context=` +
		`%7B%22current_time%22%3A%222026-11-24T12%3A00%3A00Z%22%2C%22user_ip%22%3A%2210.1.2.3%22%7D`)
	b.waitFor("graph", `document.querySelector('#trace-canvas')?._cyreg?.cy?.nodes().length > 0`)
	b.settled()
	// What sticks out past the right edge, outside boxes that scroll on their
	// own or cut short with an ellipsis (cut off by a box that hides its
	// overflow counts: it can't be read).
	const over = `(() => { const w = document.documentElement.clientWidth; const out = [];
		for (const el of document.body.querySelectorAll('*')) {
			const r = el.getBoundingClientRect();
			if (r.width === 0 || r.right <= w + 1) continue;
			let clipped = false;
			// main is the page's own scroll box: scrolling it sideways is the page scrolling.
			for (let p = el.parentElement; p && p.tagName !== 'MAIN' && p !== document.body; p = p.parentElement) {
				const s = getComputedStyle(p);
				if ((/(auto|scroll)/.test(s.overflowX) || s.textOverflow === 'ellipsis') && p.getBoundingClientRect().right <= w + 1) { clipped = true; break; }
			}
			if (!clipped) out.push(el.tagName.toLowerCase() + (el.id ? '#' + el.id : '') + (el.classList.length ? '.' + [...el.classList].join('.') : '') + ' ' + Math.round(r.right));
		}
		return out.slice(0, 12).join(', '); })()`
	if got := eval[string](b, over); got != "" {
		t.Errorf("past the right edge of a 390px screen: %s", got)
	}
	if sw := eval[int](b, `Math.max(document.scrollingElement.scrollWidth - document.documentElement.clientWidth, document.querySelector('main').scrollWidth - document.querySelector('main').clientWidth)`); sw > 0 {
		t.Errorf("the page scrolls sideways by %dpx", sw)
	}
	// One at a time: the explanation first; the switch shows the graph,
	// measured and drawn at its size, and is remembered.
	const shown = `[getComputedStyle(document.querySelector('#inspector')).display !== 'none', getComputedStyle(document.querySelector('#trace-graph')).display !== 'none'].join(' ')`
	if got := eval[string](b, shown); got != "true false" {
		t.Errorf("at first (pane, graph): %q, want the pane only", got)
	}
	b.run(chromedp.Click(`.view-switch [data-view="graph"]`, chromedp.ByQuery))
	if got := eval[string](b, shown); got != "false true" {
		t.Errorf("after Graph (pane, graph): %q, want the graph only", got)
	}
	const cy = `document.querySelector('#trace-canvas')._cyreg.cy`
	b.waitFor("graph measured", cy+`.width() > 300 && `+cy+`.height() > 300`)
	if h := eval[float64](b, `document.querySelector('#trace-canvas').getBoundingClientRect().height / innerHeight`); h < 0.6 {
		t.Errorf("the graph is %.0f%% of the screen's height, want at least 60%%", h*100)
	}
	if got := eval[string](b, `document.querySelector('.view-switch [aria-pressed="true"]').dataset.view + ' ' + localStorage.getItem('fgax.phoneView')`); got != "graph \"graph\"" {
		t.Errorf("pressed and remembered: %q", got)
	}
	// Pinning a step (◎) from the explanation shows it on the graph.
	b.run(chromedp.Click(`.view-switch [data-view="read"]`, chromedp.ByQuery), chromedp.Click(`#rpanel-why li.step .step-loc`, chromedp.ByQuery))
	if got := eval[string](b, shown); got != "false true" {
		t.Errorf("after pinning a step (pane, graph): %q, want the graph", got)
	}
	b.checkClean("check phone")
}

// TestBrowserCheckReadableZoom: a granting path too long to show readably in
// the graph is shown from its top (the object) at a readable zoom rather
// than shrunk to fit; "Fit all" still shows everything.
func TestBrowserCheckReadableZoom(t *testing.T) {
	x := newTestExplorer(t)
	b := newBrowser(t, x)
	b.run(emulation.SetDeviceMetricsOverride(1280, 720, 1, false))
	b.open(`~/check?user=user:eng-002&relation=can_view&object=document:doc-0007&context=` +
		`%7B%22current_time%22%3A%222026-11-24T12%3A00%3A00Z%22%2C%22user_ip%22%3A%2210.1.2.3%22%7D`)
	b.waitFor("graph", `document.querySelector('#trace-canvas')?._cyreg?.cy?.nodes().length > 0`)
	b.settled()
	b.run(chromedp.Evaluate(`document.querySelector('.player [data-act="final"]').click()`, nil), chromedp.Sleep(700*time.Millisecond))
	const cy = `document.querySelector('#trace-canvas')._cyreg.cy`
	// The path does not fit readably here (else this test checks nothing).
	if fit := eval[float64](b, `(() => { const c = `+cy+`, bb = c.elements('.flowpath').boundingBox(); return Math.min((c.width() - 120) / bb.w, (c.height() - 120) / bb.h); })()`); fit >= 0.8 {
		t.Fatalf("the granting path fits at zoom %.2f; make the screen smaller", fit)
	}
	got := eval[string](b, `(() => { const c = `+cy+`, r = c.nodes('.root').renderedBoundingBox();
		return [c.zoom() >= 0.8 - 1e-6, r.y1 >= 0 && r.y2 <= c.height() / 3, r.x1 >= 0 && r.x2 <= c.width()].join(' '); })()`)
	if got != "true true true" {
		t.Errorf("readable framing: %q (zoom ≥ 0.8 | object near the top | object in view)", got)
	}
	// A note says part of the graph is out of view; its Fit all shows
	// everything, whatever the zoom, and the note goes.
	b.waitFor("note", `!document.querySelector('.graph-note').hidden`)
	b.run(chromedp.Click(`.graph-note [data-act="fit"]`, chromedp.ByQuery))
	if !eval[bool](b, `(() => { const c = `+cy+`, bb = c.elements(':visible').renderedBoundingBox(); return bb.y1 >= -1 && bb.y2 <= c.height() + 1 && c.zoom() < 0.8; })()`) {
		t.Error("Fit all should show the whole graph")
	}
	b.waitFor("note gone", `document.querySelector('.graph-note').hidden`)
	b.checkClean("readable zoom")
}

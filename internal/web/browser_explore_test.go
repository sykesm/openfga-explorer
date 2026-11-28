package web

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"github.com/sykesm/openfga-explorer/internal/config"
	"github.com/sykesm/openfga-explorer/internal/fgatest"
)

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
	b.settled() // htmx must have wired the filter before typing
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
	st, err := srv.CreateStore(t.Context(), "many-types", dsl.String(), nil)
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
	st, err := srv.CreateStore(t.Context(), "large", dsl.String(), nil)
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
		b.run(chromedp.Click(`#instance-note [data-act="undo"]`, chromedp.ByQuery))
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

// TestBrowserExploreClearKeepsOpenInstance: "Clear" starts the
// graph over from the instance open in the panel, and "Expand neighbors"
// puts it back if it was taken off the graph.
func TestBrowserExploreClearKeepsOpenInstance(t *testing.T) {
	x := newTestExplorer(t)
	b := newBrowser(t, x)
	b.open("~/explore?type=document&id=doc-0007")
	b.waitFor("instance panel", `document.querySelector('#instance-panel .instance[data-object="document:doc-0007"]')`)
	b.settled()
	const cy = `document.querySelector('#instance-canvas')._cyreg.cy`
	const own = cy + `.getElementById('document:doc-0007')`
	panelEdges := eval[int](b, `JSON.parse(document.querySelector('#instance-panel fga-graph-patch script').textContent).elements.filter((e) => e.group === 'edges').length`)
	// Grow the graph beyond the instance, then clear it.
	b.run(chromedp.Click(`#instance-panel [data-act="expand-hop"]`, chromedp.ByQuery))
	b.waitFor("hop", `document.querySelector('#instance-note .note-text').textContent.startsWith('Added')`)
	b.run(chromedp.Click(`.canvas-bar [data-act="clear"]`, chromedp.ByQuery))
	b.waitFor("cleared to the open instance", own+`.length && `+own+`.hasClass('current') && `+cy+`.edges().length === `+fmt.Sprint(panelEdges))
	if eval[bool](b, `!document.querySelector('#instance-empty').classList.contains('hidden')`) {
		t.Error("the graph says it is empty, with the open instance on it")
	}
	// Taken off the graph: Expand neighbors puts it back and expands around it.
	b.run(chromedp.Evaluate(`void `+own+`.remove()`, nil))
	b.run(chromedp.Click(`#instance-panel [data-act="expand-hop"]`, chromedp.ByQuery))
	b.waitFor("back on the graph", own+`.length && `+own+`.hasClass('current') && document.querySelector('#instance-note .note-text').textContent.includes('around')`)
	b.checkClean("clear keeps the open instance")
}

// TestBrowserExploreInstanceGraphKeepsView: opening an instance from the
// graph, or adding an edge from a list, keeps the zoom and leaves the nodes
// already placed where they are.
func TestBrowserExploreInstanceGraphKeepsView(t *testing.T) {
	x := newTestExplorer(t)
	b := newBrowser(t, x)
	b.open("~/explore?type=document&id=doc-0007")
	b.waitFor("instance panel", `document.querySelector('#instance-panel .instance[data-object="document:doc-0007"]')`)
	b.settled()
	const cy = `document.querySelector('#instance-canvas')._cyreg.cy`
	positions := `JSON.stringify(` + cy + `.nodes().map((n) => [n.id(), Math.round(n.position('x')), Math.round(n.position('y'))]))`
	kept := func(what, before string) {
		t.Helper()
		if z := eval[float64](b, cy+`.zoom()`); z < 1.59 || z > 1.61 {
			t.Errorf("%s: zoom %.2f, want 1.60 (kept)", what, z)
		}
		after := eval[string](b, positions)
		for _, p := range regexp.MustCompile(`\["[^"]+",-?\d+,-?\d+\]`).FindAllString(before, -1) {
			if !strings.Contains(after, p) {
				t.Errorf("%s: node moved: %s", what, p)
			}
		}
	}
	b.run(chromedp.Evaluate(`void `+cy+`.zoom({ level: 1.6, renderedPosition: { x: 200, y: 200 } })`, nil))

	// Open a neighbor from the graph.
	before := eval[string](b, positions)
	peer := eval[string](b, cy+`.nodes().filter((n) => n.data('kind') !== 'count' && !n.hasClass('current'))[0].id()`)
	b.run(chromedp.Evaluate(`void `+cy+`.getElementById('`+peer+`').emit('tap')`, nil))
	b.waitFor("opened", `document.querySelector('#instance-panel .instance[data-object="`+peer+`"]')`)
	b.settled()
	b.run(chromedp.Sleep(400 * time.Millisecond)) // the pan to it
	kept("opening "+peer, before)
	if !eval[bool](b, `(() => { const p = `+cy+`.getElementById('`+peer+`').renderedPosition(); return p.x > 0 && p.y > 0 && p.x < `+cy+`.width() && p.y < `+cy+`.height(); })()`) {
		t.Errorf("%s is not in view after opening it", peer)
	}

	// Add an edge from the panel's list.
	before = eval[string](b, positions)
	n := eval[int](b, cy+`.nodes().length`)
	if eval[bool](b, `!!document.querySelector('#instance-panel .add-edge:not(.on)')`) {
		b.run(chromedp.Click(`#instance-panel .add-edge:not(.on)`, chromedp.ByQuery))
		b.waitFor("edge added", cy+`.nodes().length >= `+fmt.Sprint(n))
		b.run(chromedp.Sleep(300 * time.Millisecond)) // the old re-layout ran 120ms later
		kept("adding an edge", before)
	}
	b.checkClean("instance graph keeps its view")
}

// TestBrowserExploreLeavesAndClose: "Remove leaves" removes the instances
// with one edge that are not expanded or open, with an Undo; Close leaves the
// open instance, after which Clear empties the graph.
func TestBrowserExploreLeavesAndClose(t *testing.T) {
	x := newTestExplorer(t)
	b := newBrowser(t, x)
	b.open("~/explore?type=document&id=doc-0007")
	b.waitFor("instance panel", `document.querySelector('#instance-panel .instance[data-object="document:doc-0007"]')`)
	b.settled()
	const cy = `document.querySelector('#instance-canvas')._cyreg.cy`
	const note = `document.querySelector('#instance-note .note-text').textContent`
	b.run(chromedp.Click(`#instance-panel [data-act="expand-hop"]`, chromedp.ByQuery))
	b.waitFor("hop", note+`.startsWith('Added')`)
	leaves := cy + `.nodes().filter((n) => n.data('kind') !== 'count' && !n.hasClass('expanded') && !n.data('focus') && !n.hasClass('current') && n.degree(false) <= 1).length`
	before, n := eval[int](b, leaves), eval[int](b, cy+`.nodes().length`)
	if before == 0 {
		t.Fatal("no leaves after +1 hop")
	}
	b.run(chromedp.Click(`.canvas-bar [data-act="remove-leaves"]`, chromedp.ByQuery))
	b.waitFor("leaves removed", note+`.startsWith('Removed')`)
	if got := eval[int](b, cy+`.nodes().length`); got != n-before {
		t.Errorf("%d nodes after removing %d leaves of %d", got, before, n)
	}
	if !eval[bool](b, cy+`.getElementById('document:doc-0007').hasClass('current')`) {
		t.Error("the open instance was removed")
	}
	b.run(chromedp.Click(`#instance-note [data-act="undo"]`, chromedp.ByQuery))
	b.waitFor("undone", cy+`.nodes().length === `+fmt.Sprint(n))

	// Close: the panel shows the type again; the instance stays on the graph.
	b.run(chromedp.Click(`#instance-panel [data-act="close-instance"]`, chromedp.ByQuery))
	b.waitFor("closed", `!document.querySelector('#instance-panel .instance')`)
	if eval[bool](b, cy+`.nodes('.current').length > 0`) || !eval[bool](b, cy+`.getElementById('document:doc-0007').length > 0`) {
		t.Error("closing should keep the instance on the graph, no longer marked open")
	}
	if eval[bool](b, `new URL(location.href).searchParams.has('id')`) {
		t.Error("the URL still names the closed instance")
	}
	if eval[bool](b, `document.querySelector('#explore-side').classList.contains('focus-instance')`) {
		t.Error("the side panel still hides the type section")
	}
	b.run(chromedp.Click(`.canvas-bar [data-act="clear"]`, chromedp.ByQuery))
	b.waitFor("empty", cy+`.nodes().length === 0`)
	b.checkClean("leaves and close")
}

// TestBrowserExploreTypeGraphKeepsZoom: choosing a type (a node, an edge, a
// chip) or reopening the side panel keeps the type graph's zoom; the chosen
// type comes into view.
func TestBrowserExploreTypeGraphKeepsZoom(t *testing.T) {
	x := newTestExplorer(t)
	b := newBrowser(t, x)
	b.open("~/explore")
	const cy = `document.querySelector('#schema-canvas')._cyreg.cy`
	b.waitFor("type graph", `document.querySelector('#schema-canvas')._cyreg?.cy?.nodes().length > 0`)
	b.settled()
	b.run(chromedp.Evaluate(`void `+cy+`.zoom({ level: 1.7, renderedPosition: { x: 100, y: 100 } })`, nil))
	// The chosen type is inside the canvas (its middle, rendered).
	inView := func(typ string) bool {
		return eval[bool](b, `(() => { const p = `+cy+`.getElementById('`+typ+`').renderedPosition(); return p.x > 0 && p.y > 0 && p.x < `+cy+`.width() && p.y < `+cy+`.height(); })()`)
	}
	check := func(what, typ string) {
		t.Helper()
		b.waitFor(what, `document.querySelector('#type-panel [data-selected-type="`+typ+`"]')`)
		b.settled()
		if z := eval[float64](b, cy+`.zoom()`); z < 1.69 || z > 1.71 {
			t.Errorf("%s: zoom %.2f, want 1.70 (kept)", what, z)
		}
		if !inView(typ) {
			t.Errorf("%s: %s is not in view", what, typ)
		}
	}
	b.run(chromedp.Evaluate(`void `+cy+`.getElementById('folder').emit('tap')`, nil))
	check("node", "folder")
	b.run(chromedp.Evaluate(`void `+cy+`.edges('[target = "document"]')[0].emit('tap')`, nil))
	check("edge", "document")
	b.run(chromedp.Click(`.type-chip[data-type="group"]`, chromedp.ByQuery))
	check("chip", "group")
	// Collapsing the side panel and reopening it (a node tap does) keeps it too.
	b.run(chromedp.Evaluate(`document.activeElement.blur()`, nil), chromedp.KeyEvent("t"))
	b.waitFor("collapsed", `document.querySelector('.explore-layout').classList.contains('side-collapsed')`)
	b.run(chromedp.Evaluate(`void `+cy+`.getElementById('user').emit('tap')`, nil))
	check("node, reopening the panel", "user")
	b.checkClean("type graph zoom")
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

// Objects used recently are kept in the browser and listed first by an empty
// ID search: an instance opened in Explore, and both sides of an allowed check.
func TestBrowserRecentObjects(t *testing.T) {
	x := newTestExplorer(t)
	b := newBrowser(t, x)
	b.open("~/explore?type=group&id=eng")
	b.waitFor("instance", `document.querySelector('#instance-panel .instance[data-object="group:eng"][data-exists="true"]')`)
	b.settled()
	if got := eval[string](b, `JSON.stringify(JSON.parse(localStorage.getItem('fgax.recentObjects:' + document.body.dataset.base) || '[]'))`); !strings.Contains(got, `"group:eng"`) {
		t.Errorf("recent objects after opening group:eng: %s", got)
	}
	b.run(chromedp.Evaluate(`(() => { const q = document.querySelector('fga-picker[data-field="inst"] .picker-q'); q.value = ''; q.focus(); htmx.trigger(q, 'focus'); })()`, nil))
	b.waitFor("recent in search", `[...document.querySelectorAll('fga-picker[data-field="inst"] .picker-results .pick')].some((p) => p.dataset.value === 'group:eng' && p.textContent.includes('recent'))`)

	b.open(`~/check?user=user:eng-002&relation=can_view&object=document:doc-0007&context=` +
		`%7B%22current_time%22%3A%222026-11-24T12%3A00%3A00Z%22%2C%22user_ip%22%3A%2210.1.2.3%22%7D`)
	b.waitFor("allowed", `document.querySelector('#check-result .verdict.v-allowed')`)
	got := eval[string](b, `localStorage.getItem('fgax.recentObjects:' + document.body.dataset.base) || ''`)
	if !strings.Contains(got, `"document:doc-0007"`) || !strings.Contains(got, `"user:eng-002"`) || !strings.Contains(got, `"group:eng"`) {
		t.Errorf("recent objects after an allowed check: %s", got)
	}
	b.checkClean("recent objects")
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

// TestBrowserTypeComboEnter: Enter in a picker's type box never submits the
// check form (it is one field of several), even with its list closed; and
// leaving the box and coming straight back keeps the list open (the delayed
// close on blur used to shut it, so a quick Enter fell through to the form).
func TestBrowserTypeComboEnter(t *testing.T) {
	srv := fgatest.Start(t)
	var dsl strings.Builder
	dsl.WriteString("model\n  schema 1.1\ntype user\n")
	for i := range 20 { // enough types for the type box (not a plain select)
		fmt.Fprintf(&dsl, "type kind_%02d\n  relations\n    define owner: [user]\n", i)
	}
	st, err := srv.CreateStore(t.Context(), "many-types", dsl.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	x := explorerFor(t, st)
	b := newBrowser(t, x)
	b.open("~/check")
	const q = `fga-picker[data-field="object"] .tcombo-q`
	b.waitFor("combobox", `document.querySelector('`+q+`')`)
	b.run(chromedp.Evaluate(`window.__submits = 0; document.body.addEventListener('htmx:beforeRequest', (e) => { if (/\/check\/run/.test(e.detail.pathInfo?.requestPath || '')) __submits++; });
		document.querySelector('#check-form').addEventListener('submit', () => __submits++, true); void 0`, nil))
	// Blur and refocus at once: after the old close delay, the list is still open.
	b.run(chromedp.Click(q, chromedp.ByQuery))
	b.waitFor("list open", `!document.querySelector('#tc-object').hidden`)
	b.run(chromedp.Evaluate(`(() => { const i = document.querySelector('`+q+`'); i.blur(); i.focus(); })()`, nil), chromedp.Sleep(250*time.Millisecond))
	if eval[bool](b, `document.querySelector('#tc-object').hidden`) {
		t.Error("leaving the type box and coming straight back closed its list")
	}
	// With the list closed (Escape), Enter does not submit the form.
	b.key(q, kb.Escape)
	b.waitFor("closed", `document.querySelector('#tc-object').hidden`)
	b.key(q, kb.Enter)
	b.run(chromedp.Sleep(300 * time.Millisecond))
	if n := eval[int](b, `__submits`); n != 0 {
		t.Errorf("Enter in the type box submitted the check form %d time(s)", n)
	}
	// Choosing with ↓ and Enter moves on to the ID box without submitting.
	b.run(chromedp.Evaluate(`document.querySelector('`+q+`').value = ''`, nil))
	b.key(q, "kind_1"+kb.ArrowDown+kb.Enter)
	b.waitFor("chosen", `/^kind_1\d$/.test(document.querySelector('fga-picker[data-field="object"] select[name="object_type"]').value)`)
	b.run(chromedp.Sleep(300 * time.Millisecond))
	if n := eval[int](b, `__submits`); n != 0 {
		t.Errorf("choosing a type with Enter submitted the check form %d time(s)", n)
	}
	b.checkClean("type combo enter")
}

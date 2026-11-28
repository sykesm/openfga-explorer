package web

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"github.com/sykesm/openfga-explorer/internal/fgatest"
)

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

// TestBrowserModules: the model page's file tabs, and opening a type from
// its module group in Explore.
func TestBrowserModules(t *testing.T) {
	srv := fgatest.Start(t)
	st, err := srv.CreateModularStore(t.Context(), "modular-demo", fgatest.SeedDir()+"/modular", nil)
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
	b.settled() // htmx must have taken over the link, or it navigates
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

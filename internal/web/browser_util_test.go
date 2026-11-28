package web

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/emulation"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// Browser smoke tests: the explorer in headless Chrome against an in-process
// OpenFGA. They cover the flows that have regressed before (pickers, edge
// selection mirroring the graph, check results, keyboard access, the type
// combobox) and fail on any JavaScript exception or CSP violation. They need
// Chrome or Chromium and are skipped with -short or when none is installed.
//
// This file holds the harness; the tests are by page: browser_check_test.go
// (the Check page and its trace), browser_explore_test.go (Explore), and
// browser_pages_test.go (the other pages).

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

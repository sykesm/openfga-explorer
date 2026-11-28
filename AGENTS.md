# AGENTS.md

Guidance for coding agents (and people) working on OpenFGA Explorer: a
read-only web UI and CLI for exploring OpenFGA authorization models and data,
and for explaining check decisions. Go + htmx, server-rendered.

## Ground rules

- **Read-only.** The explorer never writes to OpenFGA (no tuple writes, no
  model writes, no assertions). Writing is a deliberate future decision, not
  something to add in passing.
- **Prefer logic in Go — where it is prudent.** Put logic in Go when it makes
  sense and improves maintainability: decisions that follow from the model or
  the trace (granting paths, folds, node details, summaries, diffs, checks)
  belong in Go, where they are tested. Do not move logic just to move it:
  interaction, layout, and instant filtering (e.g. type matching as you type)
  stay in the browser.
- **No JavaScript build step, no npm.** Browser code is plain ES modules in
  `internal/web/static/js/`, served as-is. JSDoc comments document types; there
  is no TypeScript. Each module starts with `// @ts-check`, so editors check
  those types (nothing in the build does); `$` returns `HTMLElement | null`,
  so check for null or cast. `internal/web/package.json` only marks the
  modules as ES modules for `node --test`.
- **Stateless server.** Nothing about a person's work is kept between requests
  (drafts live in the browser; scenario runs start and discard an in-memory
  engine per request), so replicas scale horizontally. Rate limits and caches
  may live per replica only when losing them costs time, never correctness.
- **Built for large stores and models.** Every query is bounded (budgets,
  timeouts, page sizes, sampling) and paginated where results can be large.
  Never load or draw "everything" by default; say when results are partial.
- **Be honest in the UI.** Don't imply a result is exhaustive unless it is;
  label samples as samples; say what a view does not show (e.g. the change log
  does not record who made a change). Plain language over jargon.
- **People don't choose plumbing.** Data access strategies (API vs datastore)
  are configuration, not UI choices.

## Layout

```
cmd/fgaexplorer       serve and trace commands
internal/fga          OpenFGA HTTP client (official protobuf types)
internal/schema       model analysis: kinds, reachability, routes, modules, checks, diff
internal/trace        check walker, explanations, text and graph rendering
internal/scenario     `fga model test` documents, sandbox runs, capture, masking
internal/cond         CEL condition evaluation (including OpenFGA's ipaddress)
internal/datastore    optional read-only Postgres connection
internal/search       instance prefix search (Postgres)
internal/tuples       edge sources: OpenFGA API, datastore
internal/ids          per-type ID normalization and display
internal/config       YAML + environment configuration
internal/fgatest      in-process OpenFGA (memory datastore) for tests
internal/web          handlers, templates, static assets, browser modules
deploy/               docker compose stack, generic sample data (seed/gen
                      generates it)
```

## Browser code

- **Content Security Policy is strict** (`script-src 'self'; style-src 'self'`):
  no inline scripts, no inline `<style>`, and no `style="…"` attributes in HTML
  you build as strings. Set styles through the DOM (`el.style.x = …`).
- **Accessibility is a requirement**, checked by tests: WCAG AA contrast for
  palette tokens *and* for what the graphs actually draw (never fade labels
  with opacity; de-emphasize with style), 24×24 px targets, keyboard support,
  WAI-ARIA patterns (tabs, comboboxes, modal dialogs with focus management),
  `prefers-reduced-motion`.
- Each module owns its state; `main.js` only wires page events. Shortcuts are
  registered per page (`shortcuts.onKey`). Escape HTML you build (`esc`).
- **Widgets are custom elements** (`<fga-picker>`, `<fga-tabs>`,
  `<fga-type-finder>`, `<fga-edge-list>`, `<fga-graph-patch>`, `<fga-trace>`):
  the server renders their parts, and the element sets itself up in
  `connectedCallback`, wherever it appears (first page or htmx swap), so
  `main.js` has no per-swap wiring. Work that needs htmx to have finished
  (settled, `hx-*` processed) waits with `whenLoaded` (core.js); code that
  loads a region acts on it when `load()` resolves. Call methods only on
  elements the browser has set up (`:defined`). Light DOM only (no shadow
  DOM: ARIA id references and the global stylesheet must reach inside).
  Pages call the element's methods and listen for its `fgax:*` events; a
  widget never reaches into page state. Document the parts, methods, and
  events at the top of its definition. Mark state with ARIA attributes
  (`aria-selected`, …) rather than classes on elements with an `id`: htmx
  puts back the server's `class` when it settles a swap.

## Domain notes

- **Modular models**: OpenFGA stores only the combined model plus each
  definition's module and file. The explorer rebuilds module files from that
  metadata (rules exact; comments and layout are not stored), and a test
  checks the round trip. Preserve module scoping in anything you add.
- **Scenarios** use the `fga model test` format (`.fga.yaml`, or JSON) and must
  stay compatible with the fga CLI. File references are not read server-side.
- **Live queries** (queries over real data: the Access tab, "check real
  people") are admin-only, explicitly started, and bounded by
  `live_queries` settings. Without authentication, nobody is an admin unless
  the server runs with `--dangerously-treat-anonymous-as-admin`.
- **Exposure**: the server listens on `127.0.0.1:8090` by default and has no
  authentication of its own (an SSO proxy supplies an identity header).
  Cross-site requests are rejected (`sameSite`) without affecting loopback use.

## Tests

- `make test` runs everything that needs no Docker: Go tests against an
  in-process OpenFGA (`internal/fgatest`, loaded from `deploy/seed`), browser
  tests in headless Chrome (skipped without Chrome or with `-short`), and JS
  unit tests (`node --test`, Node 18+). `make test-short` is the quick loop.
- Golden files: `go test ./internal/trace -run Golden -update`, then review.
- `make test-integration` needs the Docker stack (`make up seed`) for the
  Postgres datastore and search tests.
- When you fix a bug, add a test that would have caught it. Browser tests must
  fail on JavaScript errors and CSP violations (`checkClean`).

## Content and dependencies

- **Keep the repository clean and reusable**: no secrets, no real or
  proprietary models, data, names, or hostnames. Sample models and data are
  generic (Acme/Globex documents, folders, groups) and generated by
  `go run ./deploy/seed/gen` (a test fails if the committed JSON drifts).
  Run `make lint` (gofmt, go vet, staticcheck, modernize) and `make leaks`
  (gitleaks) before committing. Run `make vuln` (govulncheck) when dependencies
  change; it queries the Go vulnerability database, so it is not part of
  `make test`.
- **Minimal dependencies.** Add one only when it clearly earns its place.
  Vendored browser libraries are pinned, unmodified, and listed in
  `THIRD_PARTY_NOTICES.md` (update it when they change).
- **Go and shell only — no Python.** Keep helpers as simple as the job: a
  short shell command or script (the Makefile reads the `fga` CLI's JSON
  with `jq`), and a small Go command run with `go run` only when shell would
  be awkward (e.g. `deploy/seed/gen`). Developer tools (linters, formatters,
  scanners) are pinned with the `go tool` pattern in `tools.mod`/`tools.sum`
  and run as `go tool -modfile=tools.mod <tool>`, so their dependencies stay
  out of `go.mod` and they need no separate install.
- **Prerequisites** (README, "Prerequisites"): Go, make, jq, Node, Docker,
  and the fga CLI. Use any of them freely; don't add new ones (or Python)
  without a clear reason, and list any you add there.
- Licensed under Apache-2.0 (`LICENSE`, `NOTICE`).

## Commits

- Commit only when asked.
- Do not add `Co-Authored-By`, "Generated with", or similar attribution
  trailers or lines to commit messages or pull requests.

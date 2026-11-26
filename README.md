# OpenFGA Explorer

A read-only Go + htmx tool for exploring [OpenFGA](https://openfga.dev)
authorization models and data, and explaining check decisions. It is built
for large stores and models. It talks to the OpenFGA API and, optionally,
reads OpenFGA's Postgres datastore for search and exact counts. It never
writes to OpenFGA.

Licensed under Apache-2.0. Not affiliated with or endorsed by the OpenFGA
project.

- **Explore**: pick a type on the type graph to see its relations, grouped as
  *Direct* (tuples only), *Derived* (computed only), and *Direct + derived*.
  Pick an instance with prefix search, then walk the graph. Relations with
  few edges are listed and drawn right away. Relations with many edges show a
  count (`100+`) and an edge chooser.
- **Check**: given a user, relation, and object, the explorer re-evaluates
  the check step by step. It draws every path it evaluated, animates the
  walk, highlights the branches that *block* access (failed intersections,
  exclusions that apply, conditions that evaluated false), and prints the
  same trace as an indented text tree.

## Prerequisites

| Tool | Version | Used for |
|---|---|---|
| [Go](https://go.dev/dl/) | 1.27+ | building, tests, `go run` helpers, and `go tool` dev tools (gitleaks) |
| make | 3.81+ (GNU) | the Makefile targets |
| [jq](https://jqlang.org) | 1.6+ | reading the fga CLI's JSON in `make seed` and the integration targets |
| [Node.js](https://nodejs.org) | 18+ | the JS unit tests (`node --test`); nothing to install from npm |
| [Docker](https://docs.docker.com/get-docker/) with Compose v2 | | the local stack (OpenFGA, Postgres, the explorer) and the container image |
| [fga CLI](https://github.com/openfga/cli) | 0.9+ | loading sample stores (`make seed`, `make seed-modular`) |

Tested with Go 1.27.4, GNU Make 3.81, jq 1.8.2, Node 18.16, Docker 29.9 with
Compose v5.7, and fga 0.9.0. Optional: Google Chrome or Chromium for the
headless-browser tests (skipped without it). Running only the explorer
binary against an existing OpenFGA needs none of these beyond Go to build it.

## Quick start

Everything in containers (OpenFGA, Postgres, the explorer):

```sh
docker compose -f deploy/docker-compose.yml -p fgaexplorer up -d --build
make seed            # sample store (needs the fga CLI); make seed-modular for a modular model
open http://127.0.0.1:8090
```

Or build and run the binary (Go 1.27) against the same stack:

```sh
make build
./bin/fgaexplorer serve --config explorer.example.yaml \
  --datastore-uri 'postgres://openfga:openfga@localhost:5433/openfga?sslmode=disable'
open http://127.0.0.1:8090
```

The sample data has nested groups, folder inheritance, an intersection, an
exclusion, a wildcard, conditions, and relations with 100+ edges. A container
image on its own: `docker build -t openfga-explorer .` (see `Dockerfile`).

The stack runs OpenFGA v1.23 on Postgres 18, with Postgres data in the named
volume `fgaexplorer_pgdata`. A new major version of Postgres can't read the
old one's files, so to upgrade an existing stack, dump, recreate, and restore:

```sh
docker exec fgaexplorer-postgres-1 pg_dump -U openfga -Fc openfga > openfga.dump
docker compose -f deploy/docker-compose.yml -p fgaexplorer down -v   # deletes the old data
docker compose -f deploy/docker-compose.yml -p fgaexplorer up -d --wait postgres
docker exec -i fgaexplorer-postgres-1 pg_restore -U openfga -d openfga --no-owner < openfga.dump
docker compose -f deploy/docker-compose.yml -p fgaexplorer up -d
```

Or start over: `down -v`, `up -d`, and `make seed`.

### Listening and exposure

The explorer listens on **127.0.0.1:8090** (this machine only) unless told
otherwise with `--listen` (or `listen:` / `FGAX_LISTEN`): one interface, e.g.
`--listen 192.0.2.10:8090`, or every interface, `--listen 0.0.0.0:8090`
(`[::]:8090`, `:8090`). It has **no authentication of its own**: beyond this
machine, put it behind an authenticating proxy that sets an identity header
(`auth.identity_header`), or reach it over an SSH tunnel. It logs a warning
when it listens beyond loopback. In containers it listens on all interfaces
inside the container, and the compose file publishes it on 127.0.0.1 only
(`EXPLORER_PUBLISH` changes that).

Requests that another site makes a browser send (cross-site fetches and form
posts) are rejected, so a page elsewhere can't start traces or queries as a
signed-in person; same-origin use, links from tickets, and non-browser
clients are unaffected. Admin-only features (queries over real data) need an
admin identity; without authentication, `--dangerously-treat-anonymous-as-admin`
makes anonymous requests admins (for local demos).

### Using an existing OpenFGA

Point `openfga.api_url` (or `--api-url`, `OPENFGA_API_URL`) at any OpenFGA
server; tested with v1.23. The datastore is optional; with it (`datastore.uri`,
`--datastore-uri`, `FGAX_DATASTORE_URI`) the explorer needs:

- the Postgres database of the **same** OpenFGA deployment (read replicas are
  fine and suit a shared service);
- OpenFGA's schema migrated through `006_add_collate_index` (its
  `idx_user_lookup` index makes edge and prefix queries fast);
- a role with `SELECT` on the `tuple` table (the connection is opened
  read-only with a 5s statement timeout);
- the `tuple` table in the connection's schema (set
  `options=-csearch_path=<schema>` in the URI if OpenFGA uses another);
- Postgres 11 or newer. `C`/`POSIX` and English-style linguistic collations
  (`en_US`) are handled; other locale collations are untested.

OpenFGA on MySQL or SQLite works through the API alone (search then takes exact
IDs, and edge counts are approximate).

Explain a check from the terminal:

```sh
./bin/fgaexplorer trace --store $STORE --user user:mallory --relation can_view --object document:doc-0004
./bin/fgaexplorer trace ... --context '{"current_time":"2026-11-24T12:00:00Z"}' --all-paths
./bin/fgaexplorer trace ... --json     # Cytoscape elements + animation steps
```

## Using it

- **Plain-language answers.** Every check result starts with a sentence, e.g.
  *"user:sales-007 has viewer on document:roadmap through group:sales →
  group:team-150"* or *"excluded by “but not blocked from org” because
  user:mallory has blocked on organization:acme"*, with the step-by-step facts
  behind it. **Copy** puts the sentence, facts, text trace and permalink on the
  clipboard as Markdown; **PNG** saves the graph.
- **Large models.** Above 12 types, the Explore panel offers a search (by
  type name or relation, e.g. “can_approve”) with results as you type, your
  recent types (each removable, or *Clear all*), and the most connected types
  as starting points; Check's type pickers become the same kind of search.
  Above 40 types, the type graph starts empty and shows the chosen type's
  neighborhood (*Draw all types* is still there).
- **Modular models** (`fga.mod`, schema 1.2) keep their module scoping. The
  Model page lists each module (files, types it defines, types it extends,
  conditions) and shows every file, plus `fga.mod` and the combined model
  annotated with sources. OpenFGA stores only the combined model with each
  definition's module and file, so the files are rebuilt from that metadata;
  a test checks that combining them again gives the same model. Explore
  groups types by module, tags each type with its module and each relation
  another module added (`extend type`), and the type finder lists a module's
  types; check traces name the module behind each relation. Try it with
  `make seed-modular`.
- **Scenarios** (the Scenarios tab): tuples plus what should and shouldn't
  be allowed, in the `fga model test` format (`.fga.yaml`, or the same thing
  as JSON). Each test runs in a fresh in-memory OpenFGA inside the request,
  with only its own tuples, against the document's `model` or the model being
  viewed. Results explain every expectation and link each check to the tuples
  that granted it. Nothing is stored on the server (the draft stays in the
  browser), so any replica can serve a run; runs are bounded (5,000 tuples
  per test, 500 assertions, 10s, limited concurrency). File references
  (`model_file`, `tuple_file`) aren't read. See
  `deploy/seed/scenarios.fga.yaml`, which also runs with
  `fga model test --store-id`. Besides typing or pasting, **Add to
  scenario** on a check result adds that check to the draft with the tuples
  on its granting path (conditions included), so it reproduces on its own;
  **rename IDs** (on by default) renames every ID consistently per type
  (`user:anne` → `user:user-1`) so no real names leave the store.
- **Model checks and changes.** The Model page lists problems OpenFGA
  accepts but that are almost always mistakes (relations nothing can grant,
  “X from Y” parents without X, unused conditions and parameters, unused
  types), and **What changed in this version?** compares two versions: rule
  changes, and which user types gained or lost each relation, including
  through relations it depends on.
- **Readable graphs.** Large fan-outs of failed branches fold into one
  "N other branches checked" node (click to expand). The longest granting
  route is laid out as a straight vertical spine (object at the top, user at
  the bottom) with other branches beside it; it is animated and zoomed to. Nodes carry ✓/✗ marks and failed
  edges are dashed, so nothing depends on color alone. Zoomed out, nodes show
  short labels.
- **Layout.** After a run the check form collapses to one line (**Edit** or
  `E` reopens it). Beside the graph, a resizable side panel shows the selected
  node's details (what it means, the server's verdict, links to trace or
  explore it) above two tabs: **Text trace** and **Who else?** (other holders,
  each one click from its own trace). `T` or **Panel** collapses it so the graph
  takes the full width; on phones it is a bottom sheet. The explore side panel
  is resizable and collapsible the same way (`T` or **Panel**; it reopens when
  you open a type or instance from the graph), and instance details are split
  into Edges / Derived / Incoming tabs (Incoming loads when opened).
- **Expanding the instance graph in place.** Shift+click a node (or
  right-click it: Open, Expand here, Expand +1 hop, Remove) to add its edges
  without opening it; **+1 hop** (the canvas bar, or **Expand neighbors** in
  the instance panel) expands the open instance's neighbors that are not
  expanded yet, then the next ring. Nodes already placed stay put; a dashed
  outline marks instances not expanded yet; the line over the graph says what
  was added, offers **Undo**, and says when a limit cut an expansion short.
  **Remove leaves** takes off the instances with one edge that are not
  expanded or open (again for the next layer; also per node, from its menu),
  with an Undo. **Clear** starts the graph over from the instance open in the
  panel; **Close** (in the panel) leaves that instance, so Clear empties it.
  Expansions are bounded per person by `graph_expansion` (instances per
  expansion, a `max_reads` read budget, a `timeout`, one at a time,
  `per_minute`), with higher limits for admins under `graph_expansion.admin`
  (still capped); `enabled: false` turns it off.
- **Changes** (the Changes tab): tuple writes and deletes from OpenFGA's
  change log, for live, the last 15 minutes to 7 days, or between two times
  (in the browser's timezone), optionally for one type; 50 per page, oldest
  first. Pages are links carrying OpenFGA's cursor; live polls with it. The
  log doesn't record who made a change.
- **Queries over real data (admins).** With `live_queries.enabled`, admins
  get two read-only tools that query the store's real tuples: an **Access**
  tab in Explore ("what can this user, or group's members, access?", one
  ListObjects call per relation), and **Check real people** on the model
  changes page (samples objects of the type from the change log and compares
  who holds the relation under the old and new model with ListUsers). Each
  run is started explicitly and bounded: one at a time per person,
  `runs_per_hour` per person (counted per replica), a `max_checks` call
  budget, `max_objects` sampled, and a `timeout`; results say they are a
  sample. Admins come from `auth.roles` via the identity header. Without
  authentication, nobody is an admin unless the server runs with
  `--dangerously-treat-anonymous-as-admin` (anonymous requests only; a named
  identity is never upgraded).
- **Command palette.** Ctrl+K (⌘K) or **Search** finds pages and types, by
  name or by relation.
- **Module colors.** In a modular model, Explore can color types by module.
- **Shared request context.** The header's **Context** sets condition
  parameters (e.g. `current_time`, `user_ip`) once for checks and for
  "who has it?" lists in both views.
- **Keyboard.** `/` search · `Space` play/pause · `←` `→` step · `F` fit ·
  `T` side panel · `E` edit · `?` help. In the text trace, `↑` `↓` move and `Enter`
  shows a node's details.
- Light and dark themes follow the OS; the palette (Okabe–Ito hues) is
  checked against WCAG AA by `internal/web/contrast_test.go`, and what the
  graph actually draws (including de-emphasized, skipped and dimmed parts) by
  `TestBrowserGraphContrast`.

## How check explanations work

OpenFGA's Check API only returns allowed or denied. To explain a decision,
the walker (`internal/trace`) evaluates the model's rewrite rules itself:

| Rewrite | How it is evaluated |
|---|---|
| `[user, …]` (direct) | First an exact `Read` for a tuple naming the user, then the `type:*` wildcard tuple, then userset tuples (`group#member`), each followed recursively |
| `owner` (computed) | Recurse on the same object |
| `viewer from parent` | `Read` the `parent` tuples, then recurse on each parent |
| `or` / `and` / `but not` | Combine child results; the deciding branch is marked *blocking* |
| `with condition` | Evaluate the CEL expression locally (cel-go), using the tuple's context, which takes precedence over the request context, as in OpenFGA |

Large edge sets are handled with **probes** rather than full scans. After
`max_fanout` edges, the walker calls `ListObjects` to find the specific
groups or parents the user belongs to, then does an exact `Read` for each
candidate. If `ListObjects` returns nothing, that proves no remaining edge
can grant access, so the walker can conclude a denial without reading the
rest.

Each walk has a limit on API calls and time. When the limit runs out,
branches are marked **undecided** rather than guessed. Each relation node is
then checked with a real server `Check` (in parallel, counted separately), so:

- undecided nodes still show the server's verdict, and
- any disagreement between the walker and the server is shown in magenta.

Click any node and choose **Trace from here** to drill into it with a fresh
budget.

The walker/server comparison test checks the walker against the server on every
(user, relation, object) combination in the sample store, under several
budgets. It requires zero wrong answers; "undecided" is allowed only when the
budget runs out.

### Performance

Walks issue reads ahead of time: before evaluating a fan-out (group edges,
parents, probe candidates) the walker plans each child's reads from the model
and runs them concurrently (`Parallel`, default 16), then evaluates
sequentially from that cache, so decisions and replay order are unchanged.
Verification uses BatchCheck (falling back to one Check per node on older
servers), reads are cached for 30 seconds across walks, and long walks stream
progress and can be canceled. At a simulated 10 ms per API call
(`LATENCY=1 go test ./internal/trace -run Latency`), traces of the sample store
run 2.4–2.7× faster with identical results.

## Instance search and collation

The OpenFGA API cannot list the objects of a type, so prefix search reads
OpenFGA's Postgres `tuple` table directly, read-only, using only OpenFGA's
own indexes (`internal/search`).

Under `en_US` and other linguistic collations, Postgres ignores punctuation
and case at the first comparison level. As a result, percent-encoded IDs
such as `CN%3DALICE` sort in among look-alikes like `CN3DA0001`, and
`LIKE 'x%'` cannot use the index. The search therefore:

1. walks distinct IDs in index order starting at the prefix (a recursive
   "skip scan"),
2. checks each ID byte by byte, and
3. stops once it leaves the run of IDs whose letters and digits match the
   prefix. Every true match falls inside that run.

If it hits the scan limit first, the result is labelled **incomplete**, with
a *Keep scanning* option. It never drops matches silently. A test compares
the search against a brute-force byte-prefix query on a glibc database,
including under a tiny scan limit.

The `Searcher` interface is where a future trigram search (`pg_trgm` on a
replica) fits in. Without a datastore URI, the picker accepts exact IDs and
reports whether any tuples reference them.

## Tuple sources

Edges around an object (tuples where it is the object, or the user) come from
a pluggable `tuples.Source` (`internal/tuples`):

| Source | Filter | Counts | Notes |
|---|---|---|---|
| **API** (always available) | substring, over up to 1,000 edges per search | one page (`100+`) | OpenFGA Read API |
| **Datastore** (optional) | ID prefix, normalized per type | exact, up to 10,000 | `tuple_pkey` for edges on an object; `idx_user_lookup` (byte-order collation, exact ranges) for edges where it is the user |

`tuples.source: auto` uses the datastore when `datastore.uri` is set. The
source is an operator setting; the UI just offers "search by ID". The check walker always reads
through the API so its decisions match the server's. The datastore source is
tested against the API source (every edge, with prefix filters and paging).

## Configuration

See [`explorer.example.yaml`](explorer.example.yaml). Highlights:

- **`types.<name>`**: ID rules per type: trimming, upper or lower case,
  percent-encoding, and decoded display. For example, typing `cn=alice smith`
  becomes `CN%3DALICE%20SMITH`, and already-encoded input is left as is.
- **`auth`**: trust an identity header set by your SSO proxy and map
  identities to viewer, editor, or admin roles. v1 is read-only; the roles
  are in place for tuple and model writes in phase 2.
- **`trace`**: call budget, fanout, probe limit, timeout, and how many walks
  can run at once.
- Secrets come from the environment: `OPENFGA_API_TOKEN`,
  `OPENFGA_CLIENT_ID`/`OPENFGA_CLIENT_SECRET`/`OPENFGA_TOKEN_URL`, and
  `FGAX_DATASTORE_URI`.

## Layout

```
cmd/fgaexplorer     serve + trace commands
internal/fga        OpenFGA HTTP client (official protobuf types)
internal/schema     model analysis: relation kinds, rewrite rendering, type graph
internal/trace      check walker, text rendering, Cytoscape export
internal/cond       CEL condition evaluation (incl. OpenFGA's ipaddress type)
internal/datastore  optional read-only Postgres connection (shared)
internal/search     instance prefix search
internal/tuples     edge sources: OpenFGA API, datastore
internal/ids        per-type ID normalization and display
internal/web        handlers, templates, static assets (htmx, Cytoscape, dagre vendored)
  static/js         browser modules (ES modules, no build step): core, ui (tabs,
                    dialogs, resizers, errors), shortcuts, pickers, typematch,
                    typefinder, explore, checkform, context, trace, main
  jstest            JS unit tests (node:test)
internal/fgatest    in-process OpenFGA (memory datastore) for tests
deploy/             docker compose + sample store
```

The browser code stays small by leaving decisions to the server: which
elements form the granting path and which failed branches fold
(`trace.Graph`), what a node's detail panel says and links to, the form
summary, and the player's closing status are all computed in Go and tested
there. The modules draw and handle interaction. Each owns its own state;
`main.js` only wires page events to them, and keyboard shortcuts are
registered per page (`shortcuts.onKey`).

## Tests

```sh
make test              # everything that needs no Docker (see below)
make test-short        # quick: skips the walker/server comparison and browser tests
make test-js           # JS unit tests (Node 18+, nothing to install)
make test-integration  # the Postgres datastore and search tests, against `make up seed`
make cover             # coverage across packages
make leaks             # secret scan of history and working tree (gitleaks, via go tool)
make seed-data         # regenerate deploy/seed/*.json (go run ./deploy/seed/gen)
```

`make test` needs no Docker: `internal/fgatest` runs a real OpenFGA server
in-process (memory datastore, the same HTTP gateway and request validation)
loaded with `deploy/seed`. Against it run:

- the walker/server comparison (every user × relation × object in the sample
  store, under three budgets; zero wrong answers allowed), and the check that
  greyed-out relations are never allowed;
- golden tests of the text trace, explanation, granting path and folds
  (`internal/trace/testdata`; refresh with `go test ./internal/trace -run Golden -update`);
- handler tests that render every page and fragment (`internal/web/handlers_test.go`);
- browser smoke tests in headless Chrome (`internal/web/browser_test.go`):
  pickers, edge selection mirroring the graph, the edge chooser, check results,
  keyboard access to tabs and the help dialog, the type combobox, and network
  errors. They fail on any JavaScript error or Content-Security-Policy
  violation, and are skipped when Chrome isn't installed.

Set `FGA_TEST_API_URL` and `FGA_TEST_STORE` to run the comparison against a
live server instead.

## License

Apache License 2.0 — see [LICENSE](LICENSE) and [NOTICE](NOTICE). The bundled
browser libraries (Cytoscape.js, cytoscape.js-dagre with dagre and graphlib,
htmx) keep their own MIT/0BSD licenses; see
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md). Contributor and agent
guidance: [AGENTS.md](AGENTS.md).

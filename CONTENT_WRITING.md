# gogogo-fullstack-template
## writing style guidelines

1. style classifications

linguistic register: written entirely in lowercase letters (with exceptions only for strictly necessary acronyms or proper nouns).

textual genre: essayistic and confessional. the text assumes the perspective of a first-person learning diary or personal essay.

predominant verbal mood: subjunctive mood and hypothetical tone. the text gropes for possibilities using terms like seems, perhaps, could, signals, and suggests, rejecting dogmatic or absolute statements.

syntactic rhythm: staccato. short sentences. elimination of commas, colons, parentheses, and em-dashes. the transition of ideas is made exclusively by periods.

grammatical voice: active voice and focus on processes. preference for verbs denoting movement, construction, and investigation (e.g., unfold, anchor, thicken, grope).

factual neutral: avoid words with extreme tones, empty (or idle) adjectives, pleonasms, and redundancies. do not use superlatives (the true, certain, best, worst, always, never, the truth, the fundamental) and adverbs of certainty or impact (highly precise, precisely, obviously, clearly, fundamentally). the argument must stand on sober description, not on the force of words.

eliminate aggressive self-promotion terms and marketing bullshit strategies. i want to be anti-marketing bullshit. avoid marketing clichés and jargon. avoid a sales, marketing, or hyperbolic tone. the argument must stand on logic, not on the force of words.

2. command instructions for text generation

adopt a decentered stance: write from the first-person singular (i notice, i noted, i understood). report your own impressions and connections without trying to dictate rules, prescribe behaviors for others, or sell a definitive conclusion.

anti-post principle: does not ask for engagement, does not deliver truths, does not virtue signal, accepts being ignored. radical economy, active ambiguity. non-performative tone, assumed risk.

use the zigzag structure: continuously alternate between the abstract concept (theory, philosophy) and the concrete scene. never let the theory float without a physical example.

3. vocabulary density rule

prefer concrete tokens over abstractions. when a feature has a name (gogogo_auth, ssehub, dagnats, loro), use the token. do not paraphrase it as "the authentication helper" or "the realtime layer" when the name is shorter. the prose should feel like reading a changelog out loud. numbers count as tokens too (56 mb, 12 kib, -p 1, -race, ~700 ms). specificity is honesty.

---

## Content Angle

### Core thesis running through all angles

Gogogo is a distillation of decisions. every web project begins with the same exhausting conversation — pick a database, pick auth, pick a router, pick a reactive ui framework, pick a task queue, pick an llm sdk, pick a deploy target — and the project stalls at configuration, never at code. this template resolves those choices up front. the opinions exist to be replaced, not defended. each layer swaps independently. you ship on day one and rewrite what you outgrow.

Note: the project is deliberately opinionated. the opinions are documented as decisions, not as gospel. every choice is paired with a remove-by instruction. a template you can disagree with is more valuable than one you must agree with.

---

### I. Functionality / Technical

these angles describe what the binary does, mechanically. each pairs a claim with a number, a path, or a build flag.

- **"one binary, zero external services"** — pocketbase (database + auth + api) runs embedded. goqite (task queue) runs on the same sqlite. nats jetstream and dagnats run in-process on `:4222` or `:8090`. no postgres, no redis, no separate broker, no docker-compose with five containers. the binary lands around ~56 mb and ships on a scratch image (healthcheck is `cmd ["/app","health"]`, no curl, no wget, no shell).
- **Five realtime layers that coexist** — pocketbase realtime (record mutations, per-user scoped by collection `owner` rule), sse hub (ephemeral signals: toasts, clients count, llm feedback, workflow progress, self-patches), nats app_crud stream (cross-instance record convergence via crudpublisher + crudconsumer), nats todos stream (cross-instance ephemeral broadcast), service worker + background sync (web offline replay via `web/resources/static/sw.js`). each layer solves a different problem. they are not alternates. they coexist in the same binary.
- **Six async primitives that coexist** — goqite (jobs + sse), dagnats (durable workflows), loro crdt (collaborative docs), pb realtime (record push), sse hub (ephemeral fan-out), jetstream (cross-instance realtime). the diagram in the readme shows them as a stack, not a menu.
- **SCOPE taxonomy as executable architecture** — every source file carries a `SCOPE:core` / `SCOPE:pluggable` / `SCOPE:feature` annotation at the top. the annotation tells an agent not just what the file does but how to delete it. this is architecture as code, not as a wiki page that drifts. a feature file with `SCOPE:feature` is a file with a built-in delete instruction.
- **Two opt-out strategies, deliberately split** — infrastructure components have runtime env vars in `config/config.go` (`NATS_ENABLED=false`, `DAGNATS_ENABLED=false`, `OFFLINE_SYNC_ENABLED=false`, `SIMULATE_LLM=false`). product features have no runtime flag — you remove them by deleting the directory and dropping one line from `router/router.go` → `Init()`. the split is intentional. you toggle infra in production without rebuilding. you delete features when you outgrow the demo.
- **Unified build, no build tags** — `go build ./cmd/web` (or `make build`) compiles everything into one binary. no `-tags=...` matrix, no stub files, no conditional compilation. `air` runs the same compilation under live reload. the binary is always the same; what runs differs by env var.
- **datastar as the only frontend state primitive** — ~12 kib client. server-rendered html streamed over sse. no `npm install`, no webpack/vite, no hydration step. `web/resources/static/app.min.css` is built once by tailwind v4 cli and embedded via `//go:embed`. the css build is the only build step outside `go build`.
- **wails v3 as proof of unification** — the same `internal/server.Run` powers the web app and the desktop app. `cmd/desktop/main.go` boots the server in a goroutine and points a webview at it. android uses the same `main.go` (Go → `libwails.so` + webview). if your backend works in the browser, it works on the desktop, with no shim and no api split.
- **age-encrypted secrets via `~/.secrets/`** — no vault, no cloud. secrets live in `~/.secrets/<service>.env`, mode 600, loaded with `set -a; source ...; set +a`. the template ships an `internal/secrets` age-decrypted loader. on deploy, `deploy-prod.sh` regenerates the secrets file from github actions secrets — there is no secret history on disk.
- **a demo that fails loudly on purpose** — `SIMULATE_LLM=true` runs an in-process fake goai client that scripts `500 → retry → slow → 200`. the user gets to watch the retry feedback toasts end-to-end without an api key. the demo doubles as a regression test for the queue + retry path.
- **the `gogogo_auth` + `pb_auth` two-cookie puzzle** — pocketbase keeps the superuser (`_superusers`) and regular users in separate auth namespaces; sharing `pb_auth` between app and admin would clobber the admin session in the same browser (issues #5050 / #1780). so the app issues two cookies with the same token under two names — one for the app, one so `/api/realtime` authenticates as the same user. the split is documented in `features/auth/auth.go` constants and explained in the readme.
- **`make check` as a single gate** — fmt → datastar-lint → css-check → golangci-lint (27 linters) → size → deadcode → race tests. the pre-commit hook runs it on every commit; `make ci-local` runs the same gate a developer would run before a push. the linters are picked to catch the mistakes llms make most often (unchecked errors, broken context propagation, body closes, slog misuse, magic numbers, contained contexts in structs).
- **`gh-signoff` as advisory stamp** — push-to-master deploys, so the signoff is a signal not a hard gate. `make signoff` is the local green stamp; it does not block a push.
- **scratch + health as a deploy primitive** — `Dockerfile` builds on scratch, the container holds one binary, and the healthcheck is `cmd ["/app","health"]` (no `shell`, no `wget`, no `curl`). the server layout in the readme is the same for every sibling project — `bin/ compose/ env/ secrets/ data/ repo/ scripts/`.

#### Specific numbers worth quoting

- ~56 mb binary on scratch
- 12 kib datastar client
- 27 linters via golangci-lint
- 6 realtime / async layers
- 5 ambient layers (pocketbase + goqite + dagnats + loro + jetstream) compiled into one binary
- 3 sc (one-process) deliverable: web, desktop, android (same `internal/server.Run`)
- 700 ms poll interval for `pollrun` (dagnats onboarding)
- 64 default sse replay buffer size (`DefaultReplayBufferSize`)
- 64 default per-client sse queue size (`DefaultClientQueueSize`)
- 15s heartbeat (`DefaultSSEHeartbeatInterval`)
- 6 dagnats steps in the `welcomeonboarding` workflow
- ~30s health wait window after deploy
- `-race -p 1` for tests (serialized packages for dagnats engine stability)
- `make check` ~156s vs the old ~282s gate

### II. Strategy / Motivation

- **"optimize for shipping, not for being right"** — the readme says it plainly: decisions are pragmatic, not dogmatic. the template embeds choices that are good enough to ship today and easy to replace tomorrow. this is the opposite of the framework that locks you into its worldview.
- **The cost of configuration, measured** — every web project starts with picking a stack. not building features. i measured this in hours, not minutes. the template exists because the configuration tax repeats per-project and stops you from shipping.
- **From postgres dependence to sqlite liberation** — pocketbase + ncruces/go-sqlite3 means zero database setup locally. no docker, no connection strings, no migration files. the database is a sqlite file you can commit to git, copy with scp, or open with any sqlite browser. ncruces ships the extensions the template leans on (fts5, spellfix1, unicode collations) that the stock driver leaves out.
- **One binary as a deployment primitive** — one file to copy, one file to run, one health check. no dependency manager, no runtime, no sidecar. the deploy workflow in `.github/workflows/deploy.yml` is scp + atomic rename + restart + health probe. no kubectl, no helm, no argocd.
- **From the framework that locks you in to a collection you can argue with** — most web frameworks ship a worldview. this template ships a starting stack whose every piece has a remove-by instruction. when you outgrow pocketbase, you delete the `db/` directory and replace it. when you outgrow datastar, you delete `internal/datastar/` and route templ over htmx. the friction is local to that one decision.
- **The desktop app as proof of unification** — the same `main.go` powers a web app, a desktop app, and an android apk. that unification is not free, but it falls out of the unified-build discipline. if you ever ship a web app that you also want on a laptop, this is a path that exists.
- **PocketBase as the boring database choice** — pocketbase is the upstream admin UI, the rest api, the realtime channel, and the sqlite host. the admin ui is at `/_/` on the same origin. no second service to deploy. for production, a cloudflare tunnel pointed at the same path is enough.
- **Two cookbook recipes, not one** — `~/.secrets/<service>.env` for local dev (mode 600, age-decrypted). github actions secrets → on-server mode-600 file for prod (overwritten each deploy, no history). the secret never lives in a long-lived artifact. an agent that has shell access can read it; the protection is at-rest + agent containment, not magical.
- **The `$5 server as the production target** — the deploy workflow targets a small linux box reachable over tailscale. a single binary, one container, one tunnel, one domain. this is the deployment model that lets you focus on the application, not the infrastructure.
- **Outbox + replay as an offline primitive** — web clients use service worker + indexeddb + background sync. desktop clients use nats leaf node + loro crdt. both rely on the same idea: queue mutations locally, replay on reconnect, merge with crdt where conflict is possible. the offline path is not a feature; it is a transport you can swap.

### III. Design Philosophy

- **Every decision documented, every decision replaceable** — `ARCHITECTURE.md` reads like a team's decision log, not a system diagram. each component has a "remove by" instruction. `decisions.md` (in `docs/`) collects the war stories behind the weirder choices (the two cookies, the pocketbase realtime vs sse hub split, the build-tag absence). the template is a collection of agreements someone already had, written down.
- **Features in `features/`, infrastructure in `internal/`** — features depend on infrastructure, never the reverse. this is not a new idea. enforcing it with scope annotations makes it visible. a feature file knows it is replaceable. an infrastructure file knows it is shared.
- **Documentation for agents, not just humans** — scope annotations, the `ARCHITECTURE.md` entrypoint, the data directory in `docs/decisions.md`, the lint config tuned to llm mistakes — these exist to be read by ai agents navigating the codebase. the template treats agents as first-class readers of its documentation, with explicit paths for them to read first.
- **SCOPE as a deletion-friendly file header** — the SCOPE annotation is a tiny comment at the top of every source file. it tells an agent whether to remove, swap, or leave. agents that read these never waste a turn asking whether they can delete a file. humans that read these know which files are safe to refactor.
- **The zig-zag between theory and code** — the readme alternates between the philosophical claim and the concrete command. a sentence like "one binary, zero external services" is followed by a build flag, a config flag, or a directory path. the prose earns the abstraction by grounding it.
- **Anti-framework stance, explicit** — there is no framework here. each piece is independently replaceable. if you prefer chi over pocketbase's router, swap it. if you want htmx instead of datastar, swap it. if you want postgres instead of sqlite, swap it. the template is a collection of choices, not a cage.
- **The lint config as a teacher** — `golangci-lint` with 27 linters (`govet`, `staticcheck`, `gosec`, `revive`, `gocritic`, `errcheck`, `ineffassign`, `unused`, `errorlint`, `nilerr`, `bodyclose`, `contextcheck`, `containedctx`, `sloglint`, `thelper`, `testifylint`, `gocyclo`, `gocognit`, `funlen`, `noctx`, `goconst`, `dupl`, `lll`, `mnd`, `tagliatelle`, `modernize`, `nolintlint`) is configured to catch the mistakes llms make most often. `.golangci.yml` reads as a curriculum. an agent that runs `make lint` is being taught the team's go style by the compiler.
- **`gofumpt` + `goimports` as formatters, not linters** — separating format from lint means `golangci-lint run` never silently rewrites code. format is explicit. lint is gated. the gate can be configured without surprising a developer mid-edit.

### IV. Skills / Applied Knowledge

these angles teach what each layer of the stack actually does. each pairs a concept with the file path that demonstrates it.

- **Three async layers that coexist** — goqite (jobs + sse), dagnats (durable workflows), nats jetstream (cross-instance realtime). each solves a different problem. they run in the same binary without conflict because they solve different problems. the unity is in the question they answer, not in their implementations.
- **The sse hub as architectural glue** — in-process fan-out to browser tabs via go channels. per-client replay buffer. backpressure. exclude-origin broadcast. `unregisterifcurrent` (added in 0.14.0) prevents a race where the disconnect-cleanup of an old connection removes the channel of a reconnected one. the hub is the bridge between background jobs and the browser. it is not a message broker. it is a distribution primitive that lives in the same process as the workers.
- **PocketBase realtime vs sse hub** — pocketbase realtime (`/api/realtime`) handles record mutations and scopes delivery per-user by the collection's `listrule` / `viewrule`. the sse hub (`/api/todos/stream`) handles ephemeral signals. they are not alternatives. they are complementary layers for different types of state change. record mutations need auth-scoped delivery; ephemeral signals need low-latency fan-out.
- **`pbrealtimerecords` as a tiny observable** — the javascript in `features/todo/components/realtime.templ` subscribes to `pbrealtime`, calls `actions.get('/api/todos/fragment')` on `pb_connect` and on `visibilitychange`, and morphs `#todo-list`. the server replies with `datastar-selector: #todo-list` + `datastar-mode: outer`. ~30 lines of javascript. no framework.
- **dagnats as a durable-workflow example** — `welcomeonboarding` in `internal/dagnats/workflow.go` is a declarative json workflow that creates three example todos. you can kill the server mid-run and the next start resumes at the last incomplete step. renaming the go handler never orphans an in-flight run because the workflow references the handler by name string.
- **loro crdt as conflict-free state** — `internal/collab` wraps `aholstenson/loro-go` in a mutex-guarded `doc`. whiteboard shapes merge on reconnect without lww data loss. the same logic powers presence, where per-cursor updates broadcast on `app.presence.<docID>` and resolve back to per-client cursors via the sse hub.
- **datastar as a server-rendered reactivity primitive** — signals are reactive variables in the client. server patches morph server-rendered html into elements by selector. no hydration. no reconciliation. no build step for the frontend logic. `internal/datastar.RenderAndPatch` paired with a selector is the unit.
- **Age + `~/.secrets/` as a vault-less secrets primitive** — age is a small modern encryption tool with no keyserver and no config. the template expects `~/.secrets/` mode 700 with per-service env files mode 600. agents read via `set -a; source ...; set +a` scripts. secrets rotate via re-render on every deploy.
- **nats leaf node as edge sync** — when a desktop binary sets `NATS_LEAFNODE_URL`, it joins the server's cluster as a leaf. offline edits queue to local jetstream; on reconnect, the cluster replays them. no service worker needed at the edge.
- **Service worker + indexeddb + background sync as web offline** — `web/resources/static/sw.js` intercepts mutating requests when the browser is offline, queues them in indexeddb, and replays them on `sync` events. the server's crudconsumer validates each replayed mutation against pocketbase rules; replays that fail are surfaced to the ui.
- **`config/config.go` as the single source of truth** — every env var the template reads is documented in one comment block. runtime constants that are package-specific (e.g. `DefaultBaseURL` in `internal/llm/goai.go`) stay cohesionated. but all *env vars* live in `config.go`.
- **The `pbrealtime` no-replay-buffer gotcha** — pocketbase realtime has no event replay buffer. a subscription that starts after a mutation has fired will never see it. the fix is the `visibilitychange` resync in `pbrealtimerecords`, which refetches the fragment whenever the tab is shown. this is the most common cause of "the other tab doesn't update".

### V. Comparison / Contrast

useful when the reader comes from a different ecosystem.

- **vs next.js / remix (react + ssr + rpc)** — no api routes, no react, no hydration, no node runtime. you write one templ file per route; the html streams to the browser via datastar. the cost of giving up react is no virtual dom, no client-side routing, no jsx; the benefit is no npm install, no build pipeline, no hydration bugs. you can swap templ for jsx + a router and reuse the rest, but at that point you have left the template behind.
- **vs django / rails (full-stack monolith)** — no orm migration files, no model definition language, no admin scaffolding code. pocketbase's admin is a separate embedded ui you reach at `/_/`. the deployment unit is one binary, not a python+ruby pack. the trade-off: less convention, more wiring.
- **vs laravel / django + celery (web + queue)** — goqite replaces celery with sqlite-backed queues. retry-go replaces the celery retry machinery with exponential backoff + jitter that respects `clientid`. you do not need redis, rabbitmq, or a worker node.
- **vs firebase / supabase (managed realtime)** — pocketbase gives you the same realtime + auth + storage surface, but you ship the database as a sqlite file you control. no monthly bill. no vendor lock. no region pinning.
- **vs chat-based llm app frameworks (langchain / llamaindex / autogen)** — `internal/llm` is a small goai wrapper. any openai-compatible provider connects with `GOAI_BASE_URL` + `GOAI_API_KEY`. there is no agent runtime. if you need a real agent loop, you wire a goqite job that calls the llm and streams progress over the sse hub.

### VI. War Stories / Mistakes (debug-shaped)

each angle pairs a real bug with the fix and the file where it lives. these are gold for the reader because they earn the abstraction.

- **the "add doesn't show in the other tab" bug** — pocketbase realtime has no replay buffer. fix: resync on `pb_connect` and `visibilitychange` in `pbrealtimerecords`. regression tests: `TestCrossSessionCreatePropagates`, `TestRealtimeResyncWiringRendered`, `TestRealtimeResyncFragmentMorphHeaders` in `features/todo/realtime_propagation_test.go`.
- **the orphan iife syntax error that killed cross-tab realtime** — a v0.12.1 refactor removed an iife wrapper but left a stray `})();`. the script aborted. nothing loaded. fix: removed the orphan. regression test: `TestRealtimeNoOrphanIIFE`.
- **the dagnats port mistake** — `natspport: 0` disables the nats-server listener so it never says `readyforconnections`. `-1` is the random-port idiom. fix: changed to `-1`. the engine now starts in tests.
- **the toasts spamming "step x/6" every 700ms** — `pollrun` emitted `publishprogress` on every tick without state deduplication. fix: track `laststep/lastphase/lastdetail` and only emit on change. (`features/todo/handlers/onboarding.go`)
- **the toast "step 1/6" duplicated at workflow start** — `handlestart` called `publishprogress` manually and `pollrun` called it again on the first tick. fix: removed the manual call; polling owns all progress.
- **the white-board that did not load at all** — `whiteboard.js` was an external `<script>` that loaded before the inline block that set `wb_doc_id`. the guard bailed. fix: switch to `defer`. the canvas drew again.
- **the `clientcount` saying "4 online" with 2 tabs** — whiteboard and todo shared the same sse hub. `stats().clients` counted whiteboard connections. fix: `countuserclients()` filters by `userid != ""`. (`internal/queue/ssehub.go`)
- **the racy `unregister` on reconnection** — old connection's `defer unregister(clientid)` removed the new connection's channel after an `EventSource` reconnect. fix: `UnregisterIfCurrent(clientID, ch)` only removes if the channel is still the same. (v0.14.0)
- **the double whiteboard broadcast** — `applyop` already did `broadcastexcept` and `handleupdate` did another `broadcast`. peers got duplicated events. fix: `applyop` now uses `broadcast`; `handleupdate` skips the second call.
- **the syntaxerror on `actions.get` (datastar ESM proxy)** — datastar v1.2.2's embedded esm has `actions` as a proxy that resolves to the `apply` with no context. `cleanups` was `undefined`. fix: switched to a hidden `@get` button whose `.click()` carries the right context. (v0.12.1)
- **the infinite loading spinner when llm key is unset** — `handlesuggestjob` returned an error without sending `suggest_result`. fix: send the error result before returning. spinner clears.
- **the dagnats test that never started embedded nats** — see above (`natspport: -1`).
- **the `wb_doc_id missing` whiteboard bug** — same root cause as the script ordering; fix identical.

### VII. Specific Use Cases / Personas

useful when the reader asks "but who is this for?".

- **the solo founder shipping a v0** — one binary, one server, one tunnel. no devops to hire. sqlite file is the backup. ~~a single `scp`~~ a github action deploys it.
- **the team lead evaluating go for web** — start with the readme's `who this template is for` section. the lint config is a curriculum. the `decisions.md` reads as a team's retrospective.
- **the llm agent navigating an unfamiliar codebase** — read `ARCHITECTURE.md` first. respect `SCOPE:` annotations. run `make check` before editing; run `make lint` after. the `//nolint:tagliatelle` comments are intentional wire contracts; respect the reason.
- **the indie hacker who wants offline web** — web client uses service worker + indexeddb; desktop uses nats leaf node. both paths replay mutations on reconnect. merge conflicts route through loro crdt for the whiteboard.
- **the migrating rails / django developer** — features live in `features/<name>/`. handlers are pure http. ui is server-rendered templ. async is goqite or dagnats. the naming will feel familiar.
- **the security-conscious team** — secrets never sit long-term on disk. the on-server file is regenerated every deploy. supply-chain: go has no mass npm tree; every module hash-pins via `go.sum`; `govulncheck` audits it.
- **the offline-first / sync-first thinker** — read `docs/offline-sync-decision.md` (if present) or the relevant `crudproxy.go`. the principle is the same: queue local, replay on reconnect, merge with crdt where conflict is possible.

### VIII. Possible Formats

- **short twitter/x thread** — pairs with: go developers starting a web project, solo founders shipping a v0. ten to twenty tweets. each tweet is one decision + one number + one path.
- **long-form blog post (2000-3500 words)** — pairs with: general tech audience, teams evaluating go. zig-zag between philosophy and a single concrete command. best angle: "one binary, zero external services" or "the cost of configuration".
- **comparison post ("why not next.js / rails / django")** — pairs with: developers from other ecosystems. one section per framework. honest about trade-offs.
- **architecture deep-dive (sse hub vs pocketbase realtime)** — pairs with: backend engineers, agent builders. cites file paths and lint config. shows a real bug story.
- **case study: deploying one binary to a $5 server** — pairs with: indie hackers, ops engineers. focuses on `bin/ compose/ env/ secrets/ data/ repo/ scripts/` layout and `deploy-prod.sh`.
- **"lessons learned building for llm agents"** — pairs with: agent builders, team leads shipping ai workflows. talks about scope annotations as agent breadcrumbs, the lint curriculum, the no-replay-buffer gotcha.
- **"how i replaced postgresql with sqlite and shipped on day one"** — pairs with: solo founders. leans into ncruces/go-sqlite3 vs modernc.org/sqlite, pocketbase as the boring choice, no migration files.
- **changelog roundup post** — pairs with: returning readers. each release is a small story; a full post is a few of them strung together with the war stories (the racy `unregister`).
- **annotated code-walk** — pairs with: backend engineers. walks one path (e.g. create todo → realtime broadcast → fragment morph) end-to-end with file:line refs.
- **annotated decision log excerpt** — pairs with: senior engineers. quotes `docs/decisions.md` and unpacks one decision at a time.

### IX. What This Template Is Not (anti-marketing)

this block exists because half the value of the prose is the negative space.

- **not a framework.** a framework locks you in. this is a collection of replaceable choices. you read `decisions.md` and decide whether to keep each.
- **not an llm agent runtime.** `internal/llm` is a small wrapper. you wire an llm call into a handler. there is no agent loop, no tool registry, no planning layer.
- **not a low-code tool.** every line of ui is templ and datastar. there is no visual editor.
- **not a managed platform.** pocketbase stores its database as a sqlite file you control. there is no per-row pricing.
- **not a kubernetes substitute.** the deploy target is a single binary on a single server. you can run multiple instances behind a load balancer if you want — that is what `nats_enabled=true` is for — but the default is "one box, one tunnel".
- **not a clean-slate rewrite of next.js or rails.** those are mature ecosystems. this template refuses to compete on features. it competes on the configuration tax.

### X. Where This Template Wins On Numbers

the marketing-proof points (anti-marketing tone, sober description, no superlatives).

- one binary ~56 mb ships on scratch, healthchecks on `/health`.
- 27 linters tuned to llm-style mistakes, with a "make check" gate that runs in ~156s.
- six realtime/async layers coexist; you opt out via env var or by deleting one directory.
- `~/.secrets/<service>.env` mode 600; secrets regenerated every deploy.
- 12 kib client (datastar) + ~34 kb css (daisyui) + zero npm install.
- wails v3 desktop + android share `internal/server.Run` with the web binary.
- 700 ms poll cadence; 64-deep per-client replay buffer; 15 s heartbeat.
- -race -p 1 test run; dagnats engine serialized for stability.

---

### Potential Audiences (persona recap)

- **Go developers starting a web project** — see Template not Framework.
- **Solo founders shipping alone** — see $5 server + One binary as deployment primitive.
- **LLM agents navigating the codebase** — see ARCHITECTURE.md entrypoint + SCOPE annotations.
- **Developers tired of configuration** — see Decision log + The cost of configuration.
- **Teams evaluating Go for web** — see Comparison block + Lint-as-curriculum.
- **Offline-first / sync-first** — see Service worker + NATS Leaf + Loro CRDT.
- **Migrating from another ecosystem** — see Comparison block.

---

### Closing Frame (use only as opening or closing line)

this is a collection of decisions you can read, argue with, and replace. the binary ships what the choices ship. when you outgrow a choice, the `decisions.md` row tells you where to start. that is the whole offer.

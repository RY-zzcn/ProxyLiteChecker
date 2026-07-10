# ProxyLiteChecker Agent Notes

ProxyLiteChecker is the single-machine lightweight line inspired by
ProxyPoolChecker. It must remain separate from `/root/ProxyPoolChecker`; do not
write, move, delete, or reformat files in that older project while working here.

## Product Shape

- One Go service owns the web UI, API, SQLite data, proxy source fetcher, local
  checker, export endpoints, and optional local HTTP gateway.
- There is no panel/node split, no agent registration, no heartbeat, and no
  distributed dispatch queue.
- Whoever needs proxies deploys this service on that same VPS or Windows host.
  The result is meaningful from that machine's network perspective.

## Important Paths

- `cmd/proxylite`: Go server, API, checker, source fetcher, local gateway.
- `internal/checkmeta`: IP metadata and Cloudflare helper logic.
- `app/web`: static frontend served by the Go service.
- `scripts`: local development and validation helpers.
- `data`: runtime SQLite data, ignored by git.

## Codex Resume Protocol

When the user says to continue ProxyLiteChecker development, resume the roadmap,
or asks for the next version, read these files before changing code:

1. `docs/PROJECT_HANDOFF.md`
2. `docs/ROADMAP_V0.4.4.md`
3. `docs/ROADMAP_V0.4.3.md`
4. `docs/ROADMAP_V0.4.0_TO_V0.4.2.md`
5. `CHANGELOG.md`

The current canonical sequence is:

- released: `v0.4.3`, containing the state model, persistent scheduler,
  proxy-first checking, cached GeoIP enrichment, aggregate stats, lock-free
  gateway refresh, frontend polling reduction, and one-command Linux deployment
- current implementation stage: `v0.4.4` comprehensive native Web UI redesign,
  responsive data presentation, theme support, and accessibility
- no later version may start until v0.4.4 completes the existing 8899
  desktop/mobile acceptance; every stage must still complete the
  existing 8899 acceptance, commit, push, annotated tag, GitHub Release, assets,
  CI, and GHCR workflow

Code, tests, CHANGELOG, release tags, the handoff document, and then the current
roadmap are the truth order. Superseded design and progress documents have been
removed so there is only one active roadmap.

Before implementation, inspect `git status --short --branch`, confirm the real
version, and run the relevant baseline tests. Work on the first unfinished
roadmap version only.

## Mandatory Development And Release Workflow

These requirements are standing project instructions and are not optional:

1. Record progress continuously, not only at the end of a session. After every
   material work package, migration, risky refactor, or validation checkpoint,
   immediately update `docs/PROJECT_HANDOFF.md` and the active roadmap with the
   current work package, completed changes, tests already run, current blocker,
   and exactly one next executable action. Before starting a long-running test
   or release operation, record the intended command so an interrupted session
   has a precise resume point.
2. Every completed development stage must be committed and pushed to GitHub,
   then published as a new version. Completion includes the version bump,
   CHANGELOG, commit, push, annotated tag, GitHub Release, release assets, and
   GHCR tags when the release workflow provides them. A stage must not be marked
   complete while any of these required publication steps is pending. If GitHub
   credentials, network access, CI, or release assets block publication, record
   the exact blocker and leave the stage marked blocked/in progress.
3. The only integration deployment is the existing local service at
   `http://127.0.0.1:8899`. After each development stage, back up data when a
   migration is involved, rebuild/update that deployment, restart it, and test
   health, login/bootstrap, the changed APIs, and the necessary Web/gateway
   flows on port 8899. Do not deploy a second temporary ProxyLiteChecker service
   or use an alternate application port. Unit tests and migration checks may
   still use in-memory databases or backup copies; this restriction applies to
   the running integration deployment.

At the end of every implementation session, update the handoff status, roadmap
checkboxes, migrations, tests run, local 8899 deployment result, GitHub release
status, remaining risk, and the single next executable task.

## Common Commands

```bash
go test ./...
go build -o bin/proxylite ./cmd/proxylite
./scripts/preflight_check.sh
./scripts/start.sh
```

Default web address:

```text
http://127.0.0.1:8899
```

Project timestamps and runtime scheduling displays use Beijing time
(`Asia/Shanghai`).

Default target gateway ports:

```text
generic  HTTP 18080 / SOCKS5 18081
openai   HTTP 18082 / SOCKS5 18083
grok     HTTP 18084 / SOCKS5 18085
gemini   HTTP 18086 / SOCKS5 18087
claude   HTTP 18088 / SOCKS5 18089
```

## Guardrails

- Keep the implementation lightweight and single-binary friendly.
- Do not introduce ProxyPoolChecker's node/agent concepts.
- Keep frontend text focused on operation, not marketing.
- Do not commit runtime data, logs, binaries, or local environment files.

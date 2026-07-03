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

Default HTTP gateway:

```text
http://0.0.0.0:18080
```

## Guardrails

- Keep the implementation lightweight and single-binary friendly.
- Do not introduce ProxyPoolChecker's node/agent concepts.
- Keep frontend text focused on operation, not marketing.
- Do not commit runtime data, logs, binaries, or local environment files.

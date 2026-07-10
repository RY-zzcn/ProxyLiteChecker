# ProxyLiteChecker Deployment

## Local Service

Build and run:

```bash
go build -o bin/proxylite ./cmd/proxylite
./bin/proxylite
```

Optional `.env` is loaded by `scripts/start.sh`.

## Mandatory Local Acceptance Deployment

The canonical integration deployment is the existing local service at:

```text
http://127.0.0.1:8899
```

After every development stage:

1. Back up `data/` first when the stage includes a migration.
2. Rebuild or run the normal local update flow.
3. Restart the existing service bound to port 8899.
4. Verify `/health`, login and `/api/bootstrap`, changed APIs, the Web console,
   and any gateway behavior affected by the stage.
5. Record the commands and results in `docs/PROJECT_HANDOFF.md` and the roadmap.

Do not start a second temporary ProxyLiteChecker deployment and do not use an
alternate application port for integration acceptance. In-memory unit tests and
database migration copies remain allowed because they are not deployed services.

## systemd User Service

Install the user service:

```bash
scripts/install_systemd.sh
systemctl --user start proxylite.service
systemctl --user status proxylite.service
```

Logs:

```bash
journalctl --user -u proxylite.service -f
```

The service uses:

- Working directory: repository root
- Environment file: `.env`
- Binary: `bin/proxylite`
- Data directory: `data/`

## Backup And Restore

Create a backup:

```bash
scripts/backup_data.sh
```

Restore a backup:

```bash
scripts/restore_data.sh backups/proxylite-data-YYYYMMDD-HHMMSS.tar.gz
```

Stop the service before restore when running under systemd.

## Database Upgrade

Starting with `v0.4.0`, startup records explicit SQLite migrations in
`schema_migrations`. The v0.4.0 migration creates authoritative probe and target
state tables, backfills existing v0.3.4 checks, and keeps legacy tables and
quality columns for short-term rollback compatibility.

Back up the data directory before upgrading. On first startup, confirm the log
contains schema version `402001` for v0.4.2 (`401001` for v0.4.1 and `400001`
for v0.4.0). v0.4.1 adds persistent `job_runs`, `scheduler_state`, and
coordinator fairness state. v0.4.2 adds the rebuildable `ip_geo_cache`,
proxy-first atomic result bundles, and performance indexes. If a rollback is
required, stop the service,
restore the pre-upgrade backup, and then start the older binary.

Rolling back to v0.4.0 does not delete v0.4.1 task history, but the older
in-memory scheduler ignores persisted due times and backoff state.

Rolling back from v0.4.2 leaves `ip_geo_cache` unused. Restore the pre-upgrade
backup when validating a full rollback; older versions ignore the cache table
but do not understand v0.4.2 runtime behavior or diagnostics.

## Local Update

```bash
scripts/update_local.sh
```

The update script creates a backup, pulls with `--ff-only`, rebuilds `bin/proxylite`, and restarts the user service if it is active.

## Security Baseline

For public or shared hosts, set these values in `.env`:

```bash
SECRET_KEY=replace-with-a-long-random-secret
ADMIN_PASSWORD=replace-with-a-strong-password
PLC_REQUIRE_SECURE=1
```

Limit gateway ports with a firewall or security group when binding to `0.0.0.0`.

## Preflight

Run before release or service update:

```bash
scripts/preflight_check.sh
```

It checks shell syntax, version consistency, Go tests, build output, and whitespace-safe diffs.

Preflight alone does not complete a stage. The stage also requires successful
port-8899 acceptance, Git commit and push, an annotated version tag, GitHub
Release assets, and the GHCR tags produced by the release workflow.

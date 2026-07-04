# ProxyLiteChecker Deployment

## Local Service

Build and run:

```bash
go build -o bin/proxylite ./cmd/proxylite
./bin/proxylite
```

Optional `.env` is loaded by `scripts/start.sh`.

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

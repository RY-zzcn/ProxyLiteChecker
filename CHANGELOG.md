# Changelog

## 0.1.5 - 2026-07-03

- Add GHCR Docker image publishing for `main` and `v*` tags.
- Embed the web console into release binaries so Windows/Linux/macOS binaries can run directly.
- Add release asset descriptions and Windows run instructions to README and GitHub Release notes.

## 0.1.4 - 2026-07-03

- Expand built-in proxy sources from the initial stable subset to 30+ sources.
- Add a SOCKS5 gateway on `0.0.0.0:18081`, sharing the same checked proxy pool as the HTTP gateway.
- Show both HTTP and SOCKS5 gateway endpoints in the web console.

## 0.1.3 - 2026-07-03

- Change the default HTTP gateway bind address to `0.0.0.0` for Docker and LAN consumers.
- Add project icon, author attribution, and repository link to the web UI.
- Refresh the console color palette and surface styling.

## 0.1.2 - 2026-07-03

- Add GitHub Actions CI for tests, vet, and build checks.
- Add tag-driven GitHub Release workflow with multi-platform binary assets and checksums.

## 0.1.1 - 2026-07-03

- Redesign the static frontend as a single-page workbench without the left sidebar.
- Add interactive task feedback, toast notifications, source selection counts, and clearer gateway status.
- Keep default ports separate from ProxyPoolChecker: web `8899`, local HTTP gateway `18080`.

## 0.1.0 - 2026-07-03

- Bootstrap the single-machine ProxyLiteChecker service.
- Add local SQLite storage, login, proxy import, source fetch, local checks, export endpoints, and local HTTP gateway.

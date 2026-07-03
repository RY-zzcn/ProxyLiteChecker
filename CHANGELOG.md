# Changelog

## 0.2.0 - 2026-07-03

- Mark time units beside runtime settings and standardize project timestamps on Beijing time.
- Make fetch/check job creation atomic so manual and automatic jobs cannot race into concurrent heavy work.
- Add tests for job conflict scheduling, target gateway round-robin upstream selection, import deduplication, stale cleanup, and Beijing time formatting.
- Update Docker exposure and documentation for the completed lightweight single-machine release.

## 0.1.13 - 2026-07-03

- Keep quick action buttons from turning blue on hover and align source fetching with the local-check action style.
- Add automatic stale-untested proxy deletion with a configurable TTL in hours.
- Add tests covering proxy source import deduplication and stale untested cleanup.

## 0.1.12 - 2026-07-03

- Align the manual import panel height with the proxy source panel.
- Reorganize automatic task settings into grouped fetch, check, and maintenance sections with a single-row desktop target selector.
- Add copy buttons beside each HTTP/SOCKS5 local gateway address.

## 0.1.11 - 2026-07-03

- Fix GitHub Release instructions for the per-target local gateway ports and Docker port mapping.

## 0.1.10 - 2026-07-03

- Change the local gateway model to fixed per-target entrances, matching the ProxyPoolChecker gateway pattern without adding node/panel concepts.
- Expose default target ports as generic `18080/18081`, OpenAI `18082/18083`, Grok `18084/18085`, Gemini `18086/18087`, and Claude `18088/18089`.
- Redesign the single-page console layout with a cleaner quick-operation area, structured check/export parameters, compact configuration panels, and target gateway cards.
- Keep export target selection isolated to export links and persisted runtime settings so it does not affect check scheduling.

## 0.1.9 - 2026-07-03

- Add per-target proxy check storage so one proxy can keep separate results for generic, OpenAI, Grok, Gemini, and Claude targets.
- Support multi-target manual and automatic check jobs from the web console.
- Add target-aware HTTP/SOCKS5 gateway upstream selection with live recent-upstream display.
- Add target-aware TXT / JSON export controls and query handling.
- Reorder the single-page console so proxy source, import, and settings stay compact, gateway status sits above the repository, and the proxy repository remains last.

## 0.1.8 - 2026-07-03

- Add visible quick-check controls for check range, target profile, and batch size beside the local check action.
- Sync quick-check controls with the full runtime settings panel.
- Tighten dashboard panel spacing and reduce action card height for a denser single-page console.
- Replace the bright local-check button background with a quieter status-style treatment.

## 0.1.7 - 2026-07-03

- Add automatic failed-proxy cleanup with optional immediate delete on failed check results.
- Add available-proxy expiry settings that move stale available proxies back to untested and prioritize them before newly imported untested proxies.
- Add low-stock source fetching that starts an automatic fetch when untested proxy count falls below a configurable threshold.
- Add UI controls and scheduler status for cleanup, expiry, low-stock threshold, and fetch cooldown settings.

## 0.1.6 - 2026-07-03

- Add persisted runtime settings for proxy list page size, source fetch limits, check scope, target profile, batch size, concurrency, rounds, and timeouts.
- Add automatic source fetch and automatic proxy check scheduling with conflict handling against manual long-running tasks.
- Redesign the proxy repository area as a fixed-height paginated table so large imports no longer stretch the page.
- Add a compact web settings panel for auto tasks and detection parameters.

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

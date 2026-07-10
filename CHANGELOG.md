# Changelog

## Unreleased

## 0.4.1 - 2026-07-10

- Persist job history, parameters, progress, terminal results, errors, parent relationships, and process instance IDs in SQLite with restart-safe autoincrement IDs.
- Mark jobs left active by a previous process as `interrupted`, preserve their history, and schedule at most one recovery catch-up.
- Add a single heavy-work coordinator that keeps cancellation slots until worker acknowledgement, merges duplicate automatic intents, and alternates fetch/check grants to prevent starvation.
- Persist scheduler due times, completion outcomes, pending reasons, backoff state, failure counters, and last job IDs; calculate future runs from actual terminal outcomes.
- Add target low-stock settings that check existing candidates before fetching and optionally create a parented check pipeline only when a fetch adds proxies.
- Add job history and scheduler status APIs, recent persistent jobs in bootstrap, and Web UI support for `cancelling`, `interrupted`, blocking jobs, pending reasons, and backoff.
- Add migration, restart recovery, progress throttling, terminal persistence, history pagination, cancellation-slot, fairness, low-stock, pipeline, fake-clock, API, and scheduler backoff tests.

## 0.4.0 - 2026-07-10

- Add explicit, transactional schema migrations with Beijing-time migration records and startup schema-version logging.
- Split proxy identity from authoritative `proxy_probe_state` and `proxy_target_state` records while retaining legacy quality columns and `proxy_checks` as rollback shadows.
- Backfill v0.3.4 data without losing proxy identities, preserve base-only capability for diagnostics, and strictly reclassify named-target base-only records as unavailable.
- Make target lists, exports, gateway pools, low-stock counts, deletion, TTL transitions, and global availability statistics read the correct probe or target state dimension.
- Add nested probe, target state, and target summary fields to proxy responses; distinguish transport availability from unique target availability in statistics and the Web console.
- Track probe and target status transition times independently, require repeated base failures for periodic cleanup, and record probe/target requeue maintenance separately.
- Add migration idempotency, real v0.3.4 database, atomic state write, strict capability, independent lifecycle, gateway/export authority, and cross-target isolation coverage.
- Add a canonical project handoff document, the v0.4.0-v0.4.2 roadmap, and a repository-level resume protocol; remove superseded planning documents.

## 0.3.4 - 2026-07-10

- Require named target profiles to reach their Web or API endpoint before they enter target exports and gateway pools; retain base-only capability for diagnostics without treating it as target availability.
- Migrate legacy base-only target records to the corrected status model and aggregate global proxy status safely across target checks.
- Prevent one target failure from deleting a proxy that remains usable for another target, and require complete persisted base failure before automatic immediate deletion.
- Add `cancel_requested`, `partial`, and reliable terminal job handling so cancellation keeps the heavy-task lock until workers actually stop.
- Report all-source fetch failure and result-persistence failure accurately instead of displaying every stopped job as successful.
- Poll concrete job IDs in the Web UI and show distinct completed, partial, failed, and cancelled outcomes without overlapping polling cycles.
- Add status transition timestamps so requeued proxies receive a full untested TTL, reduce maintenance cadence, and avoid maintenance/check write races.
- Make low-stock counts target-aware, prioritize scheduled checks before fetches, and preserve schedules when unrelated settings are saved.
- Add migration, target availability, safe deletion, cancellation, partial-result, lifecycle, and scheduler regression coverage.
- Add a detailed `v0.3.4` optimization and acceptance plan.

## 0.3.3 - 2026-07-07

- Restore equal-height dashboard, source/import, task, settings, and gateway panel alignment after the UI polish pass.
- Move runtime settings into a four-column desktop layout to remove the large empty area left by the wrapped gateway panel.
- Keep gateway recent-upstream cards aligned with a fixed three-row compact history.
- Change the automatic-check performance controls to a readable 2x2 grid.

## 0.3.2 - 2026-07-07

- Refine the Web UI visual system with more restrained colors, softer surfaces, and clearer interactive states.
- Tighten dashboard, quick action, settings, proxy source, and gateway card spacing to reduce uneven alignment and empty areas.
- Compact gateway recent-upstream and diagnostic rows while keeping the waiting state visible.
- Improve responsive layout consistency for the single-page console.

## 0.3.1 - 2026-07-07

- Add GeoIP-backed country detection for imported and checked proxies.
- Add country filters and strict/fallback policies for local gateway upstream selection.
- Add Web UI controls and export parameters for country-limited proxy output.
- Add GeoIP database download, refresh, and configuration settings.
- Add regression coverage for country metadata storage and gateway filtering.

## 0.3.0 - 2026-07-04

- Add gateway selector state with active and isolated upstream counts in the status API and web console.
- Add configurable gateway upstream strategies, retry attempts, failure threshold, and failure cooldown settings.
- Retry CONNECT, SOCKS5, and retry-safe HTTP requests on the next upstream when an upstream fails, without repeating the same upstream in one request.
- Temporarily isolate upstreams after consecutive failures and release the isolation window if every loaded upstream is isolated.
- Add runtime gateway config persistence, `GET/POST /api/gateway/config`, and Web UI controls for hot-applied gateway settings.
- Split gateway counters into total connections, valid requests, rejected requests, upstream attempts, successes, and failures.
- Add in-memory gateway diagnostic events for recent rejected and failed requests.
- Track source fetch health, consecutive failures, automatic source cooldown, and maintenance audit events.
- Add failure reason classification for proxy checks and expose read-only target profile metadata.

## 0.2.4 - 2026-07-04

- Show gateway addresses as complete `http://` and `socks5://` URLs and rename the section to "代理网关".
- Keep each gateway target on a stable in-memory upstream pool between refreshes so round-robin requests are not reshuffled by every database read.
- Report loaded gateway upstreams from the current runtime pool while keeping available upstream counts as the database-wide unique inventory.

## 0.2.3 - 2026-07-04

- Show the active per-target gateway upstream limit in the local gateway summary.
- Add a restart-required hint for changes to `PLC_GATEWAY_UPSTREAM_LIMIT`.
- Add the same upstream-limit hint to the gateway status pill hover text.

## 0.2.2 - 2026-07-04

- Count dashboard available proxies as unique usable upstream URLs across all target checks, with the previous record count kept separately for diagnostics.
- Report local gateway loaded target slots, unique available upstreams, and per-target available counts separately so repeated target coverage is not misread as extra global inventory.
- Deduplicate gateway upstreams before applying the per-target upstream limit, so each target can load the full configured number of unique upstreams.
- Apply TXT export limits after deduplication and add tests for target-aware availability statistics and gateway aggregation.

## 0.2.1 - 2026-07-03

- Show local gateway recent upstreams as a stable five-row newest-first list with the current/latest proxy first.
- Reduce gateway status polling to two seconds for fresher live gateway state without changing persisted proxy data.
- Add coverage for gateway recent-upstream ordering and retention.

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

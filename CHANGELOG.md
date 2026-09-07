# Changelog

## 0.2.1 (unreleased)

- Tooltips on every control, field, chip and status element in the UI (hover anything for an explanation).

## 0.2.0 (2026-09-06)

First release with installers and the in-app updater.

- **Graywolf API transport** (new default). Emcomm Objects logs in to Graywolf's REST API like the web UI does and asks it to send object beacons; Graywolf handles AX.25, the channel, digipeating and iGating. Works with a stock Graywolf install: no KISS interface to configure. Kill packets go out as Graywolf *custom* beacons carrying the spec-exact `;NAME _…` info field. The KISS-over-TCP transport is still available and can be switched from Settings without a restart.
- **Settings in the browser.** Callsign, transport, Graywolf login (with *Test connection* and a channel picker), KISS address, web address, browser-on-start and update preferences. A fresh install opens Settings by itself; no text editor needed.
- **Redesigned UI.** Object cards with live countdowns, status chips (live / due / expiring / killing / killed / disabled), search and filter, a searchable symbol chart with names, dark and light themes, toasts instead of alert boxes, an Activity tab with the packet history and the server log, a status bar with version and data folder. Leaflet is bundled, so the app renders without internet (map tiles still come from OpenStreetMap when online).
- **Updater.** Checks GitHub at start and every 12 h (only the version number is fetched). On Windows the *Update* pill downloads the release, verifies its SHA-256 against `SHA256SUMS.txt`, closes, installs (installer) or swaps the executable (portable) and reopens. Other platforms get the download link. *Skip this version* is remembered.
- **Releases.** Windows installer (per-user, no admin, optional start-with-Windows), portable zip, macOS (Apple Silicon and Intel), Linux x64 and arm64 (Raspberry Pi). Data for the installed Windows build lives in `%LOCALAPPDATA%\EmcommObjects` and survives upgrades, matching VarMap.
- **API for companion tools.** `GET /api/objects` (Pinpoint-shaped JSON), `GET /api/objects.csv`, `GET /api/status`, `GET /api/version`, SSE at `/api/events`.
- Beacon all, CSV export, altitude field, "beacons deferred" instead of a warning storm while the radio is down or the callsign is unset, log file in the data folder (Windows builds run without a console window), `--no-browser`, `--data` and single-instance detection (a second start just opens the running UI).
- Scheduler wakes immediately when the transport connects or an object is saved, instead of waiting for the next 10 s tick.
- Removed the goreleaser pipeline in favour of a plain Go cross-compile plus Inno Setup workflow.

## 0.1.0

- Initial scaffold: AX.25/KISS/APRS object beacons over Graywolf's KISS port, web UI with map, symbol picker, CSV import, kill semantics and auto-expiry.

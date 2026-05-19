# emcomm-objects

[![Release](https://img.shields.io/github/v/release/KK4ODA/emcomm-objects?display_name=tag&sort=semver)](https://github.com/KK4ODA/emcomm-objects/releases/latest)
[![CI](https://github.com/KK4ODA/emcomm-objects/actions/workflows/ci.yml/badge.svg)](https://github.com/KK4ODA/emcomm-objects/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

Companion app for [Graywolf APRS](https://github.com/chrissnell/graywolf): manage and beacon APRS objects (events, served agencies, deployed units) via Graywolf's KISS port.

Inspired by the object-management UX in Pinpoint APRS and YAAC, but built for use alongside Graywolf rather than as a full APRS client.

## What it does

- Stores APRS objects in a JSON file compatible with Pinpoint APRS's `pinpointAprsObjects.json` format.
- Beacons each object on a configurable interval (per object).
- Sends frames as standard KISS over TCP to Graywolf, which transmits them to RF (and, with Graywolf's iGate enabled, gates them to APRS-IS).
- Exposes a local web UI for adding, editing, placing (on a map), and manually beaconing objects.

## What it does *not* do

- Talk to APRS-IS directly. Graywolf's iGate handles RF→IS gating.
- Receive or display incoming APRS traffic. That's what Graywolf's web UI is for.
- Manage Graywolf itself.

## Status

Pre-alpha. Not yet usable.

## Setup

Requires Go 1.22+ and a running Graywolf instance.

### Graywolf side (one-time)

In Graywolf's web UI (http://127.0.0.1:8080/), add a **KISS Interface**:

| Field | Value |
| --- | --- |
| Type | TCP |
| Mode | **modem** (not tnc) |
| Listen addr | `127.0.0.1:6700` |
| Channel mapping | KISS port 0 → your VHF APRS channel |

`mode: tnc` will silently drop our injected frames — must be `modem`.

### emcomm-objects side

**Option A: download a prebuilt binary** (recommended)

Grab the archive for your OS/arch from the [latest release](https://github.com/KK4ODA/emcomm-objects/releases/latest). Extract; copy `config.example.yaml` to `config.yaml`; edit it (set your callsign-SSID, Graywolf KISS host:port, objects file path); run:

```
./emcomm-objects                 # Linux/macOS
emcomm-objects.exe               # Windows
```

**Option B: build from source** (Go 1.22+ required)

```
git clone https://github.com/KK4ODA/emcomm-objects
cd emcomm-objects
go build ./cmd/emcomm-objects
cp config.example.yaml config.yaml
# edit config.yaml
./emcomm-objects
```

Then open <http://127.0.0.1:8765/>.

## Third-party assets

This app embeds the APRS symbol sprites from
[hessu/aprs-symbols](https://github.com/hessu/aprs-symbols) by Heikki
Hannikainen (OH7LZB). See `internal/web/ui/symbols/COPYRIGHT.md` for the
full attribution; the set is derived from Stephen Smith's (WA8LMF)
canonical APRS symbol set.

## License

TBD.

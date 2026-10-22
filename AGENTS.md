# AGENTS.md

Guide for AI coding agents (and humans) working on or with AyrPlay. AyrPlay mirrors a
Windows screen + system audio to an Apple TV, or streams audio only to a HomePod,
over AirPlay 2. It is a fork of [omarroth/doubletake](https://github.com/omarroth/doubletake).

## Set up (any OS, ~1 minute)

- Install Go at the version in `go.mod` (1.25+). That's all: the build is pure Go
  (`CGO_ENABLED=0`), and the tests need no GStreamer, GPU, or AirPlay hardware.
- `go mod download` once if the machine will be offline later.

## Everyday commands

| Task | Command |
| --- | --- |
| Full check (what CI runs) | `make check` — `go vet` for Linux **and** Windows, then `go test ./...` |
| Tests only | `go test ./...` |
| Build `AyrPlay.exe` (x64 + arm64) from any OS | `make windows` → `bin/AyrPlay.exe`, `bin/AyrPlay-arm64.exe` |
| Build for this OS | `make ayrplay` → `bin/ayrplay` |
| Windows resources (icon/manifest) | `make winres` (run by `make windows`) |

Always run `GOOS=windows go vet ./...` (part of `make check`) after touching code with
build tags — a Linux-only `go test` will not notice a broken Windows build.

## Layout

| Path | What it is |
| --- | --- |
| `cmd/ayrplay` | **The single AyrPlay executable**: no args → tray; `discover`, `doctor`, `send`, `tray`, `version`, `help` subcommands. |
| `internal/sender` | Command-line sender (flags → discovery → pairing → mirror / audio-only stream). Shared by `ayrplay send` and `cmd/doubletake`. |
| `internal/tray` | System-tray UI. `logic.go` holds the UI-free, unit-tested parts (settings, sender args, PIN protocol, status text). |
| `internal/gstreamer` | Finds an installed GStreamer runtime without PATH edits; `doctor` checks. |
| `internal/airplay` | AirPlay protocol core (RTSP, pairing, FairPlay, PTP/NTP, video/audio streams, capture pipelines) and the in-process **test receiver** (`receiver_*.go`). |
| `internal/daemon`, `cmd/doubletake-ctl` | Upstream Linux daemon mode. |
| `cmd/doubletake` | Upstream-named CLI (same sender), kept for Linux packaging. |
| `cmd/doubletake-test-receiver` | Standalone fake AirPlay receiver for manual end-to-end runs. |

## Conventions

- Platform code uses file-name/build-tag pairs (`*_windows.go` + `*_other.go` / `!windows`).
  Keep pure logic in untagged files so it is testable on Linux (see
  `internal/airplay/capture_windows_pipeline.go`).
- Add tests with every behavior change. Protocol changes should be exercised against the
  test receiver (`newReceiverServerTestPair` in `internal/airplay/receiver_server_test.go`);
  the `homepod` profile covers audio-only sessions.
- `gofmt` everything you touch.
- **Never commit binaries** — no `.exe`, no generated `.syso`. Release executables are built
  only by CI from tagged source and published with `SHA256SUMS.txt`.
- License is LGPL-3.0-or-later (inherited); don't add code under incompatible licenses.

## Driving AyrPlay for a user (agents and scripts)

All commands are non-interactive when given flags; they print JSON with `-json`.

1. `ayrplay doctor -json` — exit 0 when GStreamer and every needed plugin are present.
   `modes[].ready` says whether `mirror` and/or `audio-only` will work; `hint` says how to fix it.
   (Windows install: `winget install --id gstreamerproject.gstreamer -e`, or the MSVC runtime
   from https://gstreamer.freedesktop.org/download/. No PATH change is needed.)
2. `ayrplay discover -json [-timeout 5s]` — receivers on the LAN. Each entry has `name`, `ip`,
   `port`, `model`, `mode` (`mirror` or `audio-only`) and `send_args`.
3. `ayrplay send <send_args...>` — streams until the process is stopped (Ctrl+C / kill).
   Useful flags: `-audio-only`, `-video-codec hevc` (4K), `-target-latency-ms 2000`,
   `-monitor N`, `-no-audio`, `-debug`. `ayrplay send -h` lists all.
4. Pairing: a receiver showing a PIN needs it once. Set `DOUBLETAKE_CODE=<pin>` in the
   environment (preferred over `-code`). Credentials are then saved
   (`~/.config/doubletake/credentials.json`, i.e. `%USERPROFILE%\.config\doubletake` on Windows).
   For a live prompt, `-machine-prompts` prints `@@DOUBLETAKE_NEED_CREDENTIAL:pin@@` on stdout
   and reads the value as one line on stdin; `@@DOUBLETAKE_STATUS:paired@@` marks success.

Exit codes: `0` success, `1` failure (`doctor`: setup incomplete), `2` usage error.

### Try it without hardware

```sh
go run ./cmd/doubletake-test-receiver -profile homepod -listen 127.0.0.1:7000   # or -profile modern
go run ./cmd/ayrplay send -target 127.0.0.1 -audio-only -test                 # -test needs GStreamer
```

## Releasing

Push a `vX.Y.Z` tag. CI tests on Linux and Windows, cross-builds `AyrPlay.exe` /
`AyrPlay-arm64.exe`, and attaches them with `SHA256SUMS.txt` to the GitHub release that
`scripts/install.ps1` downloads from.

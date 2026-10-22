# AyrPlay

Mirror your Windows screen and system audio to an Apple TV (tested on Apple TV 4K 3rd gen), or stream just audio to a HomePod, over AirPlay 2.

AyrPlay is a Windows front-end for AirPlay sending, built on top of [omarroth/doubletake](https://github.com/omarroth/doubletake) (a Linux-only AirPlay mirroring sender). It adds Windows screen and audio capture, a windowless system-tray app that finds receivers on your network and routes to them automatically, and an audio-only mode for speakers. It's a personal-interoperability tool for sending to your own AirPlay 2 hardware.

Everything ships as **one file, `AyrPlay.exe`**: double-click it for the tray app, or run it from a terminal (`AyrPlay.exe send …`, `discover`, `doctor`) for scripts and AI agents.

- Repository: https://github.com/ayaanh03/AyrPlay (default branch `main`)
- Upstream: https://github.com/omarroth/doubletake

## Status / tested on

This is early and only lightly verified. What has actually been tested:

- **Sender:** Windows 11 PC with an NVIDIA GPU (NVENC).
- **Receivers:** Apple TV 4K (3rd gen, 2022, A15) for screen mirroring, and a HomePod for audio-only.

Earlier Apple TV models, other AirPlay 2 receivers, and the software (non-NVENC) encoders are **unverified on Windows**. Treat anything beyond the combinations above as unproven.

## Features

- Windows screen mirroring, H.264 or HEVC, hardware-encoded via NVENC.
- System-audio mirroring alongside the screen (WASAPI loopback of the system mix).
- Audio-only mode: stream system audio to a HomePod with no video (other AirPlay 2 speakers should work but are untested).
- System-tray GUI that discovers receivers via mDNS and auto-routes a HomePod to audio-only and an Apple TV to full mirroring.
- Display picker for multi-monitor setups.
- Quality and buffer presets (1080p vs 4K; latency vs smooth vs movie).
- Optional debug logging you can open and share.
- Finds an installed GStreamer automatically (no PATH editing) and checks your setup (`AyrPlay.exe doctor`, or **Check setup…** in the tray).
- Script/agent friendly: `discover -json`, `doctor -json`, and stable exit codes (see [AGENTS.md](AGENTS.md)).

## Getting AyrPlay

You need two things: **`AyrPlay.exe`** and the free **GStreamer runtime** (which AyrPlay uses for capture and encoding).

**Option A — one-line install (recommended).** In PowerShell:

```powershell
irm https://raw.githubusercontent.com/ayaanh03/AyrPlay/main/scripts/install.ps1 | iex
```

It downloads `AyrPlay.exe` from the latest [GitHub release](https://github.com/ayaanh03/AyrPlay/releases), checks it against the release's `SHA256SUMS.txt`, installs GStreamer with winget if it's missing, adds a Start menu shortcut and an `ayrplay` command, and starts the tray. No admin rights needed; everything goes in `%LOCALAPPDATA%\Programs\AyrPlay`. [Read the script](scripts/install.ps1) first if you like — it's short. Remove it again with `-Uninstall`.

**Option B — download it yourself.** Grab `AyrPlay.exe` (or `AyrPlay-arm64.exe` for ARM PCs) from the [Releases page](https://github.com/ayaanh03/AyrPlay/releases), optionally verify it (`Get-FileHash AyrPlay.exe` should match `SHA256SUMS.txt`), put it anywhere, and [install GStreamer](#installing-gstreamer).

**Option C — [build from source](#build-from-source).**

> Release executables are built only by CI from the tagged source code — no binary is ever committed to this repository. Only download AyrPlay from this repository's Releases page.

## Requirements

**Common (both modes):**

- **Windows 11.**
- **The GStreamer MSVC runtime**, version **1.22 or newer** (the d3d11, wasapi2, and nvcodec elements only exist in recent builds — install the latest 1.x to be safe). AyrPlay runs GStreamer as a separate process for all capture and encoding. It finds a standard install by itself; see [Installing GStreamer](#installing-gstreamer).
- **An AirPlay 2 receiver on the same subnet** as the PC, with AirPlay enabled on it. Discovery uses mDNS, which does not cross subnets.

**For audio-only (HomePod / AirPlay 2 speaker):** nothing more — no GPU and no video plugins. The only GStreamer element required is `wasapi2src` (the **wasapi2** plugin).

**For screen mirroring:** additionally the **d3d11** capture plugin and a video encoder:

- **NVIDIA GPU (NVENC)** — strongly recommended and the only tested configuration (`nvh264enc` / `nvh265enc`, from the **nvcodec** plugin).
- Software encoders (`openh264enc`, `x264enc`, `x265enc`) work as a slower fallback when no NVENC is present (`-hwaccel openh264` or `-hwaccel none`), but are **untested on Windows**.

## Installing GStreamer

You only need the GStreamer **runtime** (not the development files). Install the latest **MSVC 64-bit** runtime:

- **gstreamer.org installer (recommended):** download the MSVC x86_64 runtime installer from [gstreamer.freedesktop.org/download](https://gstreamer.freedesktop.org/download/) and run it. (Older `.msi` installers ask for an installation type — choose **Complete** so the d3d11, nvcodec, wasapi2, videoparsersbad, and rtp plugins are all included.)
- **winget:** `winget install --id gstreamerproject.gstreamer -e`

**No PATH changes or sign-out are needed.** AyrPlay looks for `gst-launch-1.0.exe` on `PATH` and, failing that, in the installer's recorded root (`GSTREAMER_1_0_ROOT_MSVC_X86_64`), the per-user default (`%LOCALAPPDATA%\Programs\gstreamer\1.0\msvc_x86_64`), the all-users default (`%ProgramFiles%\gstreamer\1.0\msvc_x86_64`), the legacy `C:\gstreamer\1.0\msvc_x86_64`, and a `gstreamer\bin` folder next to `AyrPlay.exe`.

### Verify

Run the built-in check (or use **Check setup…** in the tray menu):

```powershell
AyrPlay.exe doctor
```

It lists every element each mode needs and whether it was found; it exits `0` when everything required is installed. Audio-only (HomePod) needs just `wasapi2src` plus the audio converters; mirroring also needs the d3d11 capture elements and an H.264 encoder (NVENC `nvh264enc` preferred). If something is missing, reinstall the latest runtime (on old `.msi` installers, with **Complete**).

The Windows mirroring path uses these GStreamer elements (element names are exact; plugin groupings are the usual GStreamer layout):

- Video capture: `d3d11screencapturesrc`, `d3d11download` (typically the **d3d11** plugin in gst-plugins-bad)
- Encoders: `nvh264enc`, `nvh265enc` (NVENC); software fallbacks `openh264enc`, `x264enc`, `x265enc`
- Parsers: `h264parse`, `h265parse`
- RTP payload/timestamp: `rtph264pay`, `rtph265pay`, `rtpL16pay`, `audiobuffersplit`, `rtponviftimestamp`, `rtpstreampay`
- Audio capture: `wasapi2src` with `loopback=true` (the **wasapi2** plugin)

## Quick start (GUI, recommended)

1. Double-click `AyrPlay.exe` (or the **AyrPlay** Start menu entry if you used the installer). A console window flashes for a moment, then AyrPlay lives in the system tray (tooltip: `AyrPlay — AirPlay sender`; click **^** near the clock if the icon is hidden). There is no main window. Launching it again just tells you it's already running.
2. The first time, Windows Firewall may ask about `AyrPlay.exe` — allow it on **Private** networks so it can find and stream to your receivers.
3. Open the tray menu. Top to bottom:
   - A **status** line (`Idle`, then `Scanning…`, then e.g. `Idle — 2 receiver(s)`). If GStreamer is missing it says so — use **Check setup…**. If a session fails, it shows the reason (e.g. `Stopped: audio capture failed: …`).
   - A **Receivers** list — click a device to start. An Apple TV starts screen mirroring; a HomePod starts audio-only automatically. (The list shows up to **12** receivers; more are discovered but not listed.)
   - **Rescan** to search again.
   - **Quality**: `1080p (Wi-Fi friendly)` (default) or `4K (wired recommended)`.
   - **Buffer**: `Low latency (0.2s)`, `Smooth (1s)` (default), or `Movie (2s)`.
   - **Display**: `Display 1`, `Display 2`, … — pick which monitor to mirror.
   - **Stream audio**: on by default; uncheck to mirror video only.
   - **Debug logging**: when on, writes a verbose log for the run.
   - **Open logs folder**: opens the log/settings folder in Explorer.
   - **Check setup…**: checks GStreamer and its plugins, and offers the download page if something is missing.
   - **Stop mirroring** (enabled only while running) and **Quit**.

> **Pairing a PIN/password receiver.** When the Apple TV shows a PIN (or a receiver needs its configured password), the tray pops a dialog asking for it — type what the TV shows and click OK. Credentials are then saved, so later connects are automatic. PIN-less (transient) receivers connect with no prompt. If you mistype the PIN, the status reads "Pairing didn't complete — click the device to retry".

The tray's settings and debug log live under `%AppData%\doubletake\`:

- `%AppData%\doubletake\tray.json` — your menu choices.
- `%AppData%\doubletake\doubletake.log` — the debug log (written only when **Debug logging** is on; overwritten each run).

(Pairing credentials are stored elsewhere — see [Command-line usage](#command-line-usage-power-users).)

## Command-line usage (power users, scripts, and agents)

The same `AyrPlay.exe` is the command-line tool:

```text
AyrPlay.exe                      Start the tray app (same as double-clicking)
AyrPlay.exe tray                 Run the tray in the foreground, logging to the console
AyrPlay.exe discover [-json]     List AirPlay receivers on the network
AyrPlay.exe doctor [-json]       Check GStreamer and its plugins
AyrPlay.exe send [flags]         Stream to a receiver (see the flags below)
AyrPlay.exe version | help
```

Exit codes: `0` success, `1` failure (`doctor`: setup incomplete), `2` usage error. `discover -json` returns, for each receiver, its `name`, `ip`, `port`, `model`, the `mode` the tray would use (`mirror` or `audio-only`), and ready-to-use `send_args`. See [AGENTS.md](AGENTS.md) for the full agent workflow.

`send` with no `-target` starts an mDNS discovery scan (about 5 seconds) and lets you pick a device interactively; passing `-target` skips discovery. (Flags also work without the word `send`, exactly like upstream `doubletake`.)

Windows-relevant flags:

| Flag | Default | Purpose |
| --- | --- | --- |
| `-target` | `""` | Apple TV IP address or hostname (skip discovery). |
| `-port` | `7000` | AirPlay port (overwritten by the discovered port when discovery is used). |
| `-video-codec` | `auto` | Screen codec: `auto`, `h264`, or `hevc` (`auto` uses H.264 for receiver compatibility). |
| `-bitrate` | `0` | Video bitrate in kbps (`0` = auto, tuned for resolution/FPS). |
| `-fps` | `30` | Frames per second. |
| `-hwaccel` | `auto` | Encoder: `auto`, `nvenc`, `vaapi`, `openh264`, `none` (x264/x265). On Windows the hardware path is `nvenc`; software options are untested. |
| `-monitor` | `0` | Windows: 0-based index of the display to mirror. |
| `-no-cursor` | `false` | Don't show the mouse cursor in the captured video. |
| `-no-audio` | `false` | Disable audio streaming (video only). |
| `-audio-only` | `false` | Stream system audio only (HomePod / AirPlay speaker): no screen capture, encoder, or video stream. |
| `-target-latency-ms` | `0` | Joint audio/video playout latency override in milliseconds (`0` = automatic). |
| `-port-range` | `""` | Local UDP port range for receiver timing/audio, e.g. `60000-60010`; needs at least 3 ports (empty = OS-assigned). |
| `-code` | `""` | Pairing PIN shown on the receiver, or the password set on it; prefer the `$DOUBLETAKE_CODE` environment variable. |
| `-pair` | `false` | Force new pairing even if saved credentials exist. |
| `-creds` | (auto) | Path to saved pairing credentials. |
| `-cred-backend` | `file` | Credential storage backend: `file` or `keyring`. |
| `-debug` | `false` | Verbose debug logging. |
| `-test` | `false` | Use synthetic video (`videotestsrc`) instead of screen capture, for debugging. |

`-audio-only` and `-no-audio` are mutually exclusive; passing both exits with an error. (The binary also carries some Linux/X11 and daemon flags that have no effect on Windows. `-machine-prompts` makes PIN/password requests machine-readable for a front-end: the sender prints `@@DOUBLETAKE_NEED_CREDENTIAL:pin@@` (or `password`) on stdout and reads the value as one line on stdin, and prints `@@DOUBLETAKE_STATUS:paired@@` once pairing succeeds — the tray uses this, and so can scripts.)

Examples:

```powershell
# Find receivers (add -json for machine-readable output)
.\AyrPlay.exe discover

# Basic 1080p mirror (auto codec = H.264) to a known Apple TV
.\AyrPlay.exe send -target 192.168.1.77

# 4K HEVC mirror with a larger (movie) buffer — best on a wired/strong network
.\AyrPlay.exe send -target 192.168.1.77 -video-codec hevc -bitrate 25000 -target-latency-ms 2000

# Audio-only to a HomePod
.\AyrPlay.exe send -target 192.168.1.50 -audio-only

# First-time pairing of a receiver that shows a PIN
$env:DOUBLETAKE_CODE = "1234"; .\AyrPlay.exe send -target 192.168.1.77
```

### Pairing and credentials

On the first connection the tool connects, reads the device info, and completes pairing **before** any capture starts. PIN-less (transient) pairing is attempted automatically; a receiver that shows a PIN or has a password set needs `-code` (or set `$env:DOUBLETAKE_CODE`).

Credentials are then saved and reused, so later runs only re-verify. On Windows they are stored at:

```text
%USERPROFILE%\.config\doubletake\credentials.json
```

(Note this is a **different** folder from the tray's `%AppData%\doubletake\`.) To force a clean re-pair, delete that file or run with `-pair`; use `-creds <path>` to store them elsewhere.

## Build from source

Requires **[Go](https://go.dev/dl/) 1.25.0 or newer** (declared in `go.mod`). The build is pure Go — no CGO, no C toolchain — so you can build the Windows executable from Windows, macOS, or Linux. The first build downloads Go module dependencies, so it needs internet access.

With `make` (any OS, including Git Bash on Windows):

```sh
git clone https://github.com/ayaanh03/AyrPlay
cd AyrPlay
make windows        # -> bin/AyrPlay.exe (x64) and bin/AyrPlay-arm64.exe
make check          # vet for Linux + Windows, then run all tests
```

Or with plain `go` in PowerShell:

```powershell
cd cmd\ayrplay; go generate .; cd ..\..      # optional: embeds the icon, manifest, and version info
go build -trimpath -ldflags="-s -w" -o bin\AyrPlay.exe .\cmd\ayrplay
```

Notes:

- `AyrPlay.exe` is a normal console program on purpose, so command-line use, piping, and agents work. When double-clicked it relaunches itself as a hidden tray process, so only a brief console flash appears.
- `go generate` writes `cmd/ayrplay/resource_windows_*.syso` (icon, DPI/visual-styles manifest, version info) from the text sources next to it. These generated files are git-ignored; without them the exe still works, just with the default icon.
- The code uses per-OS build tags (`*_windows.go` vs `*_other.go`), so everything also builds and tests on Linux. `make check` vets the Windows build from any OS.
- The upstream-named `doubletake` CLI (`cmd/doubletake`, same sender) and the Linux daemon tools are still built by `make` for Linux users.
- Optional AAC-ELD audio requires `CGO_ENABLED=1` and `-tags fdk_aac` linked against `libfdk-aac` (needs a C toolchain). The default build uses the built-in ALAC encoder, which is what AyrPlay streams by default.

## Known limitations & troubleshooting

- **No receivers found.** If the tray shows `Idle — 0 receiver(s)` or the CLI scan finds nothing:
  1. Make sure AirPlay is enabled on the receiver (on Apple TV: **Settings → AirPlay and HomeKit**) and the device is awake.
  2. Allow `AyrPlay.exe` through **Windows Defender Firewall** on the *Private* network, and permit inbound **UDP 5353** (mDNS). (Tray and sender are the same program, so one rule covers both.)
  3. Confirm the PC and receiver are on the **same Wi-Fi/subnet** with no client isolation (guest networks and some mesh/extender setups block device-to-device traffic).
  4. Click **Rescan**.
  5. As a fallback, skip discovery entirely: `.\AyrPlay.exe send -target <receiver-ip>`.
- **Same subnet only.** Discovery is mDNS, which doesn't cross subnets. A double-NAT setup — a second router that isn't in **bridge/AP mode** — puts the PC and receiver on different subnets and prevents discovery. Put everything on one subnet.
- **4K needs a good network.** 1080p mirroring is smooth over Wi-Fi. 4K HEVC is bandwidth-heavy and reliable only on a **wired or strong** network; on congested Wi-Fi it drops frames and recovers at the next keyframe rather than tearing down the session. For choppy 4K, use a wired link, lower `-bitrate`, raise the buffer (**Buffer → Movie** / `-target-latency-ms 2000`), or drop to 1080p.
- **Wi-Fi latency profile.** CLI runs that omit `-target-latency-ms` pick their buffer from the network type; on Windows AyrPlay detects a Wi-Fi adapter and uses the roomier Wi-Fi profile. The tray always sets the buffer explicitly (default **Smooth = 1s**). For movies over Wi-Fi from the CLI, `-target-latency-ms 2000` is still the safest choice.
- **Pairing with a PIN/password.** The tray prompts for the PIN/password in a dialog when the receiver asks for one — enter the value shown on the Apple TV. Transient (PIN-less) receivers need nothing. For scripted/CLI use you can still pass `-code`/`$DOUBLETAKE_CODE` instead.
- **Audio-only captures the whole system mix.** Audio-only (and the audio half of mirroring) captures everything on your default Windows output device via WASAPI loopback — not a single application.
- **GStreamer errors.** Run `AyrPlay.exe doctor` (or **Check setup…**) — it names exactly which element is missing. If `d3d11screencapturesrc` or another plugin is missing, reinstall the latest GStreamer runtime (on old `.msi` installers, choose **Complete**). If GStreamer is installed somewhere unusual, add its `bin` folder to `PATH`.

## License & credits

AyrPlay is a fork of **[omarroth/doubletake](https://github.com/omarroth/doubletake)** and inherits its license: **GNU Lesser General Public License v3.0 or later (LGPL-3.0-or-later)**. The full texts are in the repository: [`LICENSE`](LICENSE) (LGPL v3) and [`COPYING.GPL`](COPYING.GPL) (the GPL v3 base text that LGPL v3 incorporates).

Because AyrPlay is a derivative work of doubletake, it remains under LGPL-3.0-or-later and **cannot be relicensed to a permissive license**. The GStreamer runtime it drives is also LGPL-licensed and is invoked as a separate process.

Credit and thanks to **omarroth/doubletake** for the AirPlay protocol core this builds on — RTSP/HTTP control, FairPlay SAP, SRP-6a pairing, ChaCha20-Poly1305 streaming, mDNS discovery, the encoder pipeline, and the latency policy — and to the prior AirPlay reverse-engineering work behind it.

On top of that, AyrPlay adds: Windows screen capture via GStreamer **d3d11 → NVENC** (`capture_windows.go`), **WASAPI loopback** audio (`audio_source_windows.go`), the windowless **system-tray GUI** with device discovery and auto-routing (`internal/tray`), the single **`AyrPlay.exe`** with `discover`/`doctor` commands (`cmd/ayrplay`, `internal/gstreamer`), an **audio-only** sending mode for HomePods/speakers (`audio_setup.go`) with a HomePod profile in the test receiver, a `-monitor` display selector, buffer/robustness tuning for high-bitrate 4K, and fixes to PIN-less (transient) pairing.

# AyrPlay

Mirror your Windows screen and system audio to an Apple TV (tested on Apple TV 4K 3rd gen), or stream just audio to a HomePod, over AirPlay 2.

AyrPlay is a Windows front-end for AirPlay sending, built on top of [omarroth/doubletake](https://github.com/omarroth/doubletake) (a Linux-only AirPlay mirroring sender). It adds Windows screen and audio capture, a windowless system-tray app that finds receivers on your network and routes to them automatically, and an audio-only mode for speakers. It's a personal-interoperability tool for sending to your own AirPlay 2 hardware. The compiled programs keep their upstream names: `doubletake.exe` (the command-line sender) and `doubletake-tray.exe` (the tray GUI).

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

## Getting AyrPlay

There are **no prebuilt Windows binaries yet** — upstream only ships Linux packages, and CI is Linux-only. For now, the only way to get `doubletake.exe` and `doubletake-tray.exe` is to **[build from source](#build-from-source)** (it's a quick, pure-Go build). If a Windows release is published later, it will be linked here.

## Requirements

**Common (both modes):**

- **Windows 11.**
- **The GStreamer MSVC runtime**, version **1.22 or newer** (the d3d11, wasapi2, and nvcodec elements only exist in recent builds — install the latest 1.x to be safe). AyrPlay shells out to GStreamer for all capture and encoding, so `gst-launch-1.0` and `gst-inspect-1.0` must be resolvable on your `PATH`. See [Installing GStreamer](#installing-gstreamer).
- **An AirPlay 2 receiver on the same subnet** as the PC, with AirPlay enabled on it. Discovery uses mDNS, which does not cross subnets.

**For audio-only (HomePod / AirPlay 2 speaker):** nothing more — no GPU and no video plugins. The only GStreamer element required is `wasapi2src` (the **wasapi2** plugin).

**For screen mirroring:** additionally the **d3d11** capture plugin and a video encoder:

- **NVIDIA GPU (NVENC)** — strongly recommended and the only tested configuration (`nvh264enc` / `nvh265enc`, from the **nvcodec** plugin).
- Software encoders (`openh264enc`, `x264enc`, `x265enc`) work as a slower fallback when no NVENC is present (`-hwaccel openh264` or `-hwaccel none`), but are **untested on Windows**.

The Windows mirroring path uses these GStreamer elements (element names are exact; plugin groupings are the usual GStreamer layout — verify with `gst-inspect-1.0`):

- Video capture: `d3d11screencapturesrc`, `d3d11download` (typically the **d3d11** plugin in gst-plugins-bad)
- Encoders: `nvh264enc`, `nvh265enc` (NVENC); software fallbacks `openh264enc`, `x264enc`, `x265enc`
- Parsers: `h264parse`, `h265parse`
- RTP payload/timestamp: `rtph264pay`, `rtph265pay`, `rtpL16pay`, `audiobuffersplit`, `rtponviftimestamp`, `rtpstreampay`
- Audio capture: `wasapi2src` with `loopback=true` (the **wasapi2** plugin)

## Installing GStreamer

You only need the GStreamer **runtime** (the development package is not required — AyrPlay calls GStreamer as a separate process and builds as pure Go). Install the latest **MSVC 64-bit runtime** for your version floor above.

### gstreamer.org installer (recommended)

Download the MSVC 64-bit **runtime** installer from the official site (gstreamer.freedesktop.org) and run it. When the installer offers an installation type, choose **Complete** so the d3d11, nvcodec, wasapi2, videoparsersbad, and rtp plugins are all included. A **Typical** install can leave some of them out — the [verification step](#put-gstreamer-on-path-and-verify) below is the real check.

### winget (alternative — verify afterwards)

```powershell
winget search gstreamer
winget install --id <the MSVC 64-bit runtime id shown by winget search>
```

A silent winget install usually installs the default (Typical) plugin set with no way to choose Complete, so it **may omit plugins AyrPlay needs**. After installing, run the verification below and, if any element is missing, re-install with the gstreamer.org **Complete** runtime.

### Put GStreamer on PATH and verify

Add the GStreamer `bin` directory to your **user** `PATH`, for example:

```text
C:\Program Files\gstreamer\1.0\msvc_x86_64\bin
```

You can do this via **System Properties → Environment Variables**, or in PowerShell:

```powershell
setx PATH "$env:PATH;C:\Program Files\gstreamer\1.0\msvc_x86_64\bin"
```

After changing `PATH`, **sign out and back in (or reboot)** so that double-clicked apps like the tray inherit the new value — not just newly opened terminals. (The official installer often adds `PATH` for you.)

Then open a **new** terminal and confirm the elements resolve. For mirroring:

```powershell
gst-inspect-1.0 d3d11screencapturesrc
gst-inspect-1.0 nvh264enc
gst-inspect-1.0 rtph264pay
gst-inspect-1.0 rtponviftimestamp
```

For audio-only you only need:

```powershell
gst-inspect-1.0 wasapi2src
```

Each should print element details. "No such element" means your install is incomplete — re-install with the full/Complete plugin set.

## Quick start (GUI, recommended)

1. Put `doubletake.exe` and `doubletake-tray.exe` in the **same folder** — the tray launches `doubletake.exe` from next to itself (falling back to `PATH`). (See [Getting AyrPlay](#getting-ayrplay) for how to obtain them.)
2. Run `doubletake-tray.exe`. It appears in the system tray (tooltip: `doubletake — AirPlay sender`); there is no window. It scans for receivers on startup.
3. Open the tray menu. Top to bottom:
   - A **status** line (`Idle`, then `Scanning…`, then e.g. `Idle — 2 receiver(s)`).
   - A **Receivers** list — click a device to start. An Apple TV starts screen mirroring; a HomePod starts audio-only automatically. (The list shows up to **12** receivers; more are discovered but not listed.)
   - **Rescan** to search again.
   - **Quality**: `1080p (Wi-Fi friendly)` (default) or `4K (wired recommended)`.
   - **Buffer**: `Low latency (0.2s)`, `Smooth (1s)` (default), or `Movie (2s)`.
   - **Display**: `Display 1`, `Display 2`, … — pick which monitor to mirror.
   - **Stream audio**: on by default; uncheck to mirror video only.
   - **Debug logging**: when on, writes a verbose log for the run.
   - **Open logs folder**: opens the log/settings folder in Explorer.
   - **Stop mirroring** (enabled only while running) and **Quit**.

> **Pairing a PIN/password receiver.** When the Apple TV shows a PIN (or a receiver needs its configured password), the tray pops a dialog asking for it — type what the TV shows and click OK. Credentials are then saved, so later connects are automatic. PIN-less (transient) receivers connect with no prompt. If you mistype the PIN, the status reads "Pairing didn't complete — click the device to retry".

The tray's settings and debug log live under `%AppData%\doubletake\`:

- `%AppData%\doubletake\tray.json` — your menu choices.
- `%AppData%\doubletake\doubletake.log` — the debug log (written only when **Debug logging** is on; overwritten each run).

(Pairing credentials are stored elsewhere — see [Command-line usage](#command-line-usage-power-users).)

## Command-line usage (power users)

The sender is `doubletake.exe`. Running it with no `-target` starts an mDNS discovery scan (about 5 seconds) and lets you pick a device interactively; passing `-target` skips discovery.

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

`-audio-only` and `-no-audio` are mutually exclusive; passing both exits with an error. (The binary also carries some Linux/X11 and daemon flags that have no effect on Windows, plus an internal `-machine-prompts` flag the tray uses to drive PIN/password entry — you don't need it by hand.)

Examples:

```powershell
# Basic 1080p mirror (auto codec = H.264) to a known Apple TV
.\doubletake.exe -target 192.168.1.77

# 4K HEVC mirror with a larger (movie) buffer — best on a wired/strong network
.\doubletake.exe -target 192.168.1.77 -video-codec hevc -bitrate 25000 -target-latency-ms 2000

# Audio-only to a HomePod
.\doubletake.exe -target 192.168.1.50 -audio-only

# First-time pairing of a receiver that shows a PIN
.\doubletake.exe -target 192.168.1.77 -code 1234
```

### Pairing and credentials

On the first connection the tool connects, reads the device info, and completes pairing **before** any capture starts. PIN-less (transient) pairing is attempted automatically; a receiver that shows a PIN or has a password set needs `-code` (or set `$env:DOUBLETAKE_CODE`).

Credentials are then saved and reused, so later runs only re-verify. On Windows they are stored at:

```text
%USERPROFILE%\.config\doubletake\credentials.json
```

(Note this is a **different** folder from the tray's `%AppData%\doubletake\`.) To force a clean re-pair, delete that file or run with `-pair`; use `-creds <path>` to store them elsewhere.

## Build from source

Requires **[Go](https://go.dev/dl/) 1.25.0 or newer** (declared in `go.mod`). The default build is pure Go — no CGO, no C toolchain. The first build downloads Go module dependencies, so it needs internet access.

```powershell
git clone https://github.com/ayaanh03/AyrPlay
cd AyrPlay

New-Item -ItemType Directory -Force bin | Out-Null
go build -o bin\doubletake.exe .\cmd\doubletake
go build -ldflags="-H=windowsgui" -o bin\doubletake-tray.exe .\cmd\doubletake-tray
```

Notes:

- **Keep `doubletake.exe` a normal console binary** — do not build it windowless. The tray launches it hidden with the Windows `CREATE_NO_WINDOW` flag, so no console appears in normal use.
- **The tray (`doubletake-tray.exe`) must be built with `-ldflags="-H=windowsgui"`**, otherwise a console window shows up behind it.
- The tray embeds its icon at compile time via `//go:embed icon.ico`, so `cmd/doubletake-tray/icon.ico` must be present when you build.
- Ship both executables in the **same folder**; the tray looks for `doubletake.exe` next to itself first.
- The code uses per-OS build tags (`windows` vs `!windows` counterparts for the capture, audio, tray-process, and daemon-lock files), so it still builds on Linux. The repo's Makefile is Linux-only and does **not** build the tray — on Windows, use the `go build` commands above.
- Optional AAC-ELD audio requires `CGO_ENABLED=1` and `-tags fdk_aac` linked against `libfdk-aac` (needs a C toolchain). The default build uses the built-in ALAC encoder, which is what AyrPlay streams by default.

## Known limitations & troubleshooting

- **No receivers found.** If the tray shows `Idle — 0 receiver(s)` or the CLI scan finds nothing:
  1. Make sure AirPlay is enabled on the receiver (on Apple TV: **Settings → AirPlay and HomeKit**) and the device is awake.
  2. Allow `doubletake.exe` and `doubletake-tray.exe` through **Windows Defender Firewall** on the *Private* network, and permit inbound **UDP 5353** (mDNS). Because the sender is launched hidden, no firewall prompt appears on its own.
  3. Confirm the PC and receiver are on the **same Wi-Fi/subnet** with no client isolation (guest networks and some mesh/extender setups block device-to-device traffic).
  4. Click **Rescan**.
  5. As a fallback, skip discovery entirely: `.\doubletake.exe -target <receiver-ip>`.
- **Same subnet only.** Discovery is mDNS, which doesn't cross subnets. A double-NAT setup — a second router that isn't in **bridge/AP mode** — puts the PC and receiver on different subnets and prevents discovery. Put everything on one subnet.
- **4K needs a good network.** 1080p mirroring is smooth over Wi-Fi. 4K HEVC is bandwidth-heavy and reliable only on a **wired or strong** network; on congested Wi-Fi it drops frames and recovers at the next keyframe rather than tearing down the session. For choppy 4K, use a wired link, lower `-bitrate`, raise the buffer (**Buffer → Movie** / `-target-latency-ms 2000`), or drop to 1080p.
- **Windows Wi-Fi latency profile (CLI only).** The automatic latency profile decides its buffer size from a wireless check that reads a Linux-style path, so on Windows a Wi-Fi link is treated as if it were wired and gets the *normal* profile (less buffer headroom) instead of the roomier Wi-Fi profile. This only affects **CLI runs that omit `-target-latency-ms`** — the tray always sets it (default **Smooth = 1s**), so tray users are unaffected. For movies over Wi-Fi from the CLI, set `-target-latency-ms` explicitly (e.g. `2000`).
- **Pairing with a PIN/password.** The tray prompts for the PIN/password in a dialog when the receiver asks for one — enter the value shown on the Apple TV. Transient (PIN-less) receivers need nothing. For scripted/CLI use you can still pass `-code`/`$DOUBLETAKE_CODE` instead.
- **Audio-only captures the whole system mix.** Audio-only (and the audio half of mirroring) captures everything on your default Windows output device via WASAPI loopback — not a single application.
- **GStreamer errors.** If the tool reports `d3d11screencapturesrc` is missing / to install gst-plugins-bad, your GStreamer install is incomplete — re-install with the full/Complete plugin set. If nothing starts at all, confirm `gst-launch-1.0` is on `PATH` (and that you re-logged in after changing `PATH`).

## License & credits

AyrPlay is a fork of **[omarroth/doubletake](https://github.com/omarroth/doubletake)** and inherits its license: **GNU Lesser General Public License v3.0 or later (LGPL-3.0-or-later)**. The full texts are in the repository: [`LICENSE`](LICENSE) (LGPL v3) and [`COPYING.GPL`](COPYING.GPL) (the GPL v3 base text that LGPL v3 incorporates).

Because AyrPlay is a derivative work of doubletake, it remains under LGPL-3.0-or-later and **cannot be relicensed to a permissive license**. The GStreamer runtime it drives is also LGPL-licensed and is invoked as a separate process.

Credit and thanks to **omarroth/doubletake** for the AirPlay protocol core this builds on — RTSP/HTTP control, FairPlay SAP, SRP-6a pairing, ChaCha20-Poly1305 streaming, mDNS discovery, the encoder pipeline, and the latency policy — and to the prior AirPlay reverse-engineering work behind it.

On top of that, AyrPlay adds: Windows screen capture via GStreamer **d3d11 → NVENC** (`capture_windows.go`), **WASAPI loopback** audio (`audio_source_windows.go`), the windowless **system-tray GUI** with device discovery and auto-routing (`cmd/doubletake-tray`), an **audio-only** sending mode for HomePods/speakers (`audio_setup.go`), a `-monitor` display selector, buffer/robustness tuning for high-bitrate 4K, and fixes to PIN-less (transient) pairing.

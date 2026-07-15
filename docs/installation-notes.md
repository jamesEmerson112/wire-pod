# wire-pod Installation Notes

Reference copy of the official install guide, saved for offline/future use.
Source: https://github.com/kercre123/wire-pod/wiki/Installation (fetched 2026-07-14).
The wiki lives in a separate GitHub wiki repo, so this content is not otherwise in this codebase.

## Prerequisites

- An Anki/Digital Dream Labs Vector robot (regular production bot or OSKR/dev-unlocked).
- A server device: Linux, macOS, Windows 10/11, or Android 6.0+.
- A Bluetooth-capable device for bot setup (the browser-based setup talks to the bot over BLE).

## Installation methods by platform

### Debian/Ubuntu Linux (.deb)

Download the `.deb` matching your architecture (amd64, armhf, arm64) from the
[releases page](https://github.com/kercre123/wire-pod/releases), then:

```bash
sudo apt update
sudo apt install -y ./wirepod_<arch>-#.#.#.deb
```

Then open the configuration page at the IP:port it prints.

### Other Linux / macOS (build from source)

```bash
git clone https://github.com/kercre123/wire-pod --depth=1
cd wire-pod
sudo STT=vosk ./setup.sh
sudo ./chipper/start.sh
```

Optional daemon mode: `sudo ./setup.sh daemon-enable`

### Windows 10/11

- Download `WirePodInstaller-v#.#.#.exe` from the releases page and run it.
- If SmartScreen warns, choose "More info" → "Run anyway".
- The installer handles configuration; click "Open browser" at the end to finish setup in the web UI.
- (Alternative: run from source under WSL, or use Docker Desktop with the compose method below.)

### macOS 11+ (.dmg)

Download the `.dmg`, drag WirePod into Applications, launch it. Approve it under
System Settings → Privacy & Security ("Open Anyway") if Gatekeeper blocks it.

### Android 6.0+ (.apk)

Install the `.apk` (Chrome works best for downloading), grant permissions,
disable battery optimization for the app, then open the control URL it shows.

### Docker Compose

Requires the server to be reachable as `escapepod.local` (mDNS). Uses the repo's
`compose.yaml` / `dockerfile` (Vosk STT only; data persists in the `/data` volume):

```bash
docker compose up -d -f https://raw.githubusercontent.com/kercre123/wire-pod/main/compose.yaml
```

## Production bot preparation (regular non-OSKR bots)

1. Place Vector on the charger.
2. Hold the backpack button ~15 seconds until he turns off and back on, reaching the
   recovery screen (`anki.com/v` or `ddl.io/v`).
3. On a device **with Bluetooth support**, open **Chrome** (or another Chromium-based
   browser) and go to https://wpsetup.keriganc.com/ to push the wire-pod-compatible
   firmware.
4. Afterward, clear user data on the bot via the on-bot menu.

### Why Chrome specifically?

The wpsetup page connects **directly to Vector over Bluetooth LE from inside the
browser**, using the **Web Bluetooth API**. That API is only implemented in
Chromium-based browsers (Chrome, Edge, Opera; Brave ships it disabled by default).
Firefox and Safari have declined to implement Web Bluetooth, so the setup page
simply cannot reach the robot from those browsers. That's also why the instructions
insist the device running the browser has Bluetooth hardware — the browser itself is
the BLE client. wire-pod also has an in-built BLE setup alternative
(`chipper/pkg/wirepod/setup/ble.go`, enabled with the `inbuiltble` build tag) that
does the same job server-side instead of through the browser.

## Bot authentication

- Both production and OSKR bots authenticate via the wire-pod web UI → "Bot Setup"
  section, using either the external webpage flow (Web Bluetooth, above) or the
  built-in BLE flow.
- OSKR/dev-unlocked bots need the additional OSKR-specific steps in the second
  section of Bot Setup first.

# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

wire-pod is server software that gives Anki/Digital Dream Labs Vector robots free voice command support by replacing DDL's cloud. It contains two independent Go modules:

- `chipper/` — the actual wire-pod server (module `github.com/kercre123/wire-pod/chipper`). This is where nearly all development happens.
- `vector-cloud/` — the on-robot client software (`vic-cloud`, `vic-gateway`), cross-compiled for the robot's ARM Linux (vicos). Rarely touched.

## Build & Run

Everything is bash-based and targets Linux/macOS (root required). On Windows, use WSL or Docker.

```bash
sudo ./setup.sh            # one-time: installs deps, STT engine, certs; generates chipper/source.sh
sudo ./chipper/start.sh    # runs the server (uses prebuilt ./chipper binary if present, else `go run`)
```

- `chipper/source.sh` (generated, not committed) holds the env config; `start.sh` sources it and picks the entry point based on `STT_SERVICE`: `vosk` (default), `coqui`, `leopard`, `rhino`, `houndify`, `whisper`, `whisper.cpp`.
- Each STT engine has its own `main.go` under `chipper/cmd/<engine>/`, all of which just call `initwirepod.StartFromProgramInit(stt.Init, stt.STT, stt.Name)` with that engine's implementation.
- CGO is required. Always build with `-tags nolibopusfile` (add `inbuiltble` for in-built BLE setup). Engine-specific `CGO_CFLAGS`/`CGO_LDFLAGS`/`LD_LIBRARY_PATH` are set in `start.sh` (e.g. Vosk expects libvosk in `~/.vosk/libvosk`).
- Manual build example (from `chipper/`): `go build -tags nolibopusfile -o chipper ./cmd/vosk`. The commit is stamped via `-ldflags "-X github.com/kercre123/wire-pod/chipper/pkg/vars.CommitSHA=..."`.
- Docker: `docker compose up` (root `compose.yaml` + `dockerfile`, Vosk-only). Mutable state is symlinked into `WIREPOD_DATA_DIR` (default `/data`) by `docker/entrypoint.sh`. CI (`.github/workflows/docker-publish.yml`) publishes `ghcr.io/kercre123/wire-pod` on pushes to main.

Working directory matters: the chipper binary must run from `chipper/` — paths like `../certs`, `./jdocs`, `./plugins`, `../vosk/models/` in `pkg/vars` are relative.

### Tests

`chipper/` has no tests. `vector-cloud/` has unit tests: `cd vector-cloud && go test ./internal/...` (run a single one with e.g. `go test ./internal/ipc -run TestName`).

### vector-cloud

```bash
cd vector-cloud
make docker-builder   # one-time: builds the ARM cross-compile docker image
make vic-cloud        # or: make vic-gateway
```

## Architecture

### Request flow (voice command)

Robot streams Opus audio over gRPC → `pkg/servers/chipper` (gRPC service impl) → `pkg/wirepod/preqs` (process request; entry chain) → `pkg/wirepod/speechrequest` (normalizes the audio stream into an STT-friendly form) → `pkg/wirepod/stt/<engine>` (speech-to-text) → `pkg/wirepod/ttr` (text-to-response: intent matching, param parsing, plugin dispatch, LLM/knowledge-graph responses, sends intent back to the robot).

Intent matching uses `chipper/intent-data/<locale>.json` keyphrase lists, plus user-defined custom intents (`customIntents.json`) and Go plugins. `pkg/wirepod/localization` handles languages/model downloads.

### Servers and ports

`pkg/initwirepod/startserver.go` wires everything together:

- **443** (configurable, or 8084 in escape-pod-compat mode): single TLS listener split by cmux — HTTP/2 → gRPC (three services: chipper voice, `pkg/servers/jdocs`, `pkg/servers/token`), HTTP/1 → `/ok` health check. Certs come from `../certs/` or `./epod/` (escape pod mode, which also broadcasts mDNS as `escapepod.local` via `pkg/mdnshandler`).
- **8080**: setup/config web UI and API (`pkg/wirepod/config-ws`, serves `chipper/webroot/`) — this runs on the main thread; if wire-pod isn't set up yet, only this runs.
- **80**: connection check endpoint + SDK app (`pkg/wirepod/sdkapp`: robot settings, control, BLE onboarding via `pkg/wirepod/setup`).

Config changes from the web UI call `initwirepod.RestartServer()` to bounce the gRPC listeners without killing the process.

### Global state

`pkg/vars` holds all shared state and persisted config: `APIConfig` (apiConfig.json), jdocs, `botSdkInfo.json` (known robots + GUIDs), custom intents, loaded intent lists. Env vars (`STT_SERVICE`, `WEATHERAPI_*`, `KNOWLEDGE_*`, `DDL_RPC_PORT`, ...) can seed/override config in `vars/config.go`. Avoid import cycles into `vars` — it's imported by nearly everything (note `vars.SttInitFunc` exists specifically to break one).

### Extensibility

- **Plugins** (`chipper/plugins/`): Go `plugin` .so files (Linux only, `go build -buildmode=plugin`) exporting `Utterances []string`, `Name string`, and `Action func(transcribedText, botSerial, guid, target string) (intent string, speech string)`. Loaded at startup by `pkg/wirepod/ttr/plugins.go`; see `plugins/whatdate/` for a minimal example.
- **Lua scripting** (`pkg/scripting`): gopher-lua bindings exposing robot behavior control (sayText, playAnimation, moveWheels, HTTP requests, etc.).
- **Web UI** (`chipper/webroot/`): plain HTML/JS/CSS served directly — no build step or framework.
- Robot SDK interactions go through `github.com/fforchino/vector-go-sdk`; LLM responses through `github.com/sashabaranov/go-openai`.

## Upstream PR candidates (kercre123/wire-pod)

Small, self-contained fixes to offer upstream — one small PR at a time, starting with the
most undeniable. NEVER PR a fork branch directly: cherry-pick onto a clean branch cut from
`upstream/main` (this fork's main carries personal docs/scripts and `build-windows.ps1`
has user-specific default paths). Consider opening an issue first for anything larger.

Already fixed in this fork (extract the minimal diff when PRing):
- **Logger data race** — upstream `pkg/logger` has no mutex; concurrent gRPC/goroutine
  writers vs. HTTP readers on `LogArray`/`LogTrayArray`. Minimal fix: one `sync.Mutex`.
- **ble.js dead `GetLog` timer** — `showBotAuth()` references a variable that doesn't stop
  polling (upstream equivalent: log polling never stops when leaving the Logs tab).
- **jdocs two-line ReadDocs log** — `servers/jdocs/server.go` logs a header then dumps
  `req.Items` as a second timestamped line; merge into one line.
- **ANSI escape leak** — `sdkapp/server.go` logs `\033[1;36m...` which renders as garbage
  in the web UI log; strip escapes for the UI buffers.
- **Unpunctuated LLM response dropped** — `ttr/kgsim.go` EOF branch reports
  "no response" when the answer never contained `.?!` (e.g. one-word replies);
  fix: flush the remaining buffer as a final sentence at EOF.
- **LLM stream goroutine death hangs the caller** — mid-stream error `return`s and an
  empty-choices chunk `return`s (should `continue`) without signaling `successIntent`/
  `speakReady`, leaving StreamingKGSim's caller and speaking loop blocked forever
  (`ttr/kgsim.go` ~:342-350).

Known upstream bugs, NOT yet fixed in this fork (verify still present before PRing):
- Unknown/empty `Knowledge.Provider` → nil `*openai.Client` panic (no `default:` in the
  provider switch, `ttr/kgsim.go` ~:242-257).
- LLM command without `||` separator → index-out-of-range panic
  (`ttr/kgsim_cmds.go` GetActionsFromString ~:203).
- `DoSayText` calls `removeSpecialCharacters` but discards the result
  (`ttr/kgsim_cmds.go` ~:291).
- `ActionNewRequest` and `ActionPlaySound` are both `= 4` (`ttr/kgsim_cmds.go` ~:29-32);
  latent, masked because playSound is disabled.
- jdocs ReadDocs indexes `req.Items[0]` without an empty check
  (`servers/jdocs/server.go` ~:76).
- `GET /api/get_kg_api` returns the LLM API key in plaintext to any LAN client
  (`config-ws/webserver.go`) — needs an upstream design conversation, not a drive-by fix.

## Rust port (sibling repo)

A full-parity Rust rewrite of the chipper server lives in the separate repo
`../wire-pod-rs` (local git only, not on GitHub). Approved plan:
`~/.claude/plans/breezy-knitting-cray.md` (11 phases, P0–P10). The Rust server
targets drop-in protocol/state compatibility — same gRPC/HTTP/mDNS contract,
same `%APPDATA%\wire-pod` state files — so cutover/rollback is stop-one,
start-the-other. Runtime assets (webroot, intent-data, epod certs, stttest.pcm,
weather-map) are vendored there byte-identically from THIS repo;
`cargo xtask sync-assets --from ../wire-pod --check` (run in wire-pod-rs)
detects drift — run it after editing webroot or intent-data here. This Go repo
stays the production server until cutover (plan P10) and the rollback for a
month after.

## Context History

### 2026-08-19 10:21
- [feat] **Rust port started.** Plan-mode session produced an approved 11-phase full-parity port plan (`~/.claude/plans/breezy-knitting-cray.md`); user chose: chipper only, full parity (all 6 STT engines, Lua + re-designed plugin system, BLE deferred), Windows-first, **new repo** `../wire-pod-rs`.
- Phase 0 complete in wire-pod-rs (commits `2230024`, `f0c15e3`): 13-crate Cargo workspace + xtask + CI; all 15 protos vendored from the Go module cache and compiling under tonic/protox; assets vendored byte-identically with sync-check tooling; `.gitattributes` disables EOL conversion to protect byte fidelity.
- All 4 de-risk spikes PASS on Windows/MSVC:
  - S1: rustls + hyper-auto TLS listener serving real `StreamingConnectionCheck` + `/ok` + `/ok:80`; selftest green with **ALPN on and off** (preface sniffing = cmux parity).
  - S2: Rust vosk crate + the installed `libvosk.dll`/en-US model transcribe `stttest.pcm` → **"how are you"** (engine-parity golden).
  - S3: opus/ogg roundtrips, WebRTC VAD, Go filter chain ported bug-for-bug (incl. `alpha = dt/(rc+dt)` quirk + per-chunk state reset); filtered-PCM sha256 golden recorded.
  - S4: mlua (Lua 5.1) and extism WASM hosts both work — plugins-on-Windows is new capability.
- Notable port findings: vector-sdk `shared.proto` is missing `import "onboarding.proto"` (patched in the vendored copy, see wire-pod-rs DEVIATIONS.md); cmake 4.x needs `CMAKE_POLICY_VERSION_MINIMUM=3.5` for vendored libopus; epod key is RSA-2048 (not 1028 — that only affects generated IP-mode certs); BLE was never in the packaged Windows build (`inbuiltble` tag); the Windows tray shell lives in the separate `WirePod` repo (~900 LOC to re-implement in plan P9).
- [todo] **S1 live robot test pending** (`wire-pod-rs/RUNBOOK-S1.md`): needs user present — elevated shell, quit WirePod tray app, robot offline ~5 min, verify conn-check with ALPN on+off, restart Go app. Then phases P1–P10 remain (P1 robot auth → P2 voice → P3 LLM is the XL one).
- [note] Monthly Claude spend limit hit 2026-08-19: subagent/workflow spawns fail until raised; sessions run single-context, one phase at a time with a commit per checkpoint.
- Prior session (2026-08-18): Jetson "second brain" fan-out investigation written to `docs/jetson-second-brain-investigation.md` (untracked) — verdict: wire-pod stays the foundation, wire-os is orthogonal robot-side firmware, Orin Nano Super 8GB is the target board; superseded as the active effort by the Rust port above (Jetson remains plan-compatible: Linux aarch64 is a P9 target).

### 2026-07-16 14:07
- Merged `feature/log-redesign` into main (log system redesign + 3-state bot status UI), deployed on the robot as v1.2.18-custom. Added "Upstream PR candidates" section to this file.
- [feat] New branch `feature/battery-gohome` (current): battery go-home watchdog. Added `chipper/pkg/wirepod/sdkapp/batterywatchdog.go` — polls each online bot's battery every 30s (BatteryState RPC via cached `vars.GetRobot` connections), ports the `battery.js` volts→percent curve; when percent <= `APIConfig.Battery.GoHomePercent` (new config, default 25, 0=disabled, env seed `GOHOME_BATTERY_PERCENT`) for 3 consecutive polls while off-charger, assumes behavior control (`OVERRIDE_BEHAVIORS`) and fires `DriveOnCharger` (silent, no voice line); 10-min cooldown per firing, 3 attempts max then 30-min backoff. Hooked via `go BatteryWatchdog()` in `sdkapp.BeginServer` (works in packaged WirePod wrapper builds too).
  - Why: robot's degraded battery sags too fast for the firmware's own self-dock threshold.
- [research] Added unconditional debug log of the final LLM system prompt in `ttr/kgsim_cmds.go` `CreatePrompt` (comp "llm") to diagnose why the custom GPT Prompt ("One word response only") seems ignored. Finding: prompt wiring is correct, but when LLM commands are enabled ~1500 chars of animation instructions get appended after the custom prompt and can bury short directives.
- Commits: `9b7da13` (watchdog), `b9fb109` (prompt logging).
- [todo] Elevated `scripts/build-windows.ps1 -Deploy` build+deploy to the installed Windows app was launched; verification pending at time of logging.

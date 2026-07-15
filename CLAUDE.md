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

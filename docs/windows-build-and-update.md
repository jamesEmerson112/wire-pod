# Building a replacement chipper.exe on Windows (proven 2026-07-16)

How to produce a new `C:\Program Files\wire-pod\chipper\chipper.exe` after modifying code
in this repo. Recipe was executed end-to-end on this machine (scratchpad build) and the
resulting exe verified with `go version -m` — the local fork's code flows in.

## One-command build

`scripts/build-windows.ps1` automates the whole recipe below: it clones the WirePod
wrapper (if missing), adds the `replace` directive to its `go.mod`, prepares the
`opus`/`ogg`/`vosk` CGO import libraries, compiles the resource file, and builds
`chipper.exe` into `WirePod\windows\`.

```powershell
# build only (normal PowerShell window)
powershell -ExecutionPolicy Bypass -File scripts\build-windows.ps1

# build + deploy over the installed app (elevated / "Run as administrator")
powershell -ExecutionPolicy Bypass -File scripts\build-windows.ps1 -Deploy
```

`-Deploy` (requires admin) stops the running `chipper`, copies the new exe and mirrors
`chipper/webroot` into the install dir, suffixes the `version` file with `-custom`,
relaunches, and waits for `http://localhost:8080/api/is_running` to return `true`.

Prereqs on PATH: `go`, `gcc`, `windres`, `gendef`, `dlltool` (mingw-w64), `git` (plus
`cmake` only if the opus-from-source fallback is reached). Override defaults with
`-WirePodDir`, `-RepoDir`, `-LibsDir`, `-InstallDir`. The rest of this doc explains the
manual steps the script performs.

## How the installed exe is actually produced

The installed Windows app is NOT built from this repo alone. Two repos are involved:

- **kercre123/wire-pod** (this repo) — the `chipper` Go module: all server logic.
- **kercre123/WirePod** (wrapper) — tray app + installer. `windows/cmd` is the main
  package; `cross/podapp` sets `vars.Packaged = true` and *reimplements* the server
  bring-up (a copy of `pkg/initwirepod/startserver.go` wrapped with systray/zenity).

Facts recovered from the installed exe's embedded metadata (`go version -m`):

- Built by WirePod release v1.2.18 CI (macOS runner, mingw-w64 cross-compile, go1.21.13).
- WirePod's `go.mod` pins `github.com/kercre123/wire-pod/chipper v1.5.10`
  = commit `11e7b22` = the tag `chipper/v1.5.10` on this repo. No `replace` directive —
  to build with local changes you must add one.
- Flags: `-tags nolibopusfile`, `-ldflags "-H=windowsgui -w -s -X '...vars.CommitSHA=<sha>'"`,
  `CGO_ENABLED=1`, CGO include/lib paths to `windows/libs/{ogg,opus,vosk}` (built at CI
  time, not committed: ogg+opus from source, vosk prebuilt `vosk-win64-0.3.45.zip` from
  alphacep/vosk-api releases).

## Prerequisites (already on this machine)

- Go (1.24.4 via system install)
- mingw-w64 gcc 13.2.0 (`scoop install gcc`) — includes `windres`
- `scoop install pkg-config cmake`

## Recipe

```bash
# 1. Build libopus as a shared lib (CMake; upstream uses autotools, same result).
#    IMPORTANT: name it opus-0 so the exe imports libopus-0.dll, matching the DLL
#    already shipped in the install folder.
git clone --depth 1 https://github.com/xiph/opus
cmake -G "MinGW Makefiles" -S opus -B opus/build \
  -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=ON -DOPUS_BUILD_TESTING=OFF \
  -DCMAKE_INSTALL_PREFIX=<PREFIX>/opus
cmake --build opus/build -j8 && cmake --install opus/build
# rename libopus.dll/.dll.a -> libopus-0.dll/.dll.a (or set CMake OUTPUT_NAME opus-0)

# 2. Vosk: download the exact release upstream uses
#    https://github.com/alphacep/vosk-api/releases/download/v0.3.45/vosk-win64-0.3.45.zip
#    -> vosk_api.h, libvosk.lib, libvosk.dll into <PREFIX>/vosk
#    (libvosk.dll matches the installed one byte-compatibly — same source zip)

# 3. Clone the wrapper and point it at the local fork
git clone --depth 1 https://github.com/kercre123/WirePod
echo 'replace github.com/kercre123/wire-pod/chipper => C:/Users/voan2/Documents/GitHub/wire-pod/chipper' >> WirePod/go.mod

# 4. Build
cd WirePod/windows
windres cmd/rc/app.rc -O coff -o cmd/app.syso
export CGO_ENABLED=1 GOOS=windows GOARCH=amd64
export PKG_CONFIG_PATH="<PREFIX>/opus/lib/pkgconfig"
export CGO_CFLAGS="-I<PREFIX>/opus/include -I<PREFIX>/vosk"
export CGO_LDFLAGS="-L<PREFIX>/opus/lib -L<PREFIX>/vosk"
COMMIT=$(git -C /c/Users/voan2/Documents/GitHub/wire-pod rev-parse --short HEAD)
go build -tags nolibopusfile \
  -ldflags "-H=windowsgui -w -s -X 'github.com/kercre123/wire-pod/chipper/pkg/vars.CommitSHA=$COMMIT'" \
  -o chipper.exe ./cmd

# 5. Verify the fork's code is inside
go version -m chipper.exe   # dep ...wire-pod/chipper v1.5.10 => C:/Users/.../wire-pod/chipper (devel)
```

A working copy of all of this already exists from the proof run (WirePod clone with
patched go.mod + built `chipper-test.exe` + ogg/opus/vosk under `prefix/`) in the
session scratchpad — but scratchpads are temporary; redo in a durable folder (e.g.
`Documents/GitHub/WirePod`) for repeated use.

## Deploying the new exe

1. Quit WirePod from the tray icon (or `Stop-Process -Name chipper`).
2. Copy the new exe over `C:\Program Files\wire-pod\chipper\chipper.exe` (needs admin).
3. Relaunch (Start menu, or `chipper.exe -d` as the startup registry entry does).

All data lives in `%APPDATA%\wire-pod` (jdocs, botSdkInfo, apiConfig, certs) — swapping
the exe never touches robot pairing/config.

## Caveats

- **Re-running the WirePod installer clobbers everything**: it `os.RemoveAll`s the whole
  install dir and re-extracts the latest GitHub release. Manual swaps stick otherwise —
  there is **no auto-updater** in the tray app or server (version check is display-only).
- **Edits to `chipper/pkg/initwirepod/` have no effect in packaged builds** — the wrapper
  duplicates that logic in `cross/podapp/initwirepod.go`. Everything else (logger,
  servers/*, wirepod/*, vars, scripting) is imported from the chipper module and works.
- **Web UI needs no rebuild**: `webroot/` is served as static files from the install dir —
  edit `C:\Program Files\wire-pod\chipper\webroot\...` directly (admin) and refresh.
  A rebuild/reinstall replaces these, so keep the source of truth in the repo.
- The Version tab compares the install's `version` file against the latest WirePod
  release tag — a custom build may show "update available"; cosmetic only.
- Linked DLL note: exe imports `libopus-0.dll` + `libvosk.dll` (both already in the
  install folder); `libogg-0.dll` is shipped but unused by both old and new exes.
- Local toolchain links against classic msvcrt; upstream CI uses UCRT mingw. Both
  runtimes ship with Windows 10/11 — no practical difference observed.

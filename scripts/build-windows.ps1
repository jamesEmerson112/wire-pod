# build-windows.ps1 - one-command build (and optional deploy) of the packaged
# Windows chipper.exe from this fork.
#
# The installed Windows app is produced by the wrapper repo github.com/kercre123/WirePod
# (windows/cmd), which imports this repo's chipper module. This script clones the wrapper,
# points its go.mod at this repo, prepares the CGO import libraries (opus/ogg/vosk),
# compiles the resource file, and builds chipper.exe. With -Deploy (admin), it stops the
# running server, copies the exe + webroot into the install dir, bumps the version file,
# relaunches, and waits for the health endpoint.
#
# Usage (build only, normal PowerShell window):
#   powershell -ExecutionPolicy Bypass -File scripts\build-windows.ps1
#
# Usage (build + deploy, elevated / "Run as administrator"):
#   powershell -ExecutionPolicy Bypass -File scripts\build-windows.ps1 -Deploy
#
# Prereqs on PATH: go, gcc, windres, git. Optional: gendef + dlltool (mingw-w64-tools)
# enable the import-lib route; without them the linker links directly against the
# installed DLLs (-l:libopus-0.dll), which needs no extra tools. cmake is only needed
# if the opus source fallback is reached.

param(
    [switch]$Deploy,
    [string]$WirePodDir = 'C:\Users\voan2\Documents\GitHub\WirePod',
    [string]$RepoDir    = 'C:\Users\voan2\Documents\GitHub\wire-pod',
    [string]$LibsDir    = '',
    [string]$InstallDir = 'C:\Program Files\wire-pod\chipper'
)

$ErrorActionPreference = 'Stop'

if ($LibsDir -eq '') { $LibsDir = Join-Path $WirePodDir 'windows\libs-local' }

# Proven-build artifacts left in the session scratchpad; used as fallbacks when the
# installed DLLs are not present. Safe to be absent - guarded by Test-Path everywhere.
$ScratchLibs = 'C:\Users\voan2\AppData\Local\Temp\claude\C--Users-voan2-Documents-GitHub-wire-pod\f95e50a2-dd36-4c32-9e1e-4a1b9f9771bf\scratchpad\build-libs'

function Test-Admin {
    $id = [System.Security.Principal.WindowsIdentity]::GetCurrent()
    $pr = New-Object System.Security.Principal.WindowsPrincipal($id)
    return $pr.IsInRole([System.Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Assert-Tool([string]$name) {
    $cmd = Get-Command $name -ErrorAction SilentlyContinue
    if ($null -eq $cmd) {
        throw "Required tool '$name' was not found on PATH. Install it and retry."
    }
    Write-Host ("  OK  {0} -> {1}" -f $name, $cmd.Source) -ForegroundColor DarkGray
}

function Assert-Tools {
    Write-Host "Checking toolchain ..." -ForegroundColor Cyan
    foreach ($t in @('go', 'gcc', 'windres', 'git')) {
        Assert-Tool $t
    }
    $script:HaveImpTools = $true
    foreach ($t in @('gendef', 'dlltool')) {
        if ($null -eq (Get-Command $t -ErrorAction SilentlyContinue)) {
            $script:HaveImpTools = $false
            Write-Host "  --  $t not found (optional); will link directly against DLLs" -ForegroundColor DarkGray
        }
    }
}

function Ensure-WirePodClone {
    $cmdDir = Join-Path $WirePodDir 'windows\cmd'
    if (Test-Path $cmdDir) {
        Write-Host "  OK  WirePod wrapper present at $WirePodDir" -ForegroundColor DarkGray
        return
    }
    Write-Host "Cloning WirePod wrapper into $WirePodDir ..." -ForegroundColor Cyan
    $parent = Split-Path -Parent $WirePodDir
    if (-not (Test-Path $parent)) { New-Item -ItemType Directory -Force -Path $parent | Out-Null }
    git clone --depth 1 https://github.com/kercre123/WirePod $WirePodDir
    if ($LASTEXITCODE -ne 0) { throw "git clone of WirePod failed (exit $LASTEXITCODE)" }
}

function Ensure-GoModReplace {
    $goMod = Join-Path $WirePodDir 'go.mod'
    if (-not (Test-Path $goMod)) { throw "go.mod not found at $goMod" }
    $present = Select-String -Path $goMod -SimpleMatch 'wire-pod/chipper =>' -Quiet
    if ($present) {
        Write-Host "  OK  go.mod replace directive already present" -ForegroundColor DarkGray
        return
    }
    $repoChipper = ($RepoDir -replace '\\', '/') + '/chipper'
    $line = 'replace github.com/kercre123/wire-pod/chipper => ' + $repoChipper
    Add-Content -Path $goMod -Value $line -Encoding utf8
    Write-Host "  OK  appended replace directive -> $repoChipper" -ForegroundColor Green
}

# gendef enumerates the DLL's exports; dlltool turns the .def into an import lib whose
# internal name matches the shipped DLL (so the exe imports libopus-0.dll etc).
function New-MinGWImportLib([string]$dllPath, [string]$dllName, [string]$impLibPath) {
    $dir = Split-Path -Parent $impLibPath
    Push-Location $dir
    try {
        gendef $dllPath
        if ($LASTEXITCODE -ne 0) { throw "gendef failed for $dllName (exit $LASTEXITCODE)" }
        $def = [System.IO.Path]::GetFileNameWithoutExtension($dllName) + '.def'
        dlltool -D $dllName -d $def -l $impLibPath
        if ($LASTEXITCODE -ne 0) { throw "dlltool failed for $dllName (exit $LASTEXITCODE)" }
    } finally {
        Pop-Location
    }
}

# Final fallback for opus: build the shared lib from source with cmake, named so the
# exe imports libopus-0.dll (matching the DLL shipped in the install folder).
function Build-OpusFromSource([string]$impLibPath, [string]$incDest) {
    Assert-Tool 'cmake'
    $work = Join-Path $env:TEMP 'wire-pod-opus-src'
    if (-not (Test-Path (Join-Path $work 'CMakeLists.txt'))) {
        if (Test-Path $work) { Remove-Item $work -Recurse -Force }
        git clone --depth 1 https://github.com/xiph/opus $work
        if ($LASTEXITCODE -ne 0) { throw "git clone of opus failed (exit $LASTEXITCODE)" }
    }
    $build  = Join-Path $work 'build'
    $prefix = Join-Path $work 'prefix'
    cmake -G "MinGW Makefiles" -S $work -B $build -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=ON -DOPUS_BUILD_TESTING=OFF "-DCMAKE_INSTALL_PREFIX=$prefix"
    if ($LASTEXITCODE -ne 0) { throw "cmake configure (opus) failed (exit $LASTEXITCODE)" }
    cmake --build $build -j 8
    if ($LASTEXITCODE -ne 0) { throw "cmake build (opus) failed (exit $LASTEXITCODE)" }
    cmake --install $build
    if ($LASTEXITCODE -ne 0) { throw "cmake install (opus) failed (exit $LASTEXITCODE)" }
    Copy-Item (Join-Path $prefix 'lib\libopus.dll.a') $impLibPath -Force
    Copy-Item (Join-Path $prefix 'include\opus\*') $incDest -Force
}

function Ensure-OpusHeaders([string]$incDest) {
    if (Test-Path (Join-Path $incDest 'opus.h')) { return }
    $src = Join-Path $ScratchLibs 'prefix\opus\include\opus'
    if (Test-Path $src) {
        Copy-Item (Join-Path $src '*') $incDest -Force
        Write-Host "  OK  copied opus headers" -ForegroundColor Green
    } else {
        Write-Host "  WARN opus headers not found in scratchpad prefix" -ForegroundColor Yellow
    }
}

function Write-OpusPc([string]$libsFlag) {
    $opusDir   = Join-Path $LibsDir 'opus'
    $pcPath    = Join-Path $opusDir 'lib\pkgconfig\opus.pc'
    $prefixFwd = ($opusDir -replace '\\', '/')
    $pc = @"
prefix=$prefixFwd
libdir=`${prefix}
includedir=`${prefix}/include

Name: Opus
Description: Opus IETF audio codec
Version: 0
Libs: -L`${libdir} $libsFlag
Cflags: -I`${includedir}/opus -I`${includedir}
"@
    [System.IO.File]::WriteAllText($pcPath, $pc)
    Write-Host "  OK  wrote opus.pc ($libsFlag)" -ForegroundColor Green
}

function Ensure-OpusLib {
    $opusDir = Join-Path $LibsDir 'opus'
    $impLib  = Join-Path $opusDir 'libopus.dll.a'
    $dllCopy = Join-Path $opusDir 'libopus-0.dll'
    $incDest = Join-Path $opusDir 'include\opus'
    $libsFlag = '-lopus'
    if (Test-Path $impLib) {
        Write-Host "  OK  opus import lib present" -ForegroundColor DarkGray
    } elseif (Test-Path $dllCopy) {
        Write-Host "  OK  opus DLL present for direct linking" -ForegroundColor DarkGray
        $libsFlag = '-l:libopus-0.dll'
    } else {
        $installedDll = Join-Path $InstallDir 'libopus-0.dll'
        $prefixDllA   = Join-Path $ScratchLibs 'prefix\opus\lib\libopus.dll.a'
        if ((Test-Path $installedDll) -and $script:HaveImpTools) {
            Write-Host "Generating opus import lib from installed libopus-0.dll ..." -ForegroundColor Cyan
            New-MinGWImportLib $installedDll 'libopus-0.dll' $impLib
            Write-Host "  OK  opus import lib -> $impLib" -ForegroundColor Green
        } elseif (Test-Path $installedDll) {
            # No gendef/dlltool: mingw ld links directly against the DLL via -l:filename.
            Write-Host "Copying installed libopus-0.dll for direct linking ..." -ForegroundColor Cyan
            Copy-Item $installedDll $dllCopy -Force
            $libsFlag = '-l:libopus-0.dll'
            Write-Host "  OK  opus DLL -> $dllCopy" -ForegroundColor Green
        } elseif (Test-Path $prefixDllA) {
            Write-Host "Using scratchpad opus import lib ..." -ForegroundColor Cyan
            Copy-Item $prefixDllA $impLib -Force
            Write-Host "  OK  opus import lib -> $impLib" -ForegroundColor Green
        } else {
            Write-Host "Building opus from source (cmake fallback) ..." -ForegroundColor Yellow
            Build-OpusFromSource $impLib $incDest
            Write-Host "  OK  opus import lib -> $impLib" -ForegroundColor Green
        }
    }
    Ensure-OpusHeaders $incDest
    Write-OpusPc $libsFlag
}

function Ensure-OggLib {
    $oggDir  = Join-Path $LibsDir 'ogg'
    $impLib  = Join-Path $oggDir 'libogg.dll.a'
    $incDest = Join-Path $oggDir 'include\ogg'
    if (Test-Path $impLib) {
        Write-Host "  OK  ogg import lib present" -ForegroundColor DarkGray
    } else {
        $installedDll = Join-Path $InstallDir 'libogg-0.dll'
        $prefixDllA   = Join-Path $ScratchLibs 'prefix\ogg\lib\libogg.dll.a'
        if ((Test-Path $installedDll) -and $script:HaveImpTools) {
            Write-Host "Generating ogg import lib from installed libogg-0.dll ..." -ForegroundColor Cyan
            New-MinGWImportLib $installedDll 'libogg-0.dll' $impLib
            Write-Host "  OK  ogg import lib -> $impLib" -ForegroundColor Green
        } elseif (Test-Path $prefixDllA) {
            Copy-Item $prefixDllA $impLib -Force
            Write-Host "  OK  copied ogg import lib from scratchpad prefix" -ForegroundColor Green
        } else {
            Write-Host "  WARN ogg import lib unavailable (shipped DLL is unused by the exe; continuing)" -ForegroundColor Yellow
        }
    }
    if (-not (Test-Path (Join-Path $incDest 'ogg.h'))) {
        $src = Join-Path $ScratchLibs 'prefix\ogg\include\ogg'
        if (Test-Path $src) {
            Copy-Item (Join-Path $src '*') $incDest -Force
            Write-Host "  OK  copied ogg headers" -ForegroundColor Green
        }
    }
}

function Get-VoskZip([string]$destZip) {
    $url = 'https://github.com/alphacep/vosk-api/releases/download/v0.3.45/vosk-win64-0.3.45.zip'
    Write-Host "Downloading vosk from $url ..." -ForegroundColor Yellow
    Invoke-WebRequest -Uri $url -UseBasicParsing -OutFile $destZip
}

# The vosk zip contains libvosk.lib (a ready-to-use import lib), vosk_api.h and libvosk.dll.
function Expand-VoskZip([string]$zipPath, [string]$destDir) {
    $tmp = Join-Path $destDir '_ziptmp'
    if (Test-Path $tmp) { Remove-Item $tmp -Recurse -Force }
    New-Item -ItemType Directory -Force -Path $tmp | Out-Null
    Expand-Archive -Path $zipPath -DestinationPath $tmp -Force
    $inner = Get-ChildItem -Path $tmp -Directory | Select-Object -First 1
    $srcRoot = if ($null -ne $inner) { $inner.FullName } else { $tmp }
    Copy-Item (Join-Path $srcRoot 'libvosk.lib') (Join-Path $destDir 'libvosk.lib') -Force
    Copy-Item (Join-Path $srcRoot 'vosk_api.h')  (Join-Path $destDir 'vosk_api.h')  -Force
    Copy-Item (Join-Path $srcRoot 'libvosk.dll') (Join-Path $destDir 'libvosk.dll') -Force
    Remove-Item $tmp -Recurse -Force
}

function Ensure-Vosk {
    $voskDir  = Join-Path $LibsDir 'vosk'
    $voskLib  = Join-Path $voskDir 'libvosk.lib'
    $voskImpA = Join-Path $voskDir 'libvosk.dll.a'
    $voskHdr  = Join-Path $voskDir 'vosk_api.h'
    $haveLib  = (Test-Path $voskLib) -or (Test-Path $voskImpA)
    if ($haveLib -and (Test-Path $voskHdr)) {
        Write-Host "  OK  vosk lib/header present" -ForegroundColor DarkGray
        return
    }
    $installDll = Join-Path $InstallDir 'libvosk.dll'
    $installHdr = Join-Path $InstallDir 'vosk_api.h'
    $prefixVosk = Join-Path $ScratchLibs 'prefix\vosk'
    $zip        = Join-Path $ScratchLibs 'vosk-dl\vosk-win64-0.3.45.zip'
    if ((Test-Path $installDll) -and (Test-Path $installHdr) -and $script:HaveImpTools) {
        Write-Host "Generating vosk import lib from installed libvosk.dll ..." -ForegroundColor Cyan
        Copy-Item $installHdr $voskHdr -Force
        Copy-Item $installDll (Join-Path $voskDir 'libvosk.dll') -Force
        New-MinGWImportLib $installDll 'libvosk.dll' $voskImpA
    } elseif (Test-Path $prefixVosk) {
        Write-Host "Using scratchpad prefix vosk ..." -ForegroundColor Cyan
        Copy-Item (Join-Path $prefixVosk 'libvosk.lib') $voskLib -Force
        Copy-Item (Join-Path $prefixVosk 'vosk_api.h')  $voskHdr -Force
        Copy-Item (Join-Path $prefixVosk 'libvosk.dll') (Join-Path $voskDir 'libvosk.dll') -Force
    } elseif (Test-Path $zip) {
        Write-Host "Expanding scratchpad vosk zip ..." -ForegroundColor Cyan
        Expand-VoskZip $zip $voskDir
    } else {
        $tmpZip = Join-Path $voskDir 'vosk.zip'
        Get-VoskZip $tmpZip
        Expand-VoskZip $tmpZip $voskDir
        Remove-Item $tmpZip -Force -ErrorAction SilentlyContinue
    }
    Write-Host "  OK  vosk ready ($voskDir)" -ForegroundColor Green
}

function Ensure-Libs {
    Write-Host "Preparing CGO libraries in $LibsDir ..." -ForegroundColor Cyan
    New-Item -ItemType Directory -Force -Path $LibsDir, (Join-Path $LibsDir 'opus\include\opus'), (Join-Path $LibsDir 'opus\lib\pkgconfig'), (Join-Path $LibsDir 'ogg\include\ogg'), (Join-Path $LibsDir 'vosk') | Out-Null
    Ensure-OpusLib
    Ensure-OggLib
    Ensure-Vosk
}

function Invoke-Windres {
    $winDir = Join-Path $WirePodDir 'windows'
    Write-Host "Compiling resource file ..." -ForegroundColor Cyan
    Push-Location $winDir
    try {
        windres cmd/rc/app.rc -O coff -o cmd/app.syso
        if ($LASTEXITCODE -ne 0) { throw "windres failed (exit $LASTEXITCODE)" }
    } finally {
        Pop-Location
    }
    Write-Host "  OK  compiled cmd/app.syso" -ForegroundColor Green
}

function Invoke-GoBuild {
    $winDir  = Join-Path $WirePodDir 'windows'
    $oggInc  = ((Join-Path $LibsDir 'ogg\include')        -replace '\\', '/')
    $opusInc = ((Join-Path $LibsDir 'opus\include')       -replace '\\', '/')
    $voskDir = ((Join-Path $LibsDir 'vosk')               -replace '\\', '/')
    $opusLib = ((Join-Path $LibsDir 'opus')               -replace '\\', '/')
    $oggLib  = ((Join-Path $LibsDir 'ogg')                -replace '\\', '/')
    $pkgCfg  = ((Join-Path $LibsDir 'opus\lib\pkgconfig') -replace '\\', '/')

    $sha = (git -C $RepoDir rev-parse --short HEAD)
    if ($LASTEXITCODE -ne 0) { throw "git rev-parse failed in $RepoDir (exit $LASTEXITCODE)" }
    $sha = $sha.Trim()

    Write-Host "Building chipper.exe (commit $sha) ..." -ForegroundColor Cyan
    Push-Location $winDir
    try {
        $env:CGO_ENABLED     = '1'
        $env:GOOS            = 'windows'
        $env:GOARCH          = 'amd64'
        $env:CGO_CFLAGS      = "-I$oggInc -I$opusInc -I$voskDir"
        $env:CGO_LDFLAGS     = "-L$opusLib -L$oggLib -L$voskDir"
        $env:PKG_CONFIG_PATH = $pkgCfg
        $ldflags = "-H=windowsgui -w -s -X 'github.com/kercre123/wire-pod/chipper/pkg/vars.CommitSHA=$sha'"
        go build -tags nolibopusfile -ldflags $ldflags -o chipper.exe ./cmd
        if ($LASTEXITCODE -ne 0) { throw "go build failed (exit $LASTEXITCODE)" }
    } finally {
        Pop-Location
    }
    Write-Host ("  OK  built {0}" -f (Join-Path $winDir 'chipper.exe')) -ForegroundColor Green
}

function Invoke-Deploy {
    if (-not (Test-Admin)) {
        Write-Host "Deploy requires an elevated (Administrator) PowerShell. Re-run 'Run as administrator'." -ForegroundColor Red
        exit 1
    }
    $exe = Join-Path (Join-Path $WirePodDir 'windows') 'chipper.exe'
    if (-not (Test-Path $exe)) { throw "Built exe not found at $exe" }

    Write-Host "Stopping running chipper (if any) ..." -ForegroundColor Cyan
    Stop-Process -Name chipper -Force -ErrorAction SilentlyContinue
    Start-Sleep -Milliseconds 500

    Write-Host "Copying exe -> $InstallDir" -ForegroundColor Cyan
    Copy-Item $exe (Join-Path $InstallDir 'chipper.exe') -Force

    Write-Host "Mirroring webroot ..." -ForegroundColor Cyan
    $src = Join-Path $RepoDir 'chipper\webroot'
    $dst = Join-Path $InstallDir 'webroot'
    robocopy $src $dst /MIR /NFL /NDL /NJH /NJS | Out-Null
    # robocopy: exit codes 0-7 indicate success (files copied/extra removed); >= 8 is a real failure.
    if ($LASTEXITCODE -ge 8) { throw "robocopy of webroot failed (exit $LASTEXITCODE)" }
    Write-Host "  OK  webroot mirrored" -ForegroundColor Green

    $verFile = Join-Path $InstallDir 'version'
    if (Test-Path $verFile) {
        $ver = ([System.IO.File]::ReadAllText($verFile)).Trim()
        if (-not $ver.EndsWith('-custom')) {
            [System.IO.File]::WriteAllText($verFile, $ver + '-custom')
            Write-Host "  OK  version -> $ver-custom" -ForegroundColor Green
        } else {
            Write-Host "  OK  version already suffixed ($ver)" -ForegroundColor DarkGray
        }
    }

    Write-Host "Launching chipper ..." -ForegroundColor Cyan
    Start-Process (Join-Path $InstallDir 'chipper.exe') -ArgumentList '-d' -WorkingDirectory $InstallDir

    Write-Host "Waiting for wire-pod to report healthy ..." -ForegroundColor Cyan
    $deadline = (Get-Date).AddSeconds(30)
    $healthy = $false
    while ((Get-Date) -lt $deadline) {
        try {
            $r = Invoke-WebRequest -Uri 'http://localhost:8080/api/is_running' -UseBasicParsing -TimeoutSec 3
            $body = if ($r.Content -is [string]) { $r.Content } else { [System.Text.Encoding]::UTF8.GetString($r.Content) }
            if ($body.Trim() -eq 'true') { $healthy = $true; break }
        } catch {
            # server not up yet - keep polling until the deadline
        }
        Start-Sleep -Milliseconds 1000
    }
    if (-not $healthy) { throw "wire-pod did not report healthy within 30s" }
    Write-Host "Deploy complete - wire-pod is running." -ForegroundColor Green
}

# --- Main ---
Write-Host "wire-pod Windows build" -ForegroundColor Cyan
Assert-Tools
Ensure-WirePodClone
Ensure-GoModReplace
Ensure-Libs
Invoke-Windres
Invoke-GoBuild
if ($Deploy) { Invoke-Deploy }
Write-Host "Done." -ForegroundColor Green

# backup-vector.ps1 — back up a wire-pod-connected Vector's photos, settings,
# stats, and server-side identity files into a timestamped folder.
#
# Usage (from repo root, normal PowerShell window):
#   powershell -ExecutionPolicy Bypass -File scripts\backup-vector.ps1
#
# Prereqs: WirePod running (http://localhost:8080 loads) and Vector awake,
# on the same network as this PC.

param(
    [string]$Serial  = "00303f28",
    [string]$WirePod = "http://localhost:8080",
    [string]$OutRoot = "$env:USERPROFILE\Documents\vector-backup"
)

$ErrorActionPreference = "Stop"
$stamp = Get-Date -Format "yyyy-MM-dd_HH-mm-ss"
$dest  = Join-Path $OutRoot $stamp
New-Item -ItemType Directory -Force -Path $dest, (Join-Path $dest "photos"), (Join-Path $dest "server-data") | Out-Null

# PS 5.1: .Content is [string] for text responses, [byte[]] for binary ones.
function Get-ApiText([string]$path) {
    $resp = Invoke-WebRequest -Uri "$WirePod$path" -UseBasicParsing -TimeoutSec 30
    $text = if ($resp.Content -is [string]) { $resp.Content }
            else { [System.Text.Encoding]::UTF8.GetString($resp.Content) }
    if ($text.StartsWith("error:")) { throw "wire-pod returned '$text' for $path" }
    return $text
}

Write-Host "Backing up Vector $Serial to $dest" -ForegroundColor Cyan

# --- 1. Force-pull + save settings, lifetime stats, face list (names only) ---
foreach ($item in @(
    @{ Path = "/api-sdk/get_sdk_settings?serial=$Serial"; File = "settings.json";  Label = "Robot settings" },
    @{ Path = "/api-sdk/get_robot_stats?serial=$Serial";  File = "stats.json";     Label = "Lifetime stats" },
    @{ Path = "/api-sdk/get_faces?serial=$Serial";        File = "faces.json";     Label = "Enrolled face names" }
)) {
    try {
        $text = Get-ApiText $item.Path
        [System.IO.File]::WriteAllText((Join-Path $dest $item.File), $text)
        Write-Host ("  OK  {0} -> {1}" -f $item.Label, $item.File)
    } catch {
        Write-Host ("  SKIP {0}: {1}" -f $item.Label, $_.Exception.Message) -ForegroundColor Yellow
    }
}

# --- 2. Photos (full-res JPEGs) ---
$photoCount = 0
try {
    $ids = (Get-ApiText "/api-sdk/get_image_ids?serial=$Serial") | ConvertFrom-Json
    if ($null -eq $ids) { $ids = @() }
    foreach ($id in @($ids)) {
        $file = Join-Path $dest ("photos\photo_{0:D4}.jpg" -f [int]$id)
        Invoke-WebRequest -Uri "$WirePod/api-sdk/get_image?id=$id&serial=$Serial" -UseBasicParsing -TimeoutSec 60 -OutFile $file
        # sanity: JPEGs start with FF D8
        $head = [System.IO.File]::ReadAllBytes($file)[0..1]
        if ($head[0] -eq 0xFF -and $head[1] -eq 0xD8) {
            $photoCount++
        } else {
            $err = [System.IO.File]::ReadAllText($file)
            Remove-Item $file -Force
            Write-Host "  WARN photo $id was not a JPEG ($($err.Substring(0, [Math]::Min(60, $err.Length)))), skipped" -ForegroundColor Yellow
        }
    }
    Write-Host ("  OK  {0} photo(s) saved" -f $photoCount)
} catch {
    Write-Host ("  SKIP photos: {0}" -f $_.Exception.Message) -ForegroundColor Yellow
}

# --- 3. Server-side identity + jdocs (wire-pod's data dir) ---
$dataDir = Join-Path $env:APPDATA "wire-pod"
foreach ($rel in @("jdocs\jdocs.json", "jdocs\botSdkInfo.json", "apiConfig.json")) {
    $src = Join-Path $dataDir $rel
    if (Test-Path $src) {
        Copy-Item $src (Join-Path $dest "server-data") -Force
        Write-Host "  OK  copied $rel"
    }
}
foreach ($relDir in @("session-certs", "certs")) {
    $src = Join-Path $dataDir $relDir
    if (Test-Path $src) {
        Copy-Item $src (Join-Path $dest "server-data\$relDir") -Recurse -Force
        Write-Host "  OK  copied $relDir\"
    }
}

Write-Host ""
Write-Host "Done. Backup at: $dest" -ForegroundColor Green
Write-Host "(Reminder: face RECOGNITION data cannot leave the robot - faces.json is names/timestamps only.)"

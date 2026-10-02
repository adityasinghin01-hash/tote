# tote installer for Windows (PowerShell 5+). Paste into PowerShell:
#
#   & ([scriptblock]::Create((irm <base>/install.ps1))) <ticket>
#
# Downloads tote into %USERPROFILE%\.tote-guest\bin (only for you, no admin),
# checks it against SHA256SUMS, then opens your box in guest mode.
# `tote leave` removes everything, including tote itself.
param([string]$Ticket = "")
$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"   # PS 5's progress bar makes downloads crawl
try { [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12 } catch {}

$base = if ($env:TOTE_BASE) { $env:TOTE_BASE } else { "https://github.com/adityasinghin01-hash/tote/releases/latest/download" }
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }
$root = if ($env:TOTE_GUEST_ROOT) { $env:TOTE_GUEST_ROOT } else { Join-Path $HOME ".tote-guest" }
$dir = Join-Path $root "bin"
New-Item -ItemType Directory -Force -Path $dir | Out-Null
$file = "tote-windows-$arch.exe"
$exe = Join-Path $dir "tote.exe"
$part = "$exe.part"

Write-Host "Downloading tote (windows/$arch)..."
Invoke-WebRequest -UseBasicParsing -Uri "$base/$file" -OutFile $part
$sums = (Invoke-WebRequest -UseBasicParsing -Uri "$base/SHA256SUMS").Content
if ($sums -is [byte[]]) { $sums = [Text.Encoding]::UTF8.GetString($sums) }
$line = ($sums -split "`r?`n") | Where-Object { $_ -match (" " + [regex]::Escape($file) + "$") } | Select-Object -First 1
$want = if ($line) { ($line -split " ")[0].Trim().ToLower() } else { "" }
$got = (Get-FileHash -Algorithm SHA256 -Path $part).Hash.ToLower()
if (-not $want -or $want -ne $got) {
  Remove-Item -Force $part
  throw "tote: the download didn't match its checksum - not running it"
}
Move-Item -Force $part $exe
Write-Host "tote is in $exe (only for you; 'tote leave' removes it)`n"

if (-not $Ticket) { Write-Host "Now run:  & `"$exe`" guest <ticket>"; return }
if ($env:TOTE_YES) { & $exe guest $Ticket --then-run --yes } else { & $exe guest $Ticket --then-run }

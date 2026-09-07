<#
.SYNOPSIS
  Build release packages locally (what the GitHub release workflow does).

.EXAMPLE
  .\packaging\build.ps1 -Version 0.2.0
  .\packaging\build.ps1 -Version 0.2.0 -Targets windows-x64          # just the Windows exe + installer
  .\packaging\build.ps1 -Version 0.2.0 -SkipInstaller                # no Inno Setup needed

  Output lands in .\dist:
    emcomm-objects-<ver>-windows-x64-portable.zip
    EmcommObjects-Setup-<ver>.exe                (needs Inno Setup 6)
    emcomm-objects-<ver>-{macos-arm64,macos-x64,linux-x64,linux-arm64}.tar.gz
    SHA256SUMS.txt
#>
param(
  [string]$Version = "dev",
  [string[]]$Targets = @("windows-x64", "macos-arm64", "macos-x64", "linux-x64", "linux-arm64"),
  [switch]$SkipInstaller
)
$ErrorActionPreference = "Stop"
$root = Resolve-Path (Join-Path $PSScriptRoot "..")
Set-Location $root
$dist = Join-Path $root "dist"
New-Item -ItemType Directory -Force $dist | Out-Null

$commit = (git rev-parse --short HEAD 2>$null); if (-not $commit) { $commit = "" }
$date = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
$mod = "github.com/kk4oda/emcomm-objects/internal/version"
$ldBase = "-s -w -X $mod.Version=$Version -X $mod.Commit=$commit -X $mod.BuildDate=$date"

# Windows resources (icon + version info) via go-winres -> cmd/emcomm-objects/*.syso
if ($Targets -contains "windows-x64") {
  $winres = Get-Command go-winres -ErrorAction SilentlyContinue
  if (-not $winres) { go install github.com/tc-hib/go-winres@latest; $winres = Join-Path (go env GOPATH) "bin\go-winres.exe" } else { $winres = $winres.Source }
  $numVer = if ($Version -match '^\d+\.\d+\.\d+') { $Matches[0] } else { "0.0.0" }
  & $winres make --in packaging/winres.json --out cmd/emcomm-objects/rsrc --product-version "$numVer.0" --file-version "$numVer.0" --arch amd64
  if ($LASTEXITCODE -ne 0) { throw "go-winres failed" }
}

$map = @{
  "windows-x64" = @("windows", "amd64"); "macos-arm64" = @("darwin", "arm64"); "macos-x64" = @("darwin", "amd64")
  "linux-x64"   = @("linux", "amd64");   "linux-arm64" = @("linux", "arm64")
}
foreach ($t in $Targets) {
  $goos, $goarch = $map[$t]
  $out = Join-Path $dist $t
  Remove-Item -Recurse -Force $out -ErrorAction SilentlyContinue
  New-Item -ItemType Directory -Force $out | Out-Null
  $bin = if ($goos -eq "windows") { "emcomm-objects.exe" } else { "emcomm-objects" }
  $ld = $ldBase
  if ($goos -eq "windows") { $ld += " -H windowsgui" }   # no console window; logs go to the data dir + UI
  $env:GOOS = $goos; $env:GOARCH = $goarch; $env:CGO_ENABLED = "0"
  Write-Host "building $t ..."
  go build -trimpath -ldflags $ld -o (Join-Path $out $bin) ./cmd/emcomm-objects
  if ($LASTEXITCODE -ne 0) { throw "go build failed for $t" }
  Copy-Item README.md, LICENSE, config.example.yaml $out
  Copy-Item internal/web/ui/symbols/COPYRIGHT.md (Join-Path $out "SYMBOLS-COPYRIGHT.md")
  if ($goos -eq "windows") {
    Compress-Archive -Force -Path "$out\*" -DestinationPath (Join-Path $dist "emcomm-objects-$Version-$t-portable.zip")
    if (-not $SkipInstaller) {
      $iscc = "${env:ProgramFiles(x86)}\Inno Setup 6\ISCC.exe"
      if (Test-Path $iscc) {
        & $iscc /Q "/DAppVersion=$Version" "/DSourceDir=$out" (Join-Path $root "packaging\emcomm-objects.iss")
        if ($LASTEXITCODE -ne 0) { throw "ISCC failed" }
      } else { Write-Warning "Inno Setup 6 not found; installer skipped" }
    }
  } else {
    $name = "emcomm-objects-$Version-$t"
    $stage = Join-Path $dist $name
    Remove-Item -Recurse -Force $stage -ErrorAction SilentlyContinue
    Rename-Item $out $name
    tar -czf (Join-Path $dist "$name.tar.gz") -C $dist $name
    Remove-Item -Recurse -Force $stage
  }
}
Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED -ErrorAction SilentlyContinue
Remove-Item -Recurse -Force (Join-Path $dist "windows-x64") -ErrorAction SilentlyContinue

Push-Location $dist
Get-ChildItem -File | Where-Object { $_.Name -ne "SHA256SUMS.txt" } | ForEach-Object {
  "{0}  {1}" -f (Get-FileHash $_.FullName -Algorithm SHA256).Hash.ToLower(), $_.Name
} | Set-Content -Encoding ascii SHA256SUMS.txt
Get-Content SHA256SUMS.txt
Pop-Location
Write-Host "done: $dist"

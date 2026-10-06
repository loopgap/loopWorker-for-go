<#
.SYNOPSIS
  Windows-native build/verify script for LoopWorker. No bash, no GNU coreutils,
  no make required — this is the path a Windows customer (or CI on
  windows-latest) uses. It is the PowerShell equivalent of the Makefile and,
  critically, it writes `bin\loopworker.exe` (the Makefile previously produced a
  suffix-less `bin\loopworker` on Windows, which cmd/PowerShell cannot execute).

.DESCRIPTION
  Tasks: build | test | race | fmt | cover | gate | vet | lint | vuln | smoke |
         docker | release | clean | all
  Tool availability is probed, never assumed: if golangci-lint / govulncheck /
  goreleaser / docker are missing the task reports SKIP (exit code stays 0) for
  optional tools, and FAILS for required ones (go, git).

.EXAMPLE
  powershell -ExecutionPolicy Bypass -File .release\build.ps1
  pwsh .release/build.ps1 -Task all -CoverageMin 80
#>
[CmdletBinding()]
param(
    [ValidateSet('build','test','race','fmt','vet','lint','vuln','cover','gate',
                 'smoke','docker','release','clean','all','version')]
    [string]$Task = 'build',

    # Only the shippable binary by default; -AllClis also builds the dev CLIs
    # that are deliberately NOT part of the release bundle (SCOPE-PROPOSAL.md).
    [switch]$AllClis,

    [string]$Version = '',
    [string]$CoverageMin = '80',
    [string]$ImageTag = 'loopworker:dev'
)

$ErrorActionPreference = 'Stop'
$RepoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $RepoRoot

$Product  = 'loopworker'
$DevClis  = @('loopctl','loopdebug','loopwatch','loopbench','loopsim')
$BinDir   = Join-Path $RepoRoot 'bin'

function Write-Step([string]$m) { Write-Host "==> $m" -ForegroundColor Cyan }
function Write-Ok([string]$m)    { Write-Host "    OK  $m" -ForegroundColor Green }
function Write-Skip([string]$m)  { Write-Host "    SKIP $m" -ForegroundColor Yellow }

function Get-Tool([string]$name) {
    $c = Get-Command $name -ErrorAction SilentlyContinue
    if ($c) { return $c.Source } else { return $null }
}

function Require-Tool([string]$name) {
    $p = Get-Tool $name
    if (-not $p) { throw "$name not found in PATH — install it (Go: https://go.dev/dl/)" }
    return $p
}

# ---------------------------------------------------------------- metadata --
$go = Require-Tool 'go'

$gitPath = Get-Tool 'git'
if ($Version -eq '') {
    # Falls back to the in-tree beta version when no tag exists yet; keep in sync
    # with version/version.go and the Makefile VERSION fallback.
    $beta = '0.1.0-beta'
    if ($gitPath) {
        $Version = (& $gitPath describe --tags --always --dirty 2>$null)
        if (-not $Version) { $Version = $beta }
    } else { $Version = $beta }
}
$GitCommit = 'unknown'
$BuildDate = 'unknown'
if ($gitPath) {
    $GitCommit = (& $gitPath rev-parse --short HEAD 2>$null)
    if (-not $GitCommit) { $GitCommit = 'unknown' }
    $BuildDate = (& $gitPath log -1 --format=%cI 2>$null)
    if (-not $BuildDate) { $BuildDate = 'unknown' }
}

# Import path of the version package must equal "<module>/version" from go.mod.
$Module = 'loopworker'
$goMod = Join-Path $RepoRoot 'go.mod'
if (Test-Path $goMod) {
    $m = Select-String -Path $goMod -Pattern '^module\s+(\S+)' | Select-Object -First 1
    if ($m) { $Module = $m.Matches[0].Groups[1].Value }
}

$ldflags = @(
    '-s','-w',
    "-X=$Module/version.Version=$Version",
    "-X=$Module/version.GitCommit=$GitCommit",
    "-X=$Module/version.BuildDate=$BuildDate"
)

$Targets = @($Product)
if ($AllClis) { $Targets += $DevClis }

# ------------------------------------------------------------------ tasks --
function Do-Build {
    Write-Step "build $Version ($Module) for windows/amd64 -> bin\<name>.exe"
    if (-not (Test-Path $BinDir)) { New-Item -ItemType Directory -Path $BinDir | Out-Null }
    foreach ($t in $Targets) {
        $out = Join-Path $BinDir "$t.exe"
        & $go build -trimpath -ldflags ($ldflags -join ' ') -o $out "./cmd/$t"
        if ($LASTEXITCODE -ne 0) { throw "go build ./cmd/$t failed (exit $LASTEXITCODE)" }
        if (-not (Test-Path $out)) { throw "expected artifact $out was not produced" }
        Write-Ok ("{0} ({1:N0} bytes)" -f $out, (Get-Item $out).Length)
    }
    # Prove the artifact is actually executable, not just present: a 0-byte or
    # suffix-less file is the failure mode this script exists to prevent.
    & (Join-Path $BinDir "$Product.exe") version
    if ($LASTEXITCODE -ne 0) { throw "bin\$Product.exe version returned $LASTEXITCODE" }
    Write-Ok "bin\$Product.exe is runnable"
}

function Do-Test  { Write-Step 'test'; & $go test -count=1 ./...; if ($LASTEXITCODE -ne 0) { throw 'tests failed' } }
function Do-Race  { Write-Step 'test -race ./... (ALL packages)'; & $go test -race -count=1 ./...; if ($LASTEXITCODE -ne 0) { throw 'race tests failed' } }
function Do-Vet   { Write-Step 'go vet'; & $go vet ./...; if ($LASTEXITCODE -ne 0) { throw 'go vet failed' } }

function Do-Fmt {
    Write-Step 'gofmt -l'
    $gofmt = Require-Tool 'gofmt'
    $bad = @(& $gofmt -l . 2>$null |
             Where-Object { $_ -notmatch '^(tmp-audit|dist)[\\/]' })
    if ($bad.Count -gt 0) { $bad | ForEach-Object { Write-Host "  not formatted: $_" }; throw 'gofmt check failed' }
    Write-Ok 'gofmt clean'
}

function Do-Lint {
    Write-Step 'golangci-lint run ./...'
    $gl = Get-Tool 'golangci-lint'
    if (-not $gl) { Write-Skip 'golangci-lint not installed (CI job `quality` runs it)'; return }
    & $gl run ./...
    if ($LASTEXITCODE -ne 0) { throw 'golangci-lint failed' }
}

function Do-Vuln {
    Write-Step 'govulncheck ./...'
    $gv = Get-Tool 'govulncheck'
    if (-not $gv) { Write-Skip 'govulncheck not installed (CI job `quality` runs it)'; return }
    & $gv ./...
    if ($LASTEXITCODE -ne 0) { throw 'govulncheck found vulnerabilities' }
}

function Do-Cover {
    Write-Step 'coverage (all packages, -coverpkg=./...)'
    & $go test -count=1 -covermode=atomic '-coverpkg=./...' '-coverprofile=coverage.out' ./...
    if ($LASTEXITCODE -ne 0) { throw 'coverage test run failed' }
    & $go tool cover '-func=coverage.out' | Select-Object -Last 1
}

function Do-Gate {
    Write-Step "coverage gate >= $CoverageMin%"
    if (-not (Test-Path (Join-Path $RepoRoot 'coverage.out'))) { Do-Cover }
    Push-Location (Join-Path $RepoRoot '.release/tools')
    try {
        & $go run ./covergate -file ../../coverage.out -min $CoverageMin
        if ($LASTEXITCODE -ne 0) { throw "coverage below ${CoverageMin}%" }
    } finally { Pop-Location }
}

function Do-Smoke {
    Write-Step 'boot smoke: start server, GET /api/v1/health, SIGTERM/Stop, expect clean exit'
    if (-not (Test-Path (Join-Path $BinDir "$Product.exe"))) { Do-Build }
    & (Join-Path $PSScriptRoot 'smoke.ps1') -BinPath (Join-Path $BinDir "$Product.exe")
    if ($LASTEXITCODE -ne 0) { throw "boot smoke failed (exit $LASTEXITCODE)" }
}

function Do-Docker {
    Write-Step "docker build -t $ImageTag ."
    $dk = Get-Tool 'docker'
    if (-not $dk) { Write-Skip 'docker not installed'; return }
    & $dk build -t $ImageTag .
    if ($LASTEXITCODE -ne 0) { throw 'docker build failed' }
    & $dk run --rm $ImageTag version
    if ($LASTEXITCODE -ne 0) { throw 'container version check failed' }
}

function Do-Release {
    Write-Step 'release pipeline verification (snapshot, no publish)'
    Push-Location (Join-Path $RepoRoot '.release/tools')
    try {
        & $go run ./relcheck -root ../..
        if ($LASTEXITCODE -ne 0) { throw 'relcheck failed' }
    } finally { Pop-Location }
    $gr = Get-Tool 'goreleaser'
    if (-not $gr) { Write-Skip 'goreleaser not installed; ran relcheck only'; return }
    & $gr check
    if ($LASTEXITCODE -ne 0) { throw 'goreleaser check failed' }
    & $gr release --snapshot --clean --skip=publish
    if ($LASTEXITCODE -ne 0) { throw 'goreleaser snapshot release failed' }
    Write-Ok "artifacts in .\dist (check dist\checksums.txt)"
}

function Do-Clean {
    Write-Step 'clean'
    foreach ($p in @($BinDir, (Join-Path $RepoRoot 'dist'),
                       (Join-Path $RepoRoot 'coverage.out'),
                       (Join-Path $RepoRoot 'coverage.html'))) {
        if (Test-Path $p) { Remove-Item -Recurse -Force $p; Write-Ok "removed $p" }
    }
}

function Do-Version {
    Write-Step 'build metadata'
    [pscustomobject]@{
        Version=$Version; GitCommit=$GitCommit; BuildDate=$BuildDate;
        Module=$Module; Go=(& $go version); BinSuffix='.exe'; Targets=($Targets -join ',')
    } | Format-List
}

switch ($Task) {
    'build'    { Do-Build }
    'test'     { Do-Test }
    'race'     { Do-Race }
    'fmt'      { Do-Fmt }
    'vet'      { Do-Vet }
    'lint'     { Do-Lint }
    'vuln'     { Do-Vuln }
    'cover'    { Do-Cover }
    'gate'     { Do-Gate }
    'smoke'    { Do-Smoke }
    'docker'   { Do-Docker }
    'release'  { Do-Release }
    'clean'    { Do-Clean }
    'version'  { Do-Version }
    'all'      { Do-Fmt; Do-Vet; Do-Lint; Do-Build; Do-Test; Do-Race; Do-Gate; Do-Smoke }
    default    { Do-Build }
}

Write-Host "`nTask '$Task' completed." -ForegroundColor Green
exit 0

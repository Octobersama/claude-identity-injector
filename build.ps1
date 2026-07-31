param(
    [string]$OutputDir = "dist",
    [string]$GccBin = ""
)

$ErrorActionPreference = "Stop"

if ($GccBin) {
    $resolvedGccBin = (Resolve-Path -LiteralPath $GccBin).Path
    $env:PATH = "$resolvedGccBin;$env:PATH"
}

if (-not (Get-Command gcc -ErrorAction SilentlyContinue)) {
    throw "gcc was not found. Add MinGW to PATH or pass -GccBin."
}

$resolvedOutputDir = Join-Path $PSScriptRoot $OutputDir
New-Item -ItemType Directory -Force -Path $resolvedOutputDir | Out-Null

$env:CGO_ENABLED = "1"
go test ./...
go build -buildmode=c-shared -o (Join-Path $resolvedOutputDir "claude-identity-injector.dll") .

$header = Join-Path $resolvedOutputDir "claude-identity-injector.h"
if (Test-Path -LiteralPath $header) {
    Remove-Item -LiteralPath $header
}

Write-Host "Built $(Join-Path $resolvedOutputDir 'claude-identity-injector.dll')"

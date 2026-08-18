# Build AgentQuay C++ SDK with MSVC + Qt 6.8.3 (msvc2022_64)
$ErrorActionPreference = 'Stop'

$scriptDir = Split-Path $MyInvocation.MyCommand.Path
$qtDir = 'D:\Qt\6.8.3\msvc2022_64'
$buildDir = Join-Path $scriptDir 'build'
$bridgeDist = Join-Path $scriptDir '..\..\bridge\dist'
$bridgeBinItem = Get-ChildItem $bridgeDist -Filter 'agentquay-windows-amd64.exe' -ErrorAction SilentlyContinue
$bridgeBin = if ($bridgeBinItem) { $bridgeBinItem.FullName } else { $null }

$env:QTDIR = $qtDir
$env:PATH = "$qtDir\bin;$env:PATH"

if (-not (Test-Path $buildDir)) { New-Item -ItemType Directory -Force -Path $buildDir | Out-Null }

Write-Host '=== Step 1: Configure ==='
$vcBat = 'D:\Program Files\Microsoft Visual Studio\18\Community\VC\Auxiliary\Build\vcvars64.bat'
& cmd /c "`"$vcBat`" && cmake -S `"$scriptDir`" -B `"$buildDir`" -DCMAKE_BUILD_TYPE=Release -DAGENTQUAY_BUILD_TESTS=ON -DAGENTQUAY_BUILD_EXAMPLES=ON -G Ninja"
$rc = $LASTEXITCODE
Write-Host "Configure exit: $rc"
if ($rc -ne 0) { throw "CMake configure failed" }

Write-Host '=== Step 2: Build ==='
& cmd /c "`"$vcBat`" && cmake --build `"$buildDir`" --config Release -j"
$rc = $LASTEXITCODE
Write-Host "Build exit: $rc"
if ($rc -ne 0) { throw "Build failed" }

Write-Host '=== Step 3: Schema tests ==='
& (Join-Path $buildDir 'tests\schema_tests.exe')
$rc = $LASTEXITCODE
Write-Host "Schema test exit: $rc"

if ($bridgeBin) {
    $env:AGENTQUAY_BRIDGE_BIN = $bridgeBin
    Write-Host "=== Step 4: E2E tests (Bridge: $bridgeBin) ==="
    & (Join-Path $buildDir 'tests\e2e_tests.exe')
    $rc = $LASTEXITCODE
    Write-Host "E2E test exit: $rc"
} else {
    Write-Host '=== Step 4: Skip e2e (no bridge binary) ==='
}

Write-Host '=== Done ==='

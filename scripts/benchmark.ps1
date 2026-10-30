param(
    [string]$Go = "go",
    [string]$OutputDirectory = "dist/bench/results",
    [string]$Bench = ".",
    [string]$Benchtime = "300ms",
    [int]$Count = 3,
    [switch]$Profile
)

$ErrorActionPreference = "Stop"
$repo = Split-Path $PSScriptRoot -Parent
$goCommand = (Get-Command $Go -ErrorAction Stop).Source
if (-not [IO.Path]::IsPathRooted($OutputDirectory)) {
    $OutputDirectory = Join-Path $repo $OutputDirectory
}
$output = [IO.Path]::GetFullPath($OutputDirectory)
New-Item -ItemType Directory -Force $output | Out-Null
$isolation = Join-Path $output ("isolated-" + [Guid]::NewGuid().ToString("N"))
$keys = @("HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "XDG_CONFIG_HOME", "GOCACHE", "GOMODCACHE")
$previous = @{}
Push-Location $repo
try {
    # Keep downloaded Go/module sources out of `go test ./...` and `go vet ./...`.
    $artifacts = Join-Path $repo "dist/bench"
    New-Item -ItemType Directory -Force $artifacts | Out-Null
    $moduleBoundary = Join-Path $artifacts "go.mod"
    if (-not (Test-Path -LiteralPath $moduleBoundary)) {
        Set-Content -LiteralPath $moduleBoundary -Value "module benchmark-artifacts`n`ngo 1.22`n" -Encoding utf8
    }
    foreach ($key in $keys) {
        $previous[$key] = [Environment]::GetEnvironmentVariable($key, "Process")
        $dir = if ($key -in @("GOCACHE", "GOMODCACHE")) {
            Join-Path $repo "dist/bench/$key"
        } else {
            Join-Path $isolation $key
        }
        New-Item -ItemType Directory -Force $dir | Out-Null
        [Environment]::SetEnvironmentVariable($key, $dir, "Process")
    }
    & $goCommand version | Tee-Object -FilePath (Join-Path $output "environment.txt")
    & $goCommand env GOOS GOARCH | Add-Content (Join-Path $output "environment.txt")
    git rev-parse HEAD | Add-Content (Join-Path $output "environment.txt")
    "CPU=$env:PROCESSOR_IDENTIFIER LogicalProcessors=$env:NUMBER_OF_PROCESSORS" |
        Add-Content (Join-Path $output "environment.txt")
    "Bench=$Bench Benchtime=$Benchtime Count=$Count Profile=$Profile" |
        Add-Content (Join-Path $output "environment.txt")
    foreach ($package in @("flp", "app")) {
        $exe = Join-Path $output "$package.test.exe"
        & $goCommand test -c -o $exe "./internal/$package"
        if ($LASTEXITCODE -ne 0) { throw "Cannot build $package benchmarks" }
        $arguments = @("-test.run=^$", "-test.bench=$Bench", "-test.benchmem", "-test.benchtime=$Benchtime", "-test.count=$Count")
        if ($Profile) {
            $arguments += @("-test.cpuprofile=$(Join-Path $output "$package.cpu.pprof")", "-test.memprofile=$(Join-Path $output "$package.mem.pprof")")
        }
        & $exe @arguments | Tee-Object -FilePath (Join-Path $output "$package.txt")
        if ($LASTEXITCODE -ne 0) { throw "$package benchmarks failed" }
    }
} finally {
    foreach ($key in $previous.Keys) {
        [Environment]::SetEnvironmentVariable($key, $previous[$key], "Process")
    }
    Pop-Location
}
# Raw results, profiles, executables and isolated settings stay under ignored dist/.
# No project, account, installed-plugin directory or running app is consulted.

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
$distDir = Join-Path $repoRoot 'dist'
New-Item -ItemType Directory -Force -Path $distDir | Out-Null

$head = $null
try { $head = & git -C $repoRoot rev-parse --short=12 HEAD 2>$null } catch { $head = $null }
$commit = if ($LASTEXITCODE -eq 0 -and $head) { $head.Trim() } else { 'no-commit' }
$status = @()
try { $status = & git -C $repoRoot status --porcelain 2>$null } catch { $status = @('unknown') }
$dirty = if ($status) { 'true' } else { 'false' }
$builtAt = (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')
$ldflags = "-s -w -X main.commit=$commit -X main.builtAt=$builtAt -X main.dirty=$dirty"

$previousGoOS = $env:GOOS
$previousGoARCH = $env:GOARCH
$previousLocation = Get-Location
Set-Location $repoRoot
try {
    $env:GOARCH = 'amd64'
    $env:GOOS = 'windows'
    $windowsPath = Join-Path $distDir 'sysdev-wso2-airgap-installer.exe'
    go build -trimpath -ldflags $ldflags -o $windowsPath ./cmd/sysdev-wso2-airgap-installer
    if ($LASTEXITCODE -ne 0) { throw 'Windows amd64 build failed.' }

    $env:GOOS = 'linux'
    $linuxPath = Join-Path $distDir 'sysdev-wso2-airgap-installer'
    go build -trimpath -ldflags $ldflags -o $linuxPath ./cmd/sysdev-wso2-airgap-installer
    if ($LASTEXITCODE -ne 0) { throw 'Linux amd64 build failed.' }

    Get-FileHash -Algorithm SHA256 -LiteralPath $windowsPath, $linuxPath |
        ForEach-Object { '{0}  {1}' -f $_.Hash.ToLowerInvariant(), (Split-Path -Leaf $_.Path) } |
        Set-Content -Encoding ascii -LiteralPath (Join-Path $distDir 'SHA256SUMS')
    Write-Output "Built local Windows and Linux amd64 artifacts in $distDir"
    Write-Output 'No tag or release was created or published.'
}
finally {
    $env:GOOS = $previousGoOS
    $env:GOARCH = $previousGoARCH
    Set-Location $previousLocation
}

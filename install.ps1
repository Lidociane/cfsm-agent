$ErrorActionPreference = "Stop"

$Repo = if ($env:CF_PROBE_REPO) { $env:CF_PROBE_REPO } else { "huilang-me/cfsm-agent" }
$GitHubProxy = if ($env:CF_PROBE_GH_PROXY) { $env:CF_PROBE_GH_PROXY } else { "" }
$InstallVersion = if ($env:CF_PROBE_VERSION) { $env:CF_PROBE_VERSION } else { "latest" }

$needValueFor = ""
foreach ($arg in $args) {
    if ($needValueFor) {
        switch ($needValueFor) {
            "proxy" { $GitHubProxy = $arg }
            "version" { $InstallVersion = $arg }
        }
        $needValueFor = ""
        continue
    }
    switch -Regex ($arg) {
        "^--install-ghproxy=(.+)$" { $GitHubProxy = $Matches[1]; continue }
        "^--install-ghproxy$" { $needValueFor = "proxy"; continue }
        "^--install-version=(.+)$" { $InstallVersion = $Matches[1]; continue }
        "^--install-version$" { $needValueFor = "version"; continue }
    }
}

function Get-ArchName {
    switch ($env:PROCESSOR_ARCHITECTURE) {
        "AMD64" { "amd64"; break }
        "ARM64" { "arm64"; break }
        "x86" { "386"; break }
        default { throw "unsupported architecture: $env:PROCESSOR_ARCHITECTURE" }
    }
}

function Find-InstalledBinary {
    $candidates = @(
        (Join-Path $env:ProgramFiles "cf-probe\cf-probe.exe")
    )
    foreach ($candidate in $candidates) {
        if (Test-Path $candidate) { return $candidate }
    }
    return $null
}

$command = if ($args.Count -gt 0) { $args[0] } else { "install" }
$payloadArgs = @($args)
if ($command -in @("uninstall", "remove", "delete", "purge")) {
    $installed = Find-InstalledBinary
    if ($env:CF_PROBE_UNINSTALL_USE_INSTALLED -eq "1" -and $installed) {
        & $installed @payloadArgs
        exit $LASTEXITCODE
    }
    Write-Host "[INFO] downloading temporary uninstaller"
}

$arch = Get-ArchName
$asset = "cf-probe-windows-$arch.exe"
$path = if ($InstallVersion -eq "latest") { "latest/download" } else { "download/$InstallVersion" }
$url = "https://github.com/$Repo/releases/$path/$asset"
if ($GitHubProxy) {
    $url = $GitHubProxy.TrimEnd("/") + "/" + $url
}

$tmp = Join-Path $env:TEMP "cf-probe-bootstrap-$PID.exe"
Write-Host "CF-Server-Monitor Go Probe bootstrap"
Write-Host "  repo    : $Repo"
Write-Host "  version : $InstallVersion"
Write-Host "  target  : windows/$arch"
Write-Host "  asset   : $asset"
Write-Host "  url     : $url"

try {
    Invoke-WebRequest -Uri $url -OutFile $tmp -UseBasicParsing
    if ($args.Count -eq 0) {
        & $tmp install
    } else {
        & $tmp @payloadArgs
    }
    exit $LASTEXITCODE
} finally {
    Remove-Item $tmp -Force -ErrorAction SilentlyContinue
}

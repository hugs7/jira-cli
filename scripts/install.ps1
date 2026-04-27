# Install the latest release of `jr` from GitHub Releases.
# Usage:
#   irm https://raw.githubusercontent.com/hugs7/jira-cli/main/scripts/install.ps1 | iex
#   $env:JR_VERSION="v0.2.0"; irm .../install.ps1 | iex

[CmdletBinding()]
param(
  [string]$Version = $env:JR_VERSION,
  [string]$BinDir  = (Join-Path $env:LOCALAPPDATA 'jr\bin')
)

$ErrorActionPreference = 'Stop'
$Repo = 'hugs7/jira-cli'
$Bin  = 'jr'

if (-not $Version) {
  $rel = Invoke-RestMethod -Headers @{ 'User-Agent' = 'jr-installer' } `
    -Uri "https://api.github.com/repos/$Repo/releases/latest"
  $Version = $rel.tag_name
}
$Num = $Version.TrimStart('v')

$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
  'AMD64' { 'amd64' }
  'ARM64' { 'arm64' }
  default { throw "unsupported arch: $env:PROCESSOR_ARCHITECTURE" }
}

$asset = "${Bin}_${Num}_windows_${arch}.zip"
$url   = "https://github.com/$Repo/releases/download/$Version/$asset"

$tmp = New-Item -ItemType Directory -Path (Join-Path $env:TEMP "jr-install-$([guid]::NewGuid())")
try {
  Write-Host "downloading $asset…"
  $zip = Join-Path $tmp $asset
  Invoke-WebRequest -Uri $url -OutFile $zip -UseBasicParsing
  Expand-Archive -Path $zip -DestinationPath $tmp -Force

  if (-not (Test-Path $BinDir)) { New-Item -ItemType Directory -Path $BinDir | Out-Null }
  Copy-Item -Force -Path (Join-Path $tmp "$Bin.exe") -Destination (Join-Path $BinDir "$Bin.exe")

  $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
  if (($userPath -split ';') -notcontains $BinDir) {
    [Environment]::SetEnvironmentVariable('Path', "$userPath;$BinDir", 'User')
    Write-Host "added $BinDir to your user PATH (open a new terminal)"
  }

  Write-Host "installed $Bin $Version -> $(Join-Path $BinDir "$Bin.exe")"
  & (Join-Path $BinDir "$Bin.exe") version
}
finally {
  Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}

[CmdletBinding()]
param(
  [Parameter(Mandatory = $true)]
  [ValidatePattern('^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$')]
  [string]$Version,
  [string]$InstallDir = "$env:LOCALAPPDATA\mivia\bin"
)

$ErrorActionPreference = 'Stop'
$arch = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
switch ($arch.ToUpperInvariant()) {
  'AMD64' { $goarch = 'amd64' }
  'ARM64' { $goarch = 'arm64' }
  default { throw "install: unsupported architecture: $arch" }
}

$versionNumber = $Version.Substring(1)
$archive = "mivia_${versionNumber}_windows_${goarch}.zip"
$base = "https://github.com/MiviaLabs/mivia-agent/releases/download/$Version"
$temp = Join-Path ([System.IO.Path]::GetTempPath()) ("mivia-install-" + [guid]::NewGuid())
New-Item -ItemType Directory -Path $temp | Out-Null
try {
  $archivePath = Join-Path $temp $archive
  $checksumsPath = Join-Path $temp 'checksums.txt'
  Invoke-WebRequest -UseBasicParsing -Uri "$base/$archive" -OutFile $archivePath
  Invoke-WebRequest -UseBasicParsing -Uri "$base/checksums.txt" -OutFile $checksumsPath

  $line = Get-Content $checksumsPath | Where-Object { $_ -match ("\s" + [regex]::Escape($archive) + "$") }
  if ($line.Count -ne 1) { throw 'install: archive is missing from checksums.txt' }
  $expected = ($line -split '\s+')[0].ToLowerInvariant()
  $actual = (Get-FileHash -Algorithm SHA256 -Path $archivePath).Hash.ToLowerInvariant()
  if ($actual -ne $expected) { throw 'install: checksum verification failed' }

  $extract = Join-Path $temp 'extract'
  Expand-Archive -Path $archivePath -DestinationPath $extract
  New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
  Copy-Item (Join-Path $extract 'mivia.exe') (Join-Path $InstallDir 'mivia.exe') -Force
  Write-Output "installed mivia $Version to $InstallDir\mivia.exe"
  if (-not (($env:Path -split ';') -contains $InstallDir)) {
    Write-Warning "Add $InstallDir to PATH before running mivia."
  }
}
finally {
  Remove-Item -Recurse -Force -Path $temp -ErrorAction SilentlyContinue
}

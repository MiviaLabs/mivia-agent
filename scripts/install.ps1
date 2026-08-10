[CmdletBinding()]
param(
  [Parameter(Mandatory = $true)]
  [ValidatePattern('^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$')]
  [string]$Version,
  [string]$InstallDir = "$env:LOCALAPPDATA\mivia\bin",
  [switch]$NoPathUpdate
)

$ErrorActionPreference = 'Stop'
$InstallDir = [System.IO.Path]::GetFullPath($InstallDir)
if ($InstallDir.Length -gt 3) { $InstallDir = $InstallDir -replace '[\\/]+$', '' }
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
  $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
  $pathEntries = @($userPath -split ';' | Where-Object { $_ -ne '' })
  $pathPresent = $pathEntries | Where-Object { $_.Equals($InstallDir, [StringComparison]::OrdinalIgnoreCase) }
  if ($pathPresent) {
    Write-Output "$InstallDir is already on the user PATH"
  } elseif ($NoPathUpdate) {
    Write-Warning "PATH update skipped. Add $InstallDir to the user PATH."
  } else {
    try {
      [Environment]::SetEnvironmentVariable('Path', (($pathEntries + $InstallDir) -join ';'), 'User')
      Write-Output "added $InstallDir to the user PATH"
      Write-Output 'Open a new terminal before you run mivia.'
    } catch {
      Write-Warning "Cannot update the user PATH. Add $InstallDir to the user PATH."
    }
  }
}
finally {
  Remove-Item -Recurse -Force -Path $temp -ErrorAction SilentlyContinue
}

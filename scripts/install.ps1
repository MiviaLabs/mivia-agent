[CmdletBinding()]
param(
  [Parameter(Mandatory = $false)]
  [ValidatePattern('^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$')]
  [string]$Version,
  [string]$InstallDir = "$env:LOCALAPPDATA\mivia\bin",
  [switch]$NoPathUpdate
)

$ErrorActionPreference = 'Stop'

# Pin TLS 1.2: PowerShell 5.1 may otherwise negotiate TLS 1.0/1.1 for the
# downloads below. The -bor keeps TLS 1.3 when the runtime already supports it.
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
$temp = Join-Path ([System.IO.Path]::GetTempPath()) ("mivia-install-" + [guid]::NewGuid())
New-Item -ItemType Directory -Path $temp | Out-Null
try {
if (-not $Version) { $Version = $env:MIVIA_VERSION }
$versionPointer = $false
if (-not $Version) {
  try {
    $Version = (Invoke-WebRequest -UseBasicParsing -Uri 'https://github.com/MiviaLabs/mivia-agent/releases/latest/download/mivia-version.txt').Content.Trim()
    $versionPointer = $true
  } catch { throw 'install: no stable release is published yet' }
}
$Version = $Version.Trim()
if ($Version -notmatch '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$') {
  throw 'install: version must use semantic version format'
}
if ($versionPointer -and $Version -notmatch '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$') {
  throw 'install: latest release pointer is not a stable semantic version'
}
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
  $archivePath = Join-Path $temp $archive
  $checksumsPath = Join-Path $temp 'checksums.txt'
  Invoke-WebRequest -UseBasicParsing -Uri "$base/$archive" -OutFile $archivePath
  Invoke-WebRequest -UseBasicParsing -Uri "$base/checksums.txt" -OutFile $checksumsPath

  $lines = @(Get-Content $checksumsPath | Where-Object { $_ -match ('^([0-9A-Fa-f]{64})\s{2}' + [regex]::Escape($archive) + '$') })
  if ($lines.Count -eq 0) { throw 'install: archive is missing from checksums.txt' }
  if ($lines.Count -ne 1) { throw 'install: archive has duplicate checksums' }
  $expected = ([regex]::Match($lines[0], '^([0-9A-Fa-f]{64})')).Groups[1].Value.ToLowerInvariant()
  $actual = (Get-FileHash -Algorithm SHA256 -Path $archivePath).Hash.ToLowerInvariant()
  if ($actual -ne $expected) { throw 'install: checksum verification failed' }

  $extract = Join-Path $temp 'extract'
  Add-Type -AssemblyName System.IO.Compression.FileSystem
  $zip = [System.IO.Compression.ZipFile]::OpenRead($archivePath)
  try {
    $names = @{}
    foreach ($entry in $zip.Entries) {
      if ($entry.FullName -match '(^[\\/]|(^|[\\/])\.\.([\\/]|$))' -or $entry.FullName.EndsWith('/')) { throw 'install: archive contents are unsafe' }
      if ($entry.FullName -notin @('mivia.exe', 'README.md', 'LICENSE')) { throw 'install: archive contents are unexpected' }
      if ($names.ContainsKey($entry.FullName)) { throw 'install: archive contains duplicate files' }
      $names[$entry.FullName] = $true
    }
    if ($names.Count -ne 3 -or -not $names.ContainsKey('mivia.exe')) { throw 'install: archive has no complete file set' }
  } finally { $zip.Dispose() }
  Expand-Archive -Path $archivePath -DestinationPath $extract
  $binary = Join-Path $extract 'mivia.exe'
  if (-not (Test-Path $binary -PathType Leaf)) { throw 'install: archive has no regular mivia.exe binary' }
  New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
  $staged = Join-Path $InstallDir ('.mivia.new.' + [guid]::NewGuid())
  Copy-Item $binary $staged -Force
  Move-Item $staged (Join-Path $InstallDir 'mivia.exe') -Force
  Write-Output "installed mivia $Version to $InstallDir\mivia.exe"
  $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
  $pathEntries = @($userPath -split ';' | Where-Object { $_ -ne '' })
  $pathPresent = $pathEntries | Where-Object { ([System.IO.Path]::GetFullPath($_.TrimEnd('\'))).Equals($InstallDir, [StringComparison]::OrdinalIgnoreCase) }
  $currentPathPresent = $env:Path -split ';' | Where-Object { $_.Equals($InstallDir, [StringComparison]::OrdinalIgnoreCase) }
  if ($NoPathUpdate) {
    Write-Warning "PATH update skipped. Add $InstallDir to the user PATH."
  } elseif ($pathPresent) {
    if (-not $currentPathPresent) { $env:Path = "$InstallDir;$env:Path" }
    Write-Output "$InstallDir is already on the user PATH"
  } else {
    try {
      [Environment]::SetEnvironmentVariable('Path', (($pathEntries + $InstallDir) -join ';'), 'User')
      if (-not (($env:Path -split ';') | Where-Object { $_.Equals($InstallDir, [StringComparison]::OrdinalIgnoreCase) })) {
        $env:Path = "$InstallDir;$env:Path"
      }
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

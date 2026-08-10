$ErrorActionPreference = 'Stop'

$root = Split-Path -Parent $PSScriptRoot
$installer = Join-Path $PSScriptRoot 'install.ps1'
$temp = Join-Path ([System.IO.Path]::GetTempPath()) ('mivia-installer-test-' + [guid]::NewGuid())
$priorVersion = $env:MIVIA_VERSION

try {
  $fixture = Join-Path $temp 'fixture'
  $installDir = Join-Path $temp 'install'
  New-Item -ItemType Directory -Force -Path $fixture | Out-Null
  $arch = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
  switch ($arch.ToUpperInvariant()) {
    'AMD64' { $goarch = 'amd64' }
    'ARM64' { $goarch = 'arm64' }
    default { throw "unsupported test architecture: $arch" }
  }
  $archive = "mivia_1.2.3_windows_$goarch.zip"
  $archiveRoot = Join-Path $temp 'archive'
  New-Item -ItemType Directory -Force -Path $archiveRoot | Out-Null
  [System.IO.File]::WriteAllBytes((Join-Path $archiveRoot 'mivia.exe'), [byte[]](1,2,3,4))
  Set-Content -NoNewline -Path (Join-Path $archiveRoot 'README.md') -Value 'mivia test'
  Set-Content -NoNewline -Path (Join-Path $archiveRoot 'LICENSE') -Value 'test license'
  Compress-Archive -Path (Join-Path $archiveRoot '*') -DestinationPath (Join-Path $fixture $archive)
  $hash = (Get-FileHash -Algorithm SHA256 -Path (Join-Path $fixture $archive)).Hash.ToLowerInvariant()
  Set-Content -NoNewline -Path (Join-Path $fixture 'checksums.txt') -Value "$hash  $archive"
  Set-Content -NoNewline -Path (Join-Path $fixture 'mivia-version.txt') -Value 'v1.2.3'

  function global:Invoke-WebRequest {
    param([string]$Uri, [string]$OutFile, [switch]$UseBasicParsing)
    $name = [System.IO.Path]::GetFileName(([uri]$Uri).AbsolutePath)
    if ($OutFile) {
      Copy-Item -LiteralPath (Join-Path $fixture $name) -Destination $OutFile -Force
      return
    }
    return [pscustomobject]@{ Content = [System.IO.File]::ReadAllText((Join-Path $fixture $name)) }
  }

  $env:MIVIA_VERSION = 'not-a-version'
  $invalidAccepted = $false
  try {
    & $installer -InstallDir $installDir -NoPathUpdate
    $invalidAccepted = $true
  } catch {}
  if ($invalidAccepted) { throw 'installer accepted an invalid MIVIA_VERSION' }

  $env:MIVIA_VERSION = 'v1.2.3'
  & $installer -InstallDir $installDir -NoPathUpdate
  $installed = Join-Path $installDir 'mivia.exe'
  if (-not (Test-Path -LiteralPath $installed -PathType Leaf)) { throw 'installer did not write mivia.exe' }
  if ([BitConverter]::ToString([System.IO.File]::ReadAllBytes($installed)) -ne '01-02-03-04') { throw 'installer wrote incorrect binary data' }
  if (($env:Path -split ';') -contains $installDir) { throw 'installer changed PATH with -NoPathUpdate' }
} finally {
  Remove-Item Function:\global:Invoke-WebRequest -ErrorAction SilentlyContinue
  $env:MIVIA_VERSION = $priorVersion
  Remove-Item -Recurse -Force -Path $temp -ErrorAction SilentlyContinue
}

Write-Output 'PowerShell installer contracts: ok'

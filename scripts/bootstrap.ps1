# Requires Windows PowerShell 5.1+ or PowerShell 7; modern Windows only.
[CmdletBinding()]
param(
    [Parameter(Mandatory=$true)][Uri]$ArchiveUrl,
    [Parameter(Mandatory=$true)][ValidatePattern('^[a-fA-F0-9]{64}$')][string]$Sha256,
    [string]$Destination = (Join-Path $env:LOCALAPPDATA 'ShellPier')
)
$ErrorActionPreference = 'Stop'
if ($PSVersionTable.PSVersion -lt [Version]'5.1') { throw 'Use the independent legacy entrypoint on older PowerShell.' }
if ($ArchiveUrl.Scheme -ne 'https') { throw 'Archive URL must use HTTPS.' }
if (Test-Path -LiteralPath $Destination) { throw 'Destination already exists; choose a new empty path.' }
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
$work = Join-Path ([IO.Path]::GetTempPath()) ('shellpier-download-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $work | Out-Null
try {
    $archive = Join-Path $work 'release.zip'
    Invoke-WebRequest -UseBasicParsing -MaximumRedirection 0 -Uri $ArchiveUrl -OutFile $archive
    $actual = (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash
    if ($actual -ne $Sha256) { throw 'SHA-256 mismatch; archive was not extracted.' }
    Expand-Archive -LiteralPath $archive -DestinationPath $Destination
    $exe = Join-Path $Destination 'shellpier.exe'
    if (-not (Test-Path -LiteralPath $exe)) { throw 'Archive lacks shellpier.exe.' }
    Write-Output ('Installed into ' + $Destination)
    Write-Output 'Download verified and extracted; run the executable explicitly when ready.'
} finally { Remove-Item -LiteralPath $work -Recurse -Force }

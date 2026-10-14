# Requires Windows PowerShell 5.1+ or PowerShell 7; modern Windows only.
[CmdletBinding()]
param(
    [Parameter(Mandatory=$true)][Uri]$ArchiveUrl,
    [string]$Destination = (Join-Path $env:LOCALAPPDATA 'ShellPier')
)
$ErrorActionPreference = 'Stop'
if ($PSVersionTable.PSVersion -lt [Version]'5.1') { throw 'Use the independent legacy entrypoint on older PowerShell.' }
if ($ArchiveUrl.Scheme -ne 'https') { throw 'Archive URL must use HTTPS.' }
if (Test-Path -LiteralPath $Destination) { throw 'Destination already exists; choose a new empty path.' }
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

function Get-ShellPierResponse([Uri]$Uri) {
    $request = [Net.HttpWebRequest]::Create($Uri)
    $request.AllowAutoRedirect = $false
    $request.Timeout = 60000
    $request.ReadWriteTimeout = 60000
    return $request.GetResponse()
}

function Save-ShellPierArchive([Uri]$Uri, [string]$Path) {
    $current = $Uri
    for ($redirects = 0; $redirects -le 5; $redirects++) {
        if ($current.Scheme -ne 'https') { throw 'Every download hop must use HTTPS.' }
        $response = Get-ShellPierResponse $current
        try {
            $status = [int]$response.StatusCode
            if (@(301, 302, 303, 307, 308) -contains $status) {
                if ($redirects -eq 5) { throw 'Download exceeded five redirects.' }
                $location = $response.Headers['Location']
                if (-not $location) { throw 'Download redirect lacks a Location header.' }
                $next = [Uri]::new($current, $location)
                if ($next.Scheme -ne 'https') { throw 'Download redirect must use HTTPS.' }
                $current = $next
                continue
            }
            if ($status -ne 200) { throw ('Download returned HTTP ' + $status) }
            $inputStream = $response.GetResponseStream()
            try {
                $outputStream = [IO.File]::Open($Path, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write)
                try { $inputStream.CopyTo($outputStream) } finally { $outputStream.Dispose() }
            } finally { $inputStream.Dispose() }
            return
        } finally { $response.Close() }
    }
}

$work = Join-Path ([IO.Path]::GetTempPath()) ('shellpier-download-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $work | Out-Null
try {
    $archive = Join-Path $work 'release.zip'
    Save-ShellPierArchive $ArchiveUrl $archive
    Expand-Archive -LiteralPath $archive -DestinationPath $Destination
    $exe = Join-Path $Destination 'shellpier.exe'
    if (-not (Test-Path -LiteralPath $exe)) { throw 'Archive lacks shellpier.exe.' }
    Write-Output ('Downloaded and extracted into ' + $Destination)
    Write-Output 'Run the executable explicitly when ready.'
} finally { Remove-Item -LiteralPath $work -Recurse -Force }

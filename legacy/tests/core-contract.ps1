$ErrorActionPreference='Stop'
$root=Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
. (Join-Path $root 'core.ps1')
function Assert($condition,$message){if(-not $condition){throw $message}}
function Expect-Failure($Action) { $failed=$false; try { & $Action } catch { $failed=$true }; Assert $failed 'Expected refusal did not occur' }
$file=Join-Path ([IO.Path]::GetTempPath()) ('shellpier-core-'+[Guid]::NewGuid().ToString('N'))
try {
 [IO.File]::WriteAllText($file,'abc',[Text.Encoding]::ASCII)
 $hash='ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad'
 Assert ((Get-ShellPierHash $file) -ceq $hash) 'SHA256 mismatch'
 Assert ((Assert-ShellPierIPv4 '192.0.2.1') -ceq '192.0.2.1') 'Valid IPv4 rejected'
 Expect-Failure { Assert-ShellPierIPv4 '999.1.1.1' }
 Expect-Failure { Assert-ShellPierIPv4 '127.1' }
 Expect-Failure { Assert-ShellPierPort 65536 }
 Expect-Failure { Assert-ShellPierPort 0 }
 $script:status='Valid'; $script:publisher='Bitvise Limited'
 function Get-AuthenticodeSignature {
  $cert=New-Object PSObject
  $cert|Add-Member -MemberType ScriptMethod -Name GetNameInfo -Value { return $script:publisher }
  return New-Object PSObject -Property @{Status=$script:status; SignerCertificate=$cert}
 }
 Assert-ShellPierBinary $file $hash 'Bitvise Limited'
 Expect-Failure { Assert-ShellPierBinary $file ('0'*64) 'Bitvise Limited' }
 $script:status='UnknownError'
 Expect-Failure { Assert-ShellPierBinary $file $hash 'Bitvise Limited' }
 $script:status='Valid'; $script:publisher='Unexpected Publisher'
 Expect-Failure { Assert-ShellPierBinary $file $hash 'Bitvise Limited' }
 Write-Host 'PASS SHA256, strict IPv4 and ports, corrupted binary, untrusted signature and wrong publisher refusals'
} finally { Remove-Item -LiteralPath $file -Force }

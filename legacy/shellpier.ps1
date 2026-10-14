# Run through shellpier.cmd on Windows PowerShell 2.0 or newer.
param(
    [string]$Command = 'help',
    [string]$Mode = 'direct',
    [string]$Installer,
    [string]$PublicKey,
    [string]$AllowedClient,
    [string]$ListenAddress = '0.0.0.0',
    [int]$Port = 2222,
    [int]$TtlMinutes = 180,
    [switch]$AcceptLicense,
    [switch]$AllowInstall,
    [string]$Session,
    [string]$Plink,
    [string]$RelayHost,
    [string]$RelayUser,
    [string]$RelayHostKey,
    [string]$RelayPrivateKey,
    [int]$RelayPort = 443,
    [int]$RemotePort = 2222,
    [string]$PlinkSha256 = 'c7be84cc42565ad4de7e77e99f0a61f3a8dec17c77d5ff1afad79d005af54c93',
    [string]$PlinkPublisher = 'Simon Tatham'
)
$ErrorActionPreference = 'Stop'
$scriptRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
. (Join-Path $scriptRoot 'core.ps1')
. (Join-Path $scriptRoot 'bitvise.ps1')
$root = Join-Path $env:ProgramData 'ShellPierLegacy'
$installerHash = 'd1407f989d18f4505bf623eb1c33a68cd5c79a483214ebc1ff86bee9821293a6'
function Write-State($State, [string]$Path) {
    $tmp = $Path + '.new'
    $State | Export-Clixml -Path $tmp
    Move-Item -LiteralPath $tmp -Destination $Path -Force
}
function Read-State([string]$Id) {
    if ($Id -notmatch '^[a-f0-9]{32}$') { throw 'Expected the 32-character session ID printed by start.' }
    $path = Join-Path (Join-Path $root $Id) 'state.xml'
    $state = Import-Clixml -Path $path
    if ($state.Id -cne $Id -or $state.Instance -cne ('ShellPier-' + $Id) -or $state.Owner -cne 'ShellPierLegacy-v1') { throw 'Invalid owned state.' }
    return $state
}
function Remove-Access($State) {
    $service = Get-ShellPierService $State.Instance
    if ($null -ne $service) {
        # Disable startup first so a reboot cannot restore expired access.
        Set-Service -Name $service.Name -StartupType Disabled
        Stop-Service -Name $service.Name -Force -ErrorAction Stop
        $svc = Get-Service -Name $service.Name
        $svc.WaitForStatus([ServiceProcess.ServiceControllerStatus]::Stopped,[TimeSpan]::FromSeconds(30))
        $cfg = New-Object -ComObject 'Bitvise.BssCfg'
        $locked = $false
        try {
            [void]$cfg.SetInstance($State.Instance)
            [void]$cfg.settings.Lock(); $locked = $true
            [void]$cfg.settings.Load()
            [void]$cfg.settings.access.winAccounts.Clear()
            [void]$cfg.settings.access.virtAccounts.Clear()
            [void]$cfg.settings.access.clientAddresses.Clear()
            [void]$cfg.settings.access.clientAddresses.NewClear()
            $cfg.settings.access.clientAddresses.new.addressRule.addressType = $cfg.enums.AddressVer6Type.anyIP
            $cfg.settings.access.clientAddresses.new.instr.allowConnect = $false
            [void]$cfg.settings.access.clientAddresses.NewCommit()
            [void]$cfg.settings.Save()
            [void]$cfg.settings.Load()
            if ($cfg.settings.access.winAccounts.count -ne 0 -or $cfg.settings.access.virtAccounts.count -ne 0 -or $cfg.settings.access.clientAddresses.count -ne 1 -or $cfg.settings.access.clientAddresses.GetItem(0).instr.allowConnect) { throw 'Revocation readback failed.' }
        } finally {
            if ($locked) { [void]$cfg.settings.Unlock() }
            if ($null -ne $cfg) { [void][Runtime.InteropServices.Marshal]::ReleaseComObject($cfg) }
        }
    }
    $policy = New-Object -ComObject HNetCfg.FwPolicy2
    # A GUID rule name and protected state scope deletion to this session.
    foreach ($rule in @($policy.Rules)) { if ($rule.Name -ceq $State.FirewallRule) { $policy.Rules.Remove($State.FirewallRule) } }
    $State.Status = 'stopped'
    $State | Add-Member -MemberType NoteProperty -Name StoppedUtc -Value ([DateTime]::UtcNow.ToString('o')) -Force
    Write-State $State (Join-Path (Join-Path $root $State.Id) 'state.xml')
    & "$env:SystemRoot\System32\schtasks.exe" /Delete /TN $State.TaskName /F | Out-Null
    if ($LASTEXITCODE -ne 0) { Write-Warning 'Access revoked; scheduled cleanup task could not be removed.' }
    Write-Host '[STOPPED] Named instance disabled, session accounts revoked and owned firewall rule removed. Installation and system audit logs retained.'
}
try {
    if ($Command -eq 'help') {
        Write-Host 'ShellPier legacy: doctor | prepare | start | status -Session ID | stop -Session ID'
        Write-Host 'start -Installer FILE -PublicKey FILE -AllowedClient IPv4 -AcceptLicense -AllowInstall [-Port 2222] [-TtlMinutes 180]'
        Write-Host 'prepare -Mode reverse -Plink FILE -RelayHost HOST -RelayUser USER -RelayHostKey FINGERPRINT -RelayPrivateKey FILE -PlinkSha256 SHA256'
        Write-Host 'No downloads, default-instance changes, automatic reboot or system-log deletion.'
    } elseif ($Command -eq 'doctor') {
        $report = Get-ShellPierDoctor
        $report | Select-Object OS,OSVersion,ServicePack,Architecture,PowerShell,CLR,Elevated,Proxy | Format-List
        Write-Host 'Identity (desktop account and operator may differ after elevation):'
        $report.Identity | Format-List
        Write-Host 'IPv4 interfaces (virtual and laboratory interfaces may not be remotely reachable):'
        $report.IPv4 | Format-List
        Write-Host '.NET installations:'
        $report.NetFramework | Format-List
        Write-Host 'Tool locations:'
        $report.Tools.GetEnumerator() | Format-Table -AutoSize
        Write-Host 'Firewall profile enablement:'
        $report.Firewall | Format-List
        Write-Host 'Existing Bitvise services (not modified):'
        $report.BitviseServices | Select-Object Name,DisplayName,State,StartMode | Format-Table -AutoSize
    } elseif ($Command -eq 'prepare') {
        Assert-ShellPierPort $Port
        if ($Mode -eq 'direct') {
            Write-Host '1. Obtain the official Bitvise SSH Server 9.66 installer offline. SHA256:' $installerHash
            Write-Host '2. Review the vendor EULA and choose a license valid for this use. No license is bundled or assumed.'
            Write-Host '3. Put one OpenSSH public key beside this CLI; keep the private key on the connecting machine.'
            Write-Host '4. From elevated CMD, run the start command shown by help. It creates a fresh named service and durable TTL task.'
            Write-Host '5. Verify the displayed SSH host key using the vendor control panel, then remotely run hostname and whoami.'
            Write-Host 'Use doctor to select the reachable interface. A TCP listener is not proof of remote authentication.'
        } elseif ($Mode -eq 'reverse') {
            Assert-ShellPierPort $RelayPort; Assert-ShellPierPort $RemotePort
            if ($RelayHost -notmatch '^[a-zA-Z0-9][a-zA-Z0-9.-]*$' -or $RelayUser -notmatch '^[a-zA-Z0-9_][a-zA-Z0-9_.-]*$') { throw 'Valid relay host and user are required.' }
            if ($RelayHostKey -notmatch '^SHA256:[a-zA-Z0-9+/]+={0,2}$') { throw 'A trusted relay SHA256 host key fingerprint is required; never auto-accept it.' }
            if (-not (Test-Path -LiteralPath $RelayPrivateKey -PathType Leaf)) { throw 'PuTTY .ppk key file is required; no password argument is generated.' }
            Assert-ShellPierBinary $Plink $PlinkSha256 $PlinkPublisher
            foreach ($path in @($Plink,$RelayPrivateKey)) { if ($path -match '[\r\n"%&|<>^!]') { throw 'Unsupported CMD metacharacter in path.' } }
            $line = '"' + $Plink + '" -ssh -batch -noagent -noshare -N -P ' + $RelayPort + ' -l ' + $RelayUser + ' -hostkey "' + $RelayHostKey + '" -i "' + $RelayPrivateKey + '" -R 127.0.0.1:' + $RemotePort + ':127.0.0.1:' + $Port + ' ' + $RelayHost
            Write-Host '[GUIDE_ONLY] Verified local plink bytes/publisher. No connection started:'
            Write-Host $line
            Write-Host 'Requires a local SSH server, relay permission for loopback reverse forwarding, and independent relay host-key verification.'
            Write-Host 'The local server sees the forwarded connection from 127.0.0.1; use a loopback listener/source policy, not the external client IPv4.'
            Write-Host 'TCP 443 here carries SSH, not HTTPS. HTTP CONNECT proxies, captive portals, NAT and SSH-blocking filters can prevent it.'
            Write-Host 'This generated Plink session has no automatic TTL. Use a supervised session and stop it explicitly; direct start manages its own server TTL.'
        } else { throw 'Mode must be direct or reverse.' }
    } elseif ($Command -eq 'status') {
        $state = Read-State $Session
        $service = Get-ShellPierService $state.Instance
        $running = ($null -ne $service -and $service.State -eq 'Running')
        $expired = ([DateTime]::UtcNow -ge [DateTime]::Parse($state.ExpiresUtc).ToUniversalTime())
        $listener = $null
        if ($running) { $listener = Get-ShellPierListener ([int]$service.ProcessId) ([int]$state.Port) }
        $label = $state.Status
        if ($expired -and $running) { $label = 'EXPIRED_BUT_RUNNING: cleanup needs immediate attention' }
        elseif ($running -and $null -ne $listener) { $label = 'LISTENING: remote login not verified' }
        elseif ($state.Status -eq 'running') { $label = 'DEGRADED: service or listener absent' }
        New-Object PSObject -Property @{Session=$state.Id; State=$label; ExpiresUtc=$state.ExpiresUtc; ServiceRunning=$running; Listener=$listener; Instance=$state.Instance} | Format-List
    } elseif ($Command -eq 'stop' -or $Command -eq '_expire') {
        Assert-ShellPierAdmin
        $state = Read-State $Session
        if ($Command -eq '_expire' -and [DateTime]::UtcNow -lt [DateTime]::Parse($state.ExpiresUtc).ToUniversalTime()) { throw 'Expiry has not been reached.' }
        Remove-Access $state
    } elseif ($Command -eq 'start') {
        Assert-ShellPierAdmin
        if (-not $AcceptLicense -or -not $AllowInstall) { throw 'Review the Bitvise EULA, then explicitly pass -AcceptLicense -AllowInstall to create a new dedicated service.' }
        if ($Mode -ne 'direct') { throw 'Automatic start supports direct only. Use prepare -Mode reverse for the Plink guide.' }
        Assert-ShellPierPort $Port
        if ($TtlMinutes -lt 2 -or $TtlMinutes -gt 1440) { throw 'TTL must be 2..1440 minutes.' }
        $AllowedClient = Assert-ShellPierIPv4 $AllowedClient
        $ListenAddress = Assert-ShellPierIPv4 $ListenAddress
        if ($AllowedClient -eq '0.0.0.0' -or $AllowedClient -eq '255.255.255.255') { throw 'One specific client IPv4 is required.' }
        if (-not (Test-Path -LiteralPath $PublicKey -PathType Leaf)) { throw 'Public key file is required.' }
        $text = [IO.File]::ReadAllText($PublicKey).Trim()
        if ($text -notmatch '^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp(256|384|521)) [a-zA-Z0-9+/]+=*( [^\r\n]*)?$') { throw 'Expected exactly one OpenSSH public key.' }
        Assert-ShellPierBinary $Installer $installerHash 'Bitvise Limited'
        $identity = Get-ShellPierIdentity
        $id = [Guid]::NewGuid().ToString('N')
        $instance = 'ShellPier-' + $id
        if ($null -ne (Get-ShellPierService $instance)) { throw 'Instance collision; no changes made.' }
        # Refuse any occupied target port before installation.
        $tcp = New-Object Net.Sockets.TcpListener([Net.IPAddress]::Parse($ListenAddress),$Port)
        try { $tcp.Start() } finally { $tcp.Stop() }
        if (Test-Path -LiteralPath $root) {
            $item = Get-Item -LiteralPath $root -Force
            if (-not $item.PSIsContainer -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint)) { throw 'State root must be a real directory, not a reparse point.' }
        } else { [void][IO.Directory]::CreateDirectory($root) }
        Set-ShellPierDirectoryAcl $root
        $run = Join-Path $root $id
        [void][IO.Directory]::CreateDirectory($run)
        Set-ShellPierDirectoryAcl $run
        foreach ($name in @('shellpier.ps1','core.ps1','bitvise.ps1')) { Copy-Item -LiteralPath (Join-Path $scriptRoot $name) -Destination (Join-Path $run $name) }
        $key = Join-Path $run 'access.pub'
        [IO.File]::WriteAllText($key,$text,[Text.Encoding]::ASCII)
        $expires = [DateTime]::UtcNow.AddMinutes($TtlMinutes)
        $state = New-Object PSObject -Property @{Owner='ShellPierLegacy-v1'; Id=$id; Instance=$instance; TaskName=('ShellPierLegacy-' + $id); FirewallRule=('ShellPierLegacy-' + $id); Status='preparing'; ExpiresUtc=$expires.ToString('o'); Port=$Port; AccountSid=$identity.Sid; OperatorSid=$identity.OperatorSid}
        $statePath = Join-Path $run 'state.xml'
        Write-State $state $statePath
        try {
            # A protected SYSTEM task survives terminal exit; it runs missed expiry after resume.
            $taskService = New-Object -ComObject 'Schedule.Service'
            $taskService.Connect()
            $task = $taskService.NewTask(0)
            $task.RegistrationInfo.Description = 'Revoke only this ShellPier legacy session at expiry.'
            $task.Principal.UserId = 'S-1-5-18'; $task.Principal.LogonType = 5; $task.Principal.RunLevel = 1
            $task.Settings.Enabled = $true; $task.Settings.StartWhenAvailable = $true
            $task.Settings.DisallowStartIfOnBatteries = $false; $task.Settings.StopIfGoingOnBatteries = $false
            $task.Settings.WakeToRun = $true; $task.Settings.ExecutionTimeLimit = 'PT5M'
            $trigger = $task.Triggers.Create(1)
            $trigger.StartBoundary = $expires.ToLocalTime().ToString('yyyy-MM-ddTHH:mm:ss')
            $action = $task.Actions.Create(0)
            $action.Path = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
            $action.Arguments = '-NoProfile -ExecutionPolicy Bypass -File "' + (Join-Path $run 'shellpier.ps1') + '" _expire -Session ' + $id
            $taskFolder = $taskService.GetFolder('\')
            [void]$taskFolder.RegisterTaskDefinition($state.TaskName,$task,6,'SYSTEM',$null,5)
            # No default instance, no -startService, no automatic restart.
            $process = Start-Process -FilePath $Installer -ArgumentList @(('-instance=' + $instance),'-acceptEULA') -Wait -PassThru
            if ($process.ExitCode -ne 0) { throw ('[INSTALLER_ATTENTION] Exit ' + $process.ExitCode + '. Review vendor messages/reboot requirements. No SSH start claimed.') }
            $service = Get-ShellPierService $instance
            if ($null -eq $service) { throw 'Named service was not created.' }
            Set-Service -Name $service.Name -StartupType Disabled
            Stop-Service -Name $service.Name -Force
            Invoke-ShellPierBitviseConfiguration -AccountName $identity.Account -InstanceName $instance -AllowedClient $AllowedClient -PublicKeyPath $key -BackupDirectory $run -Port $Port -ListenAddress $ListenAddress
            $service = Get-ShellPierService $instance
            if ($service.PathName -notmatch '^"([^"]+)"') { throw 'Cannot validate dedicated service executable path.' }
            $binary = $matches[1]
            $signature = Get-AuthenticodeSignature -FilePath $binary
            if ($signature.Status -ne 'Valid' -or $null -eq $signature.SignerCertificate -or $signature.SignerCertificate.GetNameInfo([Security.Cryptography.X509Certificates.X509NameType]::SimpleName,$false) -cne 'Bitvise Limited') { throw 'Dedicated service binary signature invalid.' }
            $policy = New-Object -ComObject HNetCfg.FwPolicy2
            $rule = New-Object -ComObject HNetCfg.FWRule
            $rule.Name=$state.FirewallRule; $rule.Description='ShellPier temporary session'; $rule.Protocol=6; $rule.LocalPorts=[string]$Port
            $rule.RemoteAddresses=$AllowedClient; $rule.ApplicationName=$binary; $rule.Direction=1; $rule.Action=1; $rule.Enabled=$true; $rule.Profiles=7
            $policy.Rules.Add($rule)
            $saved = $policy.Rules.Item($state.FirewallRule)
            if ($saved.ApplicationName -cne $binary -or $saved.LocalPorts -cne [string]$Port -or -not $saved.Enabled -or $saved.Action -ne 1 -or @($AllowedClient,($AllowedClient+'/32'),($AllowedClient+'/255.255.255.255')) -notcontains $saved.RemoteAddresses) { throw 'Firewall rule readback failed.' }
            if ([DateTime]::UtcNow -ge $expires) { throw 'TTL elapsed during installation; refusing to start.' }
            Set-Service -Name $service.Name -StartupType Manual
            Start-Service -Name $service.Name
            (Get-Service -Name $service.Name).WaitForStatus([ServiceProcess.ServiceControllerStatus]::Running,[TimeSpan]::FromSeconds(30))
            $service = Get-ShellPierService $instance
            $listener = Get-ShellPierListener ([int]$service.ProcessId) $Port
            if ($null -eq $listener) { throw 'Named service does not own the requested listener.' }
            $state.Status='running'; Write-State $state $statePath
            Write-Host '[LISTENING] Remote authentication still requires verification.'
            Write-Host ('Session: ' + $id + '; TTL UTC: ' + $state.ExpiresUtc)
            Write-Host ('Windows account: ' + $identity.Account + '; SID: ' + $identity.Sid)
            Write-Host ('ssh -p ' + $Port + ' -l "' + $identity.Account + '" WINDOWS_IPV4')
            Write-Host 'Verify host key out of band before connecting. Never send the private key or Windows password.'
            Write-Host ('stop: shellpier.cmd stop -Session ' + $id)
        } catch {
            $original = $_.Exception.Message
            try { Remove-Access $state } catch { Write-Warning ('[CLEANUP_FAILED] ' + $_.Exception.Message + '. Protected state and TTL task retained: ' + $id) }
            throw $original
        }
    } else { throw 'Unknown command; run help.' }
    exit 0
} catch {
    Write-Error ('[FAILED] ' + $_.Exception.Message) -ErrorAction Continue
    exit 1
}

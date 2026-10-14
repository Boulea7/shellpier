# Windows PowerShell 2.0 / CLR 2 compatible helpers. No automatic downloads.
function Get-ShellPierHash([string]$Path) {
    $sha = New-Object System.Security.Cryptography.SHA256CryptoServiceProvider
    $stream = $null
    try {
        $stream = [IO.File]::OpenRead($Path)
        return [BitConverter]::ToString($sha.ComputeHash($stream)).Replace('-', '').ToLowerInvariant()
    } finally {
        if ($null -ne $stream) { $stream.Close() }
        $sha.Clear()
    }
}
function Assert-ShellPierBinary([string]$Path, [string]$Hash, [string]$Publisher) {
    if ($Hash -notmatch '^[a-fA-F0-9]{64}$') { throw 'A trusted SHA-256 value is required.' }
    if ((Get-ShellPierHash $Path) -cne $Hash.ToLowerInvariant()) { throw '[HASH_MISMATCH] Binary was not executed.' }
    $signature = Get-AuthenticodeSignature -FilePath $Path -ErrorAction Stop
    if ($signature.Status -ne 'Valid' -or $null -eq $signature.SignerCertificate) {
        throw '[SIGNATURE_TRUST] Check date, Windows updates and certificate trust. Verification is never bypassed.'
    }
    $name = $signature.SignerCertificate.GetNameInfo([Security.Cryptography.X509Certificates.X509NameType]::SimpleName, $false)
    if ($name -cne $Publisher) { throw '[WRONG_PUBLISHER] Binary was not executed.' }
}
function Assert-ShellPierIPv4([string]$Address) {
    if ($Address -notmatch '^([0-9]{1,3}\.){3}[0-9]{1,3}$') { throw 'Expected a dotted IPv4 address.' }
    foreach ($part in $Address.Split('.')) { if ([int]$part -gt 255) { throw 'Invalid IPv4 address.' } }
    $ip = [Net.IPAddress]::Parse($Address)
    if ($ip.AddressFamily -ne [Net.Sockets.AddressFamily]::InterNetwork) { throw 'IPv4 is required.' }
    return $ip.ToString()
}
function Assert-ShellPierPort([int]$Port) {
    if ($Port -lt 1 -or $Port -gt 65535) { throw 'Port must be between 1 and 65535.' }
}
function Assert-ShellPierAdmin {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = New-Object Security.Principal.WindowsPrincipal($identity)
    if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        throw '[ADMIN_REQUIRED] Run CMD as administrator. No automatic elevation or reboot.'
    }
}
function Set-ShellPierDirectoryAcl([string]$Path) {
    $acl = New-Object Security.AccessControl.DirectorySecurity
    $acl.SetAccessRuleProtection($true, $false)
    foreach ($text in @('S-1-5-32-544', 'S-1-5-18')) {
        $sid = New-Object Security.Principal.SecurityIdentifier($text)
        $rule = New-Object Security.AccessControl.FileSystemAccessRule($sid, 'FullControl', 'ContainerInherit, ObjectInherit', 'None', 'Allow')
        $acl.AddAccessRule($rule)
    }
    [IO.Directory]::SetAccessControl($Path, $acl)
}
function Get-ShellPierService([string]$InstanceName) {
    if ($InstanceName -notmatch '^ShellPier-[a-f0-9]{32}$') { throw 'Refusing a non-owned Bitvise instance.' }
    $display = 'Bitvise SSH Server - ' + $InstanceName
    $services = @(Get-WmiObject Win32_Service -Filter ("DisplayName='" + $display + "'"))
    if ($services.Count -gt 1) { throw 'Ambiguous named instance.' }
    if ($services.Count -eq 1) { return $services[0] }
    return $null
}
function Get-ShellPierListener([int]$ProcessId, [int]$Port) {
    $lines = @(& "$env:SystemRoot\System32\netstat.exe" -ano -p tcp)
    if ($LASTEXITCODE -ne 0) { throw 'netstat failed.' }
    foreach ($line in $lines) {
        $parts = $line.Trim() -split '\s+'
        if ($parts.Count -eq 5 -and $parts[0] -eq 'TCP' -and $parts[4] -eq [string]$ProcessId -and
            $parts[1] -match (':' + $Port + '$') -and $parts[2] -match ':0$') { return $parts[1] }
    }
    return $null
}
function Get-ShellPierIdentity {
    $id = [Security.Principal.WindowsIdentity]::GetCurrent()
    $desktop = (Get-WmiObject Win32_ComputerSystem).UserName
    if ([string]::IsNullOrEmpty($desktop)) { $desktop = $id.Name }
    if ($desktop -notmatch '^[^\\]+\\[^\\]+$' -or $desktop -match '^NT (AUTHORITY|SERVICE)\\') {
        throw 'Run from a normal interactive Windows account.'
    }
    $account = New-Object Security.Principal.NTAccount($desktop)
    $sid = $account.Translate([Security.Principal.SecurityIdentifier]).Value
    return New-Object PSObject -Property @{ Account=$desktop; Sid=$sid; Operator=$id.Name; OperatorSid=$id.User.Value }
}
function Get-ShellPierDoctor {
    $os = Get-WmiObject Win32_OperatingSystem
    $id = Get-ShellPierIdentity
    $principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
    $tools = @{}
    foreach ($name in @('ssh.exe','plink.exe','schtasks.exe','netstat.exe','whoami.exe')) {
        $command = Get-Command $name -ErrorAction SilentlyContinue
        if ($null -ne $command) { $tools[$name] = $command.Definition } else { $tools[$name] = $null }
    }
    $addresses = @()
    foreach ($adapter in @(Get-WmiObject Win32_NetworkAdapterConfiguration -Filter 'IPEnabled=True')) {
        foreach ($address in @($adapter.IPAddress)) {
            if ($address -match '^([0-9]{1,3}\.){3}[0-9]{1,3}$') {
                $addresses += New-Object PSObject -Property @{IPv4=$address; Interface=$adapter.Description; Gateway=@($adapter.DefaultIPGateway); Mask=@($adapter.IPSubnet)}
            }
        }
    }
    $netfx = @()
    foreach ($key in @('HKLM:\SOFTWARE\Microsoft\NET Framework Setup\NDP\v3.5','HKLM:\SOFTWARE\Microsoft\NET Framework Setup\NDP\v4\Full')) {
        $value = Get-ItemProperty $key -ErrorAction SilentlyContinue
        if ($null -ne $value) { $netfx += New-Object PSObject -Property @{Key=$key; Version=$value.Version; Release=$value.Release; Install=$value.Install} }
    }
    $firewall = $null
    try {
        $policy = New-Object -ComObject HNetCfg.FwPolicy2
        $firewall = @{}
        foreach ($profile in @(1,2,4)) { $firewall[[string]$profile] = $policy.FirewallEnabled($profile) }
    } catch { $firewall = 'Unavailable: ' + $_.Exception.Message }
    return New-Object PSObject -Property @{ OS=$os.Caption; OSVersion=$os.Version; ServicePack=$os.ServicePackMajorVersion; Architecture=$os.OSArchitecture; PowerShell=[string]$PSVersionTable.PSVersion; CLR=[string][Environment]::Version; NetFramework=$netfx; Identity=$id; Elevated=$principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator); IPv4=$addresses; Tools=$tools; Firewall=$firewall; BitviseServices=@(Get-WmiObject Win32_Service -Filter "DisplayName LIKE 'Bitvise SSH Server%'"); Proxy=@(& "$env:SystemRoot\System32\netsh.exe" winhttp show proxy) }
}

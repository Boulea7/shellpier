# Windows PowerShell 2.0 and .NET Framework 3.5 compatible.
# Official COM reference: https://dl.bitvise.com/BssCfg951.html
# Lock/load/save: https://bitvise.com/ssh-server-guide-scriptable
# Key import: https://bitvise.com/files/scripts9/BssCfgManip-PS-PubKeyImport.txt
# Address rules: https://bitvise.com/files/scripts9/BssCfgManip-PS-ClientAddressRules.txt
function Invoke-ShellPierBitviseConfiguration {
    param(
        [Parameter(Mandatory=$true)][string]$AccountName,
        [Parameter(Mandatory=$true)][string]$InstanceName,
        [int]$Port = 2222,
        [string]$ListenAddress = "0.0.0.0",
        [Parameter(Mandatory=$true)][string]$AllowedClient,
        [Parameter(Mandatory=$true)][string]$PublicKeyPath,
        [Parameter(Mandatory=$true)][string]$BackupDirectory
    )
    $ErrorActionPreference = 'Stop'
    if ($InstanceName -notmatch '^ShellPier-[a-f0-9]{32}$') { throw 'Refusing non-owned or default instance.' }
    if ($AccountName -notmatch '^([^\\]+)\\([^\\]+)$') {
        throw 'Expected a Windows account in DOMAIN\USER or COMPUTER\USER form.'
    }
    $accountDomain = $matches[1]
    $accountUser = $matches[2]
    $parsedAddress = [System.Net.IPAddress]::Parse($AllowedClient)
    if ($parsedAddress.AddressFamily -ne [System.Net.Sockets.AddressFamily]::InterNetwork) {
        throw 'The allowed Client address must be IPv4.'
    }
    $AllowedClient = $parsedAddress.ToString()
    if (-not (Test-Path -LiteralPath $PublicKeyPath -PathType Leaf)) {
        throw 'Client public key file was not found.'
    }
    if (-not (Test-Path -LiteralPath $BackupDirectory -PathType Container)) {
        throw 'The protected backup directory must exist before configuration.'
    }
    $PublicKeyPath = (Resolve-Path -LiteralPath $PublicKeyPath).ProviderPath
    $keyText = [System.IO.File]::ReadAllText($PublicKeyPath).Trim()
    $keyParts = $keyText -split '\s+'
    if ($keyParts.Length -lt 2 -or $keyParts[0] -notmatch '^(ssh-rsa|ssh-ed25519|ecdsa-sha2-nistp(256|384|521))$') {
        throw 'Expected one supported OpenSSH public key.'
    }
    [void][System.Convert]::FromBase64String($keyParts[1])
    $expectedKeyBlob = $keyParts[1]

    function Set-ClientAddressRules($Rules, $Config, [string]$Address) {
        # Replace only the caller-selected address policy, preserving accounts.
        [void]$Rules.Clear()
        [void]$Rules.NewClear()
        $Rules.new.addressRule.addressType = $Config.enums.AddressVer6Type.ipv4
        $Rules.new.addressRule.ipv4range = $false
        $Rules.new.addressRule.ipv4 = $Address
        $Rules.new.instr.allowConnect = $true
        [void]$Rules.NewCommit()
        [void]$Rules.NewClear()
        $Rules.new.addressRule.addressType = $Config.enums.AddressVer6Type.anyIP
        $Rules.new.instr.allowConnect = $false
        [void]$Rules.NewCommit()
    }
    function Assert-ClientAddressRules($Rules, $Config, [string]$Address) {
        if ($Rules.count -ne 2) { throw 'Saved address policy has an unexpected rule count.' }
        $allow = $Rules.GetItem(0)
        $deny = $Rules.GetItem(1)
        if ($allow.addressRule.addressType -ne $Config.enums.AddressVer6Type.ipv4 -or
            $allow.addressRule.ipv4range -or $allow.addressRule.ipv4 -ne $Address -or
            -not $allow.instr.allowConnect -or
            $deny.addressRule.addressType -ne $Config.enums.AddressVer6Type.anyIP -or
            $deny.instr.allowConnect) {
            throw 'Saved address policy does not match the exact Client allow rule.'
        }
    }
    function Find-TargetAccount($Accounts, [int]$Type, [string]$Domain, [string]$User, [int]$DomainType) {
        foreach ($item in $Accounts.entries) {
            if ($item.winAccountType -ne $Type -or $item.winAccount -ine $User) { continue }
            # Disabled COM fields must not be read, even to compare empty values.
            if ($Type -eq $DomainType -and $item.winDomain -ine $Domain) { continue }
            return $item
        }
        return $null
    }
    function Has-ExpectedKey($Keys, $Config, [string]$ExpectedBlob) {
        foreach ($key in $Keys.entries) {
            $exported = $key.ExportToBase64String($Config.enums.PublicKeyFormat.openSsh)
            $parts = $exported.Trim() -split '\s+'
            if ($parts.Length -ge 2 -and $parts[1] -ceq $ExpectedBlob) { return $true }
        }
        return $false
    }

    $cfg = New-Object -ComObject 'Bitvise.BssCfg'
    [void]$cfg.SetInstance($InstanceName)
    $localAccount = ($accountDomain -ieq $env:COMPUTERNAME -or $accountDomain -eq '.')
    if ($localAccount) {
        $accountType = $cfg.enums.WinAccountType.localAccount
        $storedDomain = ''
    } else {
        $accountType = $cfg.enums.WinAccountType.domainAccount
        $storedDomain = $accountDomain
    }
    $locked = $false
    try {
        [void]$cfg.settings.Lock()
        $locked = $true
        [void]$cfg.settings.Load()
        $backupName = 'Bitvise-settings-before-' + [System.Guid]::NewGuid().ToString('N') + '.ps1.txt'
        $backupPath = Join-Path $BackupDirectory $backupName
        [System.IO.File]::WriteAllText($backupPath, $cfg.settings.Dump(), [System.Text.Encoding]::UTF8)
        if ((Get-Item -LiteralPath $backupPath).Length -eq 0) { throw 'Settings backup was empty.' }

        # Global password transport controls override per-account allowance.
        # IAccessControl951 has no publicKeyMethod property; public-key access is
        # controlled by IAccountAuthentication951.publicKeyAuth and imported keys.
        foreach ($method in @('passwordMethod', 'kbdiPasswordMethod')) {
            $currentMethod = $cfg.settings.access.$method
            if ($currentMethod -ne $cfg.enums.GlobalAuthMethod.permit -and
                $currentMethod -ne $cfg.enums.GlobalAuthMethod.permitWinAccounts) {
                $cfg.settings.access.$method = $cfg.enums.GlobalAuthMethod.permitWinAccounts
            }
        }

        # Restrict connections and stop automatic broad SSH firewall exceptions.
        $cfg.settings.windowsFirewall.sshPortsFirewallSetting = $cfg.enums.WindowsFirewallSetting.dontChange
        Set-ClientAddressRules $cfg.settings.access.clientAddresses $cfg $AllowedClient

        # Set the SSH IPv4 listener without removing bindings for other services.
        $bindings = $cfg.settings.bindings.ipv4
        for ($i = [int]$bindings.count - 1; $i -ge 0; $i--) {
            if ($bindings.GetItem($i).serviceType -eq $cfg.enums.ServiceType.ssh) { [void]$bindings.Erase($i) }
        }
        [void]$bindings.NewClear()
        $bindings.new.serviceType = $cfg.enums.ServiceType.ssh
        $bindings.new.listenInterface = $ListenAddress
        $bindings.new.port = $Port
        $bindings.new.gatewayForward = $false
        $bindings.new.proxyProtocol = $cfg.enums.ProxyProtocol.disabled
        $bindings.new.obfuscation = $false
        [void]$bindings.NewCommit()

        $accounts = $cfg.settings.access.winAccounts
        # This function is called only for a newly installed owned instance.
        [void]$accounts.Clear()
        [void]$cfg.settings.access.virtAccounts.Clear()
        $v6 = $cfg.settings.bindings.ipv6
        for ($j = [int]$v6.count - 1; $j -ge 0; $j--) {
            if ($v6.GetItem($j).serviceType -eq $cfg.enums.ServiceType.ssh) { [void]$v6.Erase($j) }
        }
        $account = Find-TargetAccount $accounts $accountType $storedDomain $accountUser $cfg.enums.WinAccountType.domainAccount
        $newAccount = ($null -eq $account)
        if ($newAccount) {
            [void]$accounts.NewClear()
            $account = $accounts.new
            $account.winAccountType = $accountType
            # winDomain is disabled for local accounts, including an empty write.
            if ($accountType -eq $cfg.enums.WinAccountType.domainAccount) {
                $account.winDomain = $storedDomain
            }
            $account.winAccount = $accountUser
        }
        $account.loginAllowed = $cfg.enums.DefaultGroupYesNo.yes
        $account.term.shellAccessType = $cfg.enums.ShellAccess.cmdPrompt
        $account.auth.passwordAuth = $cfg.enums.AuthDisp.allowed
        $account.auth.publicKeyAuth = $cfg.enums.AuthDisp.required
        Set-ClientAddressRules $account.clientAddresses $cfg $AllowedClient
        if (-not (Has-ExpectedKey $account.auth.keys $cfg $expectedKeyBlob)) {
            [void]$account.auth.keys.ImportFromFile($PublicKeyPath)
        }
        if ($newAccount) { [void]$accounts.NewCommit() }
        [void]$cfg.settings.Save()
        # Reload persistent configuration and verify every requested control.
        [void]$cfg.settings.Load()
        foreach ($method in @('passwordMethod', 'kbdiPasswordMethod')) {
            $savedMethod = $cfg.settings.access.$method
            if ($savedMethod -ne $cfg.enums.GlobalAuthMethod.permit -and
                $savedMethod -ne $cfg.enums.GlobalAuthMethod.permitWinAccounts) {
                throw ('Saved global password method does not permit Windows accounts: ' + $method)
            }
        }
        $savedAccount = Find-TargetAccount $cfg.settings.access.winAccounts $accountType $storedDomain $accountUser $cfg.enums.WinAccountType.domainAccount
        if ($null -eq $savedAccount) { throw 'The Windows account did not persist.' }
        if ($savedAccount.loginAllowed -ne $cfg.enums.DefaultGroupYesNo.yes -or
            $savedAccount.term.shellAccessType -ne $cfg.enums.ShellAccess.cmdPrompt -or
            $savedAccount.auth.passwordAuth -ne $cfg.enums.AuthDisp.allowed -or
            $savedAccount.auth.publicKeyAuth -ne $cfg.enums.AuthDisp.required) {
            throw 'Saved Windows account access settings did not match.'
        }
        Assert-ClientAddressRules $cfg.settings.access.clientAddresses $cfg $AllowedClient
        Assert-ClientAddressRules $savedAccount.clientAddresses $cfg $AllowedClient
        if (-not (Has-ExpectedKey $savedAccount.auth.keys $cfg $expectedKeyBlob)) {
            throw 'The Client public key did not persist.'
        }
        if ($cfg.settings.windowsFirewall.sshPortsFirewallSetting -ne $cfg.enums.WindowsFirewallSetting.dontChange) {
            throw 'Automatic SSH firewall management was not disabled.'
        }
        $sshBindings = 0
        foreach ($binding in $cfg.settings.bindings.ipv4.entries) {
            if ($binding.serviceType -eq $cfg.enums.ServiceType.ssh) {
                $sshBindings++
                if ($binding.listenInterface -ne $ListenAddress -or $binding.port -ne $Port -or
                    $binding.gatewayForward -or $binding.obfuscation -or
                    $binding.proxyProtocol -ne $cfg.enums.ProxyProtocol.disabled) {
                    throw 'Saved SSH listener settings did not match.'
                }
            }
        }
        if ($sshBindings -ne 1) { throw 'Expected exactly one IPv4 SSH listener.' }
        Write-Host ('[OK] Bitvise settings saved and read back for ' + $AccountName)
        Write-Host ('[OK] Protected settings backup: ' + $backupPath)
    } finally {
        if ($locked) { [void]$cfg.settings.Unlock() }
        if ($null -ne $cfg -and [System.Runtime.InteropServices.Marshal]::IsComObject($cfg)) {
            [void][System.Runtime.InteropServices.Marshal]::ReleaseComObject($cfg)
        }
    }
}

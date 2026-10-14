$ErrorActionPreference='Stop'
Add-Type @'
using System;
using System.Collections.Generic;
using System.IO;
public class EnumSet { public int localAccount=1,domainAccount=2,ipv4=1,anyIP=0,openSsh=0,permit=1,permitWinAccounts=2,dontChange=1,ssh=1,disabled=0,yes=1,cmdPrompt=3,allowed=2,required=3; }
public class Enums { public EnumSet WinAccountType=new EnumSet(),AddressVer6Type=new EnumSet(),PublicKeyFormat=new EnumSet(),GlobalAuthMethod=new EnumSet(),WindowsFirewallSetting=new EnumSet(),ServiceType=new EnumSet(),ProxyProtocol=new EnumSet(),DefaultGroupYesNo=new EnumSet(),ShellAccess=new EnumSet(),AuthDisp=new EnumSet(); }
public class Address {public int addressType;public bool ipv4range;public string ipv4;}
public class Instr {public bool allowConnect;}
public class Rule {public Address addressRule=new Address();public Instr instr=new Instr();}
public class Binding {public int serviceType,port,proxyProtocol;public string listenInterface;public bool gatewayForward,obfuscation;}
public class Rules {public List<Rule> entries=new List<Rule>();public Rule @new;public int count {get{return entries.Count;}}public void Clear(){entries.Clear();}public void NewClear(){@new=new Rule();}public void NewCommit(){entries.Add(@new);}public Rule GetItem(int i){return entries[i];}}
public class Bindings {public List<Binding> entries=new List<Binding>();public Binding @new;public int count {get{return entries.Count;}}public void Erase(int i){entries.RemoveAt(i);}public void NewClear(){@new=new Binding();}public void NewCommit(){entries.Add(@new);}public Binding GetItem(int i){return entries[i];}}
public class Key {public string value;public string ExportToBase64String(int f){return value;}}
public class Keys {public bool Fail; public int Imports;public List<Key> entries=new List<Key>();public void ImportFromFile(string p){if(Fail)throw new Exception("INJECTED_KEY_IMPORT_FAILURE");Imports++;entries.Add(new Key{value=File.ReadAllText(p).Trim()});}}
public class Auth {public int passwordAuth,publicKeyAuth;public Keys keys=new Keys();}
public class Term {public int shellAccessType;}
public class Account {public int winAccountType=1,loginAllowed;public string winAccount;private string domain;public int DomainReads,DomainWrites;public string winDomain {get{DomainReads++;if(winAccountType==1)throw new Exception("LOCAL_WINDOMAIN_DISABLED");return domain;}set{DomainWrites++;if(winAccountType==1)throw new Exception("LOCAL_WINDOMAIN_DISABLED");domain=value;}}public Auth auth=new Auth();public Term term=new Term();public Rules clientAddresses=new Rules();}
public class Accounts {public bool FailImport;public List<Account> entries=new List<Account>();public Account @new;public int count {get{return entries.Count;}}public void Clear(){entries.Clear();}public void NewClear(){@new=new Account();@new.auth.keys.Fail=FailImport;}public void NewCommit(){entries.Add(@new);}}
public class Access {public int passwordMethod=1,kbdiPasswordMethod=1;public Accounts winAccounts=new Accounts();public Accounts virtAccounts=new Accounts();public Rules clientAddresses=new Rules();}
public class Firewall {public int sshPortsFirewallSetting;}
public class AllBindings {public Bindings ipv4=new Bindings();public Bindings ipv6=new Bindings();}
public class Settings {public bool Locked;public int Saves,Unlocks;public Access access=new Access();public Firewall windowsFirewall=new Firewall();public AllBindings bindings=new AllBindings();public void Lock(){if(Locked)throw new Exception("DOUBLE_LOCK");Locked=true;}public void Unlock(){Locked=false;Unlocks++;}public void Load(){if(!Locked)throw new Exception("LOAD_WITHOUT_LOCK");}public string Dump(){return "$cfg.settings.SetDefaults()";}public void Save(){if(!Locked)throw new Exception("SAVE_WITHOUT_LOCK");Saves++;}}
public class Config {public Enums enums=new Enums();public Settings settings=new Settings();public void SetInstance(string s){if(s!="ShellPier-00000000000000000000000000000001")throw new Exception("WRONG_INSTANCE");}}
'@
function New-Object { param([string]$ComObject); if($ComObject -ne 'Bitvise.BssCfg'){throw 'Unexpected mocked New-Object call'};return $script:fake }
function Assert($condition,$message){if(-not $condition){throw $message}}

$root=Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
$module=Join-Path $root 'bitvise.ps1'
$work=Join-Path ([IO.Path]::GetTempPath()) ('shellpier-test-'+[Guid]::NewGuid().ToString('N'))
[void][IO.Directory]::CreateDirectory($work)
$key=Join-Path $work 'access.pub'
[IO.File]::WriteAllText($key,'ssh-ed25519 AAAA synthetic-test',[Text.Encoding]::ASCII)
$env:COMPUTERNAME='MOCKPC'
. $module
function Run-Config($account){Invoke-ShellPierBitviseConfiguration -AccountName $account -InstanceName 'ShellPier-00000000000000000000000000000001' -AllowedClient '192.0.2.10' -PublicKeyPath $key -BackupDirectory $work -Port 2222}
try {
 $script:fake=[Activator]::CreateInstance([Config]);Run-Config 'MOCKPC\testuser'
 $local=$fake.settings.access.winAccounts.entries[0]
 Assert ($local.DomainReads -eq 0 -and $local.DomainWrites -eq 0) 'Local account touched disabled winDomain'
 Assert ($local.auth.publicKeyAuth -eq 3) 'Public key is not required'
 Assert ($fake.settings.bindings.ipv4.GetItem(0).port -eq 2222) 'Wrong listener port'
 Assert ($fake.settings.access.clientAddresses.count -eq 2 -and -not $fake.settings.access.clientAddresses.GetItem(1).instr.allowConnect) 'Source policy not restrictive'
 Assert ($fake.settings.Saves -eq 1 -and -not $fake.settings.Locked) 'Save/Unlock failed'
 Write-Host 'PASS local account: no winDomain access, public key required, source policy, chosen port, save/unlock'
 $script:fake=[Activator]::CreateInstance([Config]);Run-Config 'EXAMPLEDOMAIN\testuser'
 $domain=$fake.settings.access.winAccounts.entries[0]
 Assert ($domain.winDomain -eq 'EXAMPLEDOMAIN' -and $domain.DomainWrites -eq 1) 'Domain configuration failed'
 Write-Host 'PASS domain account: exact winDomain set and read back'
 $script:fake=[Activator]::CreateInstance([Config]);$fake.settings.access.winAccounts.FailImport=$true;$failure=$null
 try { Run-Config 'MOCKPC\testuser' } catch { $failure=$_.Exception.Message }
 Assert ($failure -like '*INJECTED_KEY_IMPORT_FAILURE*') 'Import failure swallowed'
 Assert ($fake.settings.Saves -eq 0 -and -not $fake.settings.Locked -and $fake.settings.Unlocks -eq 1) 'Failure saved or retained lock'
 Write-Host 'PASS key failure: propagated, no Save, lock released'
 $failure=$null
 try { Invoke-ShellPierBitviseConfiguration -AccountName 'MOCKPC\testuser' -InstanceName 'Bitvise SSH Server' -AllowedClient '192.0.2.10' -PublicKeyPath $key -BackupDirectory $work } catch { $failure=$_.Exception.Message }
 Assert ($failure -like '*non-owned*') 'Default instance was not rejected'
 Write-Host 'PASS default instance rejected'
} finally { Remove-Item -LiteralPath $work -Recurse -Force }

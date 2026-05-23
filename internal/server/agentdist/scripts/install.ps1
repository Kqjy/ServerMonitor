[CmdletBinding()]
param(
    [int]$IntervalS = 0,
    [switch]$Insecure,
    [switch]$PersistInsecure,
    [switch]$AdminService
)

$Token = $env:SM_TOKEN

if ($IntervalS -le 0) {
    if ($env:SM_INTERVAL) { $IntervalS = [int]$env:SM_INTERVAL } else { $IntervalS = 10 }
}
if ($env:SM_ADMIN_SERVICE -eq '1') { $AdminService = $true }

$ErrorActionPreference = 'Stop'
$ServerUrl = '__SERVER_URL__'

if (-not $Token) {
    $sec = Read-Host -Prompt 'Agent token' -AsSecureString
    $Token = [System.Net.NetworkCredential]::new('', $sec).Password
}
if (-not $Token) { throw 'agent token required: set $env:SM_TOKEN or enter at the prompt' }
if (-not ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'run from an elevated PowerShell (Run as Administrator)'
}

if ($ServerUrl -notmatch '^https://') {
    if (-not $Insecure) {
        throw "refusing non-https:// server URL '$ServerUrl' (agent token would leak); pass -Insecure to override (debug only)"
    }
}

$arch = if ([Environment]::Is64BitOperatingSystem) { 'amd64' } else { throw 'unsupported arch (32-bit Windows is not supported)' }
$platform = "windows-$arch"

function Get-VirtualServiceSid {
    param([Parameter(Mandatory=$true)][string]$Name)
    $out = & sc.exe showsid $Name 2>&1 | Out-String
    if ($out -match '(S-1-5-80-[0-9-]+)') {
        return New-Object Security.Principal.SecurityIdentifier $Matches[1]
    }
    throw ("could not parse virtual service SID for '{0}' from sc.exe showsid output:`n{1}" -f $Name, $out)
}

if ($AdminService) {
    $svcAccount = 'LocalSystem'
    $svcSid     = $null
} else {
    $svcAccount = 'NT SERVICE\sm-agent'
    $svcSid     = Get-VirtualServiceSid -Name 'sm-agent'
}

function Lock-Acl {
    param(
        [Parameter(Mandatory=$true)][string]$Path,
        [ValidateSet('None','Read','Modify')][string]$ServiceAccess = 'None'
    )
    $item = Get-Item -Force -LiteralPath $Path
    $isContainer = $item.PSIsContainer
    $acl = Get-Acl -Path $Path
    $acl.SetAccessRuleProtection($true, $false)
    foreach ($rule in @($acl.Access)) { [void]$acl.RemoveAccessRule($rule) }
    $sysSid   = New-Object Security.Principal.SecurityIdentifier 'S-1-5-18'
    $adminSid = New-Object Security.Principal.SecurityIdentifier 'S-1-5-32-544'
    $full     = [Security.AccessControl.FileSystemRights]::FullControl
    $allow    = [Security.AccessControl.AccessControlType]::Allow
    $prop     = [Security.AccessControl.PropagationFlags]::None
    if ($isContainer) {
        $inherit = [Security.AccessControl.InheritanceFlags] 'ContainerInherit,ObjectInherit'
    } else {
        $inherit = [Security.AccessControl.InheritanceFlags]::None
    }
    $acl.AddAccessRule((New-Object Security.AccessControl.FileSystemAccessRule($sysSid,   $full, $inherit, $prop, $allow)))
    $acl.AddAccessRule((New-Object Security.AccessControl.FileSystemAccessRule($adminSid, $full, $inherit, $prop, $allow)))
    if ($svcSid -and $ServiceAccess -ne 'None') {
        $svcRights = if ($ServiceAccess -eq 'Modify') {
            [Security.AccessControl.FileSystemRights]'Modify'
        } else {
            [Security.AccessControl.FileSystemRights]'ReadAndExecute'
        }
        $acl.AddAccessRule((New-Object Security.AccessControl.FileSystemAccessRule($svcSid, $svcRights, $inherit, $prop, $allow)))
    }
    $acl.SetOwner($adminSid)
    Set-Acl -Path $Path -AclObject $acl
}

$installDir = Join-Path $env:ProgramFiles 'ServerMonitor'
$configDir  = Join-Path $env:ProgramData 'ServerMonitor'
if (-not (Test-Path $installDir)) { New-Item -ItemType Directory -Path $installDir | Out-Null }
if (-not (Test-Path $configDir))  { New-Item -ItemType Directory -Path $configDir  | Out-Null }
Lock-Acl -Path $installDir -ServiceAccess Read
Lock-Acl -Path $configDir  -ServiceAccess Modify

$exe = Join-Path $installDir 'sm-agent.exe'
$cfgPath = Join-Path $configDir 'agent.toml'
$spoolPath = Join-Path $configDir 'spool.db'

if ($Insecure) {
    [System.Net.ServicePointManager]::ServerCertificateValidationCallback = { $true }
}

Write-Host "downloading $ServerUrl agent for $platform ..."
Invoke-WebRequest -UseBasicParsing -Uri "$ServerUrl/api/v1/agent/binary?platform=$platform" -Headers @{ 'X-Agent-Token' = $Token } -OutFile $exe
Lock-Acl -Path $exe -ServiceAccess Read

$insecureLine = if ($Insecure -and $PersistInsecure) { 'true' } else { 'false' }
$cfg = @"
server_url = "$ServerUrl"
token      = "$Token"
interval_s = $IntervalS
spool_path = "$($spoolPath -replace '\\', '\\')"
insecure_skip_verify = $insecureLine
"@

$tmpCfg = [System.IO.Path]::Combine($configDir, [System.IO.Path]::GetRandomFileName())
$utf8NoBom = New-Object System.Text.UTF8Encoding($false)
[System.IO.File]::WriteAllText($tmpCfg, $cfg, $utf8NoBom)
Lock-Acl -Path $tmpCfg -ServiceAccess Modify
Move-Item -Force -LiteralPath $tmpCfg -Destination $cfgPath
Lock-Acl -Path $cfgPath -ServiceAccess Modify

if (-not $AdminService) {
    foreach ($g in @('docker-users','Performance Monitor Users')) {
        $grp = Get-LocalGroup -Name $g -ErrorAction SilentlyContinue
        if ($grp) {
            try { Add-LocalGroupMember -Group $g -Member $svcSid -ErrorAction Stop } catch {
                if ($_.Exception.Message -notmatch 'already a member') { Write-Warning ("could not add {0} to {1}: {2}" -f $svcAccount, $g, $_.Exception.Message) }
            }
        }
    }
}

$existing = Get-Service -Name 'sm-agent' -ErrorAction SilentlyContinue
if ($existing) {
    Stop-Service -Name 'sm-agent' -Force -ErrorAction SilentlyContinue
    & sc.exe delete sm-agent | Out-Null
    Start-Sleep -Seconds 1
}

$binPath = "`"$exe`" --config `"$cfgPath`""
& sc.exe create sm-agent binPath= $binPath start= auto obj= $svcAccount DisplayName= 'ServerMonitor Agent' | Out-Null
& sc.exe description sm-agent 'Reports system metrics to ServerMonitor' | Out-Null
& sc.exe failure sm-agent reset= 86400 actions= restart/5000/restart/5000/restart/5000 | Out-Null

if ($AdminService) {
    $keepPrivs = @(
        'SeChangeNotifyPrivilege',
        'SeImpersonatePrivilege',
        'SeAssignPrimaryTokenPrivilege',
        'SeCreateGlobalPrivilege',
        'SeIncreaseWorkingSetPrivilege',
        'SeDebugPrivilege',
        'SeManageVolumePrivilege',
        'SeProfileSingleProcessPrivilege',
        'SeSystemProfilePrivilege',
        'SeIncreaseBasePriorityPrivilege'
    ) -join '/'
} else {
    $keepPrivs = @(
        'SeChangeNotifyPrivilege',
        'SeImpersonatePrivilege',
        'SeCreateGlobalPrivilege',
        'SeIncreaseWorkingSetPrivilege'
    ) -join '/'
}
& sc.exe privs sm-agent $keepPrivs | Out-Null
& sc.exe sidtype sm-agent unrestricted | Out-Null

Start-Service -Name 'sm-agent'

Write-Host ''
Write-Host "installed. agent is running as $svcAccount."
Write-Host "service: Get-Service sm-agent"
Write-Host "logs:    Get-WinEvent -LogName Application -ProviderName 'sm-agent'"
if (-not $AdminService) {
    Write-Host ''
    Write-Host 'note: SMART and full-process collectors require admin. Pass -AdminService or set $env:SM_ADMIN_SERVICE=1 to install as LocalSystem if you need them.'
}

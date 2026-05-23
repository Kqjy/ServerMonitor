[CmdletBinding()]
param(
    [Parameter(Mandatory=$true)][string]$ServerUrl,
    [string]$AdminTokenFile,
    [string]$HostnameOverride,
    [int]$IntervalS = 10,
    [Parameter(Mandatory=$true)][string]$BinaryPath,
    [switch]$AdminService
)

$ErrorActionPreference = 'Stop'

function Assert-Elevated {
    $id = [Security.Principal.WindowsIdentity]::GetCurrent()
    $p  = New-Object Security.Principal.WindowsPrincipal($id)
    if (-not $p.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        throw "must run elevated (Administrator)"
    }
}
Assert-Elevated

if ($env:SM_ADMIN_SERVICE -eq '1') { $AdminService = $true }

$AdminToken = $env:SM_ADMIN_TOKEN
if (-not $AdminToken -and $AdminTokenFile) {
    if (-not (Test-Path -Path $AdminTokenFile)) { throw "admin-token-file not found: $AdminTokenFile" }
    $AdminToken = (Get-Content -Raw -Path $AdminTokenFile).Trim()
}
if (-not $AdminToken) {
    throw "admin token required: set `$env:SM_ADMIN_TOKEN or pass -AdminTokenFile PATH"
}

if ($ServerUrl -notmatch '^https://') {
    throw "server URL must start with https:// (admin token would leak over http)"
}
if (-not (Test-Path -Path $BinaryPath)) { throw "binary not found: $BinaryPath" }

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
Copy-Item -Force -Path $BinaryPath -Destination $exe
Lock-Acl -Path $exe -ServiceAccess Read

$cfgPath = Join-Path $configDir 'agent.toml'

$prevToken = $env:SM_ADMIN_TOKEN
$env:SM_ADMIN_TOKEN = $AdminToken
try {
    $regArgs = @('register', '--server', $ServerUrl, '--interval', $IntervalS, '--config', $cfgPath)
    if ($HostnameOverride) { $regArgs += @('--hostname', $HostnameOverride) }
    & $exe @regArgs
    if ($LASTEXITCODE -ne 0) { throw "register failed" }
} finally {
    $env:SM_ADMIN_TOKEN = $prevToken
    $AdminToken = $null
    [System.GC]::Collect()
}

if (Test-Path -Path $cfgPath) { Lock-Acl -Path $cfgPath -ServiceAccess Modify }

$spoolPath = Join-Path $configDir 'spool.db'
if (Test-Path -Path $spoolPath) { Lock-Acl -Path $spoolPath -ServiceAccess Modify }

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

$svc = Get-Service -Name 'sm-agent' -ErrorAction SilentlyContinue
if ($svc) {
    Stop-Service -Name 'sm-agent' -Force -ErrorAction SilentlyContinue
    & sc.exe delete sm-agent | Out-Null
    Start-Sleep -Seconds 1
}

$bin = "`"$exe`" --config `"$cfgPath`""
& sc.exe create sm-agent binPath= $bin start= auto obj= $svcAccount DisplayName= 'ServerMonitor Agent' | Out-Null
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
Get-Service -Name 'sm-agent'
"installed. service runs as $svcAccount. config: $cfgPath"
if (-not $AdminService) {
    "note: SMART and full-process collectors require admin; pass -AdminService (or SM_ADMIN_SERVICE=1) to install as LocalSystem if you need them."
}

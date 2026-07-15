[CmdletBinding()]
param(
    [switch]$KeepData
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

$syncTask = Get-ScheduledTask -TaskName 'ServerMonitor Privileged Agent Sync' -ErrorAction SilentlyContinue
if ($syncTask) {
    Unregister-ScheduledTask -TaskName 'ServerMonitor Privileged Agent Sync' -Confirm:$false
}

function Get-VirtualServiceSid {
    param([Parameter(Mandatory=$true)][string]$Name)
    $out = & sc.exe showsid $Name 2>&1 | Out-String
    if ($out -match '(S-1-5-80-[0-9-]+)') {
        return New-Object Security.Principal.SecurityIdentifier $Matches[1]
    }
    return $null
}

function Remove-Tree {
    param([Parameter(Mandatory=$true)][string]$Path)
    if (-not (Test-Path -Path $Path)) { return }
    try {
        Remove-Item -Recurse -Force -Path $Path -ErrorAction Stop
    } catch {
        Write-Warning ("could not fully remove {0}: {1}; close any process using it and re-run" -f $Path, $_.Exception.Message)
    }
}

$svcSid = Get-VirtualServiceSid -Name 'sm-agent'
if ($svcSid) {
    foreach ($g in @('docker-users','Performance Monitor Users')) {
        $grp = Get-LocalGroup -Name $g -ErrorAction SilentlyContinue
        if ($grp) {
            try { Remove-LocalGroupMember -Group $g -Member $svcSid -ErrorAction Stop } catch {
                if ($_.Exception.Message -notmatch 'was not found|not a member|No mapping') {
                    Write-Warning ("could not remove sm-agent from {0}: {1}" -f $g, $_.Exception.Message)
                }
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

$installDir = Join-Path $env:ProgramFiles 'ServerMonitor'
$configDir  = Join-Path $env:ProgramData 'ServerMonitor'

Remove-Tree -Path $installDir

Remove-Item -Force -Path (Join-Path $configDir 'deregistered') -ErrorAction SilentlyContinue

if ($KeepData) {
    "keeping $configDir (agent.toml identity + spool.db)"
} else {
    Remove-Tree -Path $configDir
}

if (Get-Service -Name 'sm-agent' -ErrorAction SilentlyContinue) {
    "service still present; close services.msc and re-run, or reboot to finish removal."
} else {
    "uninstalled. service removed."
}
if ($KeepData) {
    "config + spool retained; reinstall with install-agent-windows.ps1 to reuse this host identity"
}
"note: this does not deregister the host on the server. Use Settings -> Remove in the web UI to drop its stored history."

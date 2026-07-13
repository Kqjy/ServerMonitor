[CmdletBinding()]
param(
    [int]$IntervalS = 0,
    [switch]$Insecure,
    [switch]$PersistInsecure,
    [switch]$AdminService,
    [switch]$EnableSmart,
    [switch]$EnableBackup,
    [string]$BackupRepos,
    [string]$BackupRepoNames,
    [string]$BackupPaths,
    [string]$BackupTime = '02:30',
    [string]$BackupPruneMode = 'host',
    [string]$BackupS3Region,
    [switch]$BackupS3PathStyle,
    [switch]$Reinstall
)

$Token = $env:SM_TOKEN

if ($IntervalS -le 0) {
    if ($env:SM_INTERVAL) { $IntervalS = [int]$env:SM_INTERVAL } else { $IntervalS = 10 }
}
if ($env:SM_ADMIN_SERVICE -eq '1') { $AdminService = $true }
if ($env:SM_ENABLE_SMART -eq '1')  { $EnableSmart  = $true }
if ($env:SM_ENABLE_BACKUP -eq '1') { $EnableBackup = $true }
if (-not $BackupRepos     -and $env:SM_BACKUP_REPOS)      { $BackupRepos     = $env:SM_BACKUP_REPOS }
if (-not $BackupRepoNames -and $env:SM_BACKUP_REPO_NAMES) { $BackupRepoNames = $env:SM_BACKUP_REPO_NAMES }
if (-not $BackupPaths     -and $env:SM_BACKUP_PATHS)      { $BackupPaths     = $env:SM_BACKUP_PATHS }
if ($env:SM_BACKUP_TIME) { $BackupTime = $env:SM_BACKUP_TIME }
if ($env:SM_BACKUP_PRUNE_MODE) { $BackupPruneMode = $env:SM_BACKUP_PRUNE_MODE }
if (-not $BackupS3Region -and $env:SM_BACKUP_S3_REGION) { $BackupS3Region = $env:SM_BACKUP_S3_REGION }
if ($env:SM_BACKUP_S3_PATH_STYLE -eq '1') { $BackupS3PathStyle = $true }

$ErrorActionPreference = 'Stop'
$ServerUrl = '__SERVER_URL__'
$CanonicalInstallerSha256 = '56d6d3922a5c9ea3a6eab783fe38dde4bdc38e170043a2c75a69fcae548d18f5'
$ResticVersion = '0.19.0'

if (-not ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'run from an elevated PowerShell (Run as Administrator)'
}

if ($env:SM_REINSTALL -eq '1') { $Reinstall = $true }
$cfgPath = Join-Path (Join-Path $env:ProgramData 'ServerMonitor') 'agent.toml'
$Reconfigure = (-not $Reinstall) -and (Test-Path -LiteralPath $cfgPath)
if ($Reconfigure) {
    Write-Host ''
    Write-Host 'reconfiguring the existing sm-agent install in place: re-applying the service'
    Write-Host 'account, privileges, and group memberships, keeping the current identity and'
    Write-Host 'binary. No re-download and no token needed. Set $env:SM_REINSTALL=1 to force a'
    Write-Host 'full fresh install instead.'
    Write-Host ''
}

if (-not $Reconfigure) {
    if (-not $Token) {
        $sec = Read-Host -Prompt 'Agent token' -AsSecureString
        $Token = [System.Net.NetworkCredential]::new('', $sec).Password
    }
    if (-not $Token) { throw 'agent token required: set $env:SM_TOKEN or enter at the prompt' }
}

if (-not $Reconfigure) {
    if ($ServerUrl -notmatch '^https://') {
        if (-not $Insecure) {
            throw "refusing non-https:// server URL '$ServerUrl' (agent token would leak); pass -Insecure to override (debug only)"
        }
    }

    $arch = if ([Environment]::Is64BitOperatingSystem) { 'amd64' } else { throw 'unsupported arch (32-bit Windows is not supported)' }
    $platform = "windows-$arch"
}

function Install-Smartmontools {
    $candidates = @(
        (Join-Path $env:ProgramFiles 'smartmontools\bin\smartctl.exe'),
        (Join-Path ${env:ProgramFiles(x86)} 'smartmontools\bin\smartctl.exe')
    )
    foreach ($p in $candidates) {
        if ($p -and (Test-Path -LiteralPath $p)) {
            Write-Host "smartmontools already present: $p"
            return
        }
    }
    $winget = Get-Command winget.exe -ErrorAction SilentlyContinue
    if (-not $winget) {
        Write-Warning 'winget not available; install smartmontools manually for SMART support: https://www.smartmontools.org/wiki/Download'
        return
    }
    Write-Host 'installing smartmontools via winget (required by SMART collector) ...'
    & winget.exe install --id smartmontools.smartmontools --silent --accept-source-agreements --accept-package-agreements --scope machine | Out-Null
    if ($LASTEXITCODE -ne 0) {
        Write-Warning ("winget install smartmontools.smartmontools exited with {0}; install manually for SMART support" -f $LASTEXITCODE)
        return
    }
    foreach ($p in $candidates) {
        if ($p -and (Test-Path -LiteralPath $p)) {
            Write-Host "smartmontools installed: $p"
            return
        }
    }
    Write-Warning 'winget reported success but smartctl.exe was not found in the usual paths; install manually for SMART support'
}

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

function Escape-Toml {
    param([string]$Value)
    return ($Value -replace '\\', '\\') -replace '"', '\"'
}

function Write-BackupWarning {
    Write-Host ''
    Write-Host 'WARNING: -EnableBackup registers a scheduled task that runs as SYSTEM and reads'
    Write-Host '         every file on the host to back it up. It runs separately from the'
    Write-Host '         resident agent service and is a deliberate opt-in. Enable it only'
    Write-Host '         where whole-host backup read access is worth that exposure.'
    Write-Host ''
}

function Install-Restic {
    param([Parameter(Mandatory=$true)][string]$InstallDir)
    $resticExe = Join-Path $InstallDir 'restic.exe'
    $zipSha    = '6fa4219a70b1b5d1c429bb106a7f97f3d2a5aab74494db2e490b625edc486d8f'
    $entry     = "restic_${ResticVersion}_windows_amd64.exe"
    if (Test-Path -LiteralPath $resticExe) {
        $ver = & $resticExe version 2>$null
        if ($ver -match ("restic {0} " -f [regex]::Escape($ResticVersion))) {
            Write-Host "restic $ResticVersion already present: $resticExe"
            return
        }
    }
    $url = "https://github.com/restic/restic/releases/download/v$ResticVersion/restic_${ResticVersion}_windows_amd64.zip"
    $tmp = Join-Path $env:TEMP ('restic-' + [System.IO.Path]::GetRandomFileName())
    New-Item -ItemType Directory -Path $tmp | Out-Null
    try {
        $zip = Join-Path $tmp 'restic.zip'
        Write-Host "downloading restic $ResticVersion ..."
        Invoke-WebRequest -UseBasicParsing -Uri $url -OutFile $zip
        $got = (Get-FileHash -Algorithm SHA256 -LiteralPath $zip).Hash
        if ($got -ne $zipSha.ToUpper()) {
            throw "restic checksum mismatch (got $got, want $zipSha); refusing to install"
        }
        Expand-Archive -LiteralPath $zip -DestinationPath $tmp -Force
        $extracted = Join-Path $tmp $entry
        if (-not (Test-Path -LiteralPath $extracted)) { throw "restic zip did not contain $entry" }
        Copy-Item -Force -LiteralPath $extracted -Destination $resticExe
    } finally {
        Remove-Item -Recurse -Force -LiteralPath $tmp -ErrorAction SilentlyContinue
    }
    Write-Host "restic $ResticVersion installed: $resticExe"
}

function New-BackupKey {
    param([Parameter(Mandatory=$true)][string]$KeyPath)
    if (Test-Path -LiteralPath $KeyPath) {
        Write-Host "note: existing $KeyPath kept (guards existing snapshots; never regenerated)"
        return $false
    }
    $bytes = New-Object byte[] 32
    $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
    try { $rng.GetBytes($bytes) } finally { $rng.Dispose() }
    $b64 = [Convert]::ToBase64String($bytes)
    $utf8NoBom = New-Object System.Text.UTF8Encoding($false)
    [System.IO.File]::WriteAllText($KeyPath, $b64, $utf8NoBom)
    Lock-Acl -Path $KeyPath -ServiceAccess None
    return $true
}

function New-BackupEnvFile {
    param([Parameter(Mandatory=$true)][string]$EnvPath)
    $lines = @()
    if ($env:SM_BACKUP_S3_ACCESS_KEY_ID)     { $lines += "AWS_ACCESS_KEY_ID=$($env:SM_BACKUP_S3_ACCESS_KEY_ID)" }
    if ($env:SM_BACKUP_S3_SECRET_ACCESS_KEY) { $lines += "AWS_SECRET_ACCESS_KEY=$($env:SM_BACKUP_S3_SECRET_ACCESS_KEY)" }
    if ($env:SM_BACKUP_S3_SESSION_TOKEN)     { $lines += "AWS_SESSION_TOKEN=$($env:SM_BACKUP_S3_SESSION_TOKEN)" }
    if ($env:SM_BACKUP_REST_USERNAME)        { $lines += "RESTIC_REST_USERNAME=$($env:SM_BACKUP_REST_USERNAME)" }
    if ($env:SM_BACKUP_REST_PASSWORD)        { $lines += "RESTIC_REST_PASSWORD=$($env:SM_BACKUP_REST_PASSWORD)" }
    if (-not $lines) {
        if (Test-Path -LiteralPath $EnvPath) {
            Lock-Acl -Path $EnvPath -ServiceAccess None
            return $true
        }
        return $false
    }
    $utf8NoBom = New-Object System.Text.UTF8Encoding($false)
    [System.IO.File]::WriteAllText($EnvPath, ($lines -join "`n") + "`n", $utf8NoBom)
    Lock-Acl -Path $EnvPath -ServiceAccess None
    return $true
}

function New-TunnelSectionSnapshot {
    param([Parameter(Mandatory=$true)][string]$TomlPath)
    if (-not (Test-Path -LiteralPath $TomlPath)) { return '' }
    $captured = @()
    $inside = $false
    foreach ($line in [System.IO.File]::ReadAllLines($TomlPath)) {
        if (-not $inside) {
            if ($line -match '^\s*\[tunnel\]\s*$') {
                $inside = $true
                $captured += $line
            }
            continue
        }
        if ($line -match '^\s*\[') { break }
        $captured += $line
    }
    return ($captured -join "`r`n").TrimEnd()
}

function Write-BackupToml {
    param(
        [Parameter(Mandatory=$true)][string]$TomlPath,
        [Parameter(Mandatory=$true)][string]$StatusPath,
        [Parameter(Mandatory=$true)][string]$KeyPath,
        [Parameter(Mandatory=$true)][string]$ResticPath,
        [string[]]$Repos,
        [string[]]$Names,
        [string[]]$Paths,
        [string]$EnvFilePath,
        [string]$TunnelSection,
        [string]$PruneMode = 'host',
        [string]$S3Region,
        [switch]$S3PathStyle
    )
    $sb = New-Object System.Text.StringBuilder
    [void]$sb.AppendLine("status_path = `"$(Escape-Toml $StatusPath)`"")
    [void]$sb.AppendLine("restic_path = `"$(Escape-Toml $ResticPath)`"")
    $pathItems = ($Paths | ForEach-Object { '"' + (Escape-Toml $_) + '"' }) -join ', '
    [void]$sb.AppendLine("paths = [$pathItems]")
    [void]$sb.AppendLine('excludes = []')
    [void]$sb.AppendLine('one_file_system = false')
    [void]$sb.AppendLine("prune_mode = `"$(Escape-Toml $PruneMode)`"")
    [void]$sb.AppendLine('')
    [void]$sb.AppendLine('[retention]')
    [void]$sb.AppendLine('daily = 7')
    [void]$sb.AppendLine('weekly = 4')
    [void]$sb.AppendLine('monthly = 6')
    for ($i = 0; $i -lt $Repos.Count; $i++) {
        $u = $Repos[$i]
        $tunnelName = if ($u -match '^tunnel:(.*)$') { $Matches[1] } else { '' }
        $tunnelNode = ''
        if ($tunnelName -and $tunnelName.Contains('/')) {
            $tunnelNode = $tunnelName.Substring(0, $tunnelName.IndexOf('/'))
            $tunnelName = $tunnelName.Substring($tunnelName.IndexOf('/') + 1)
        }
        $name = if ($i -lt $Names.Count -and $Names[$i]) {
            $Names[$i]
        } elseif ($tunnelName) {
            $tunnelName
        } else {
            "repo$($i + 1)"
        }
        [void]$sb.AppendLine('')
        [void]$sb.AppendLine('[[repo]]')
        [void]$sb.AppendLine("name = `"$(Escape-Toml $name)`"")
        if ($tunnelName) {
            [void]$sb.AppendLine("tunnel_name = `"$(Escape-Toml $tunnelName)`"")
            if ($tunnelNode) {
                [void]$sb.AppendLine("tunnel_node = `"$(Escape-Toml $tunnelNode)`"")
            }
        } else {
            [void]$sb.AppendLine("url = `"$(Escape-Toml $u)`"")
        }
        [void]$sb.AppendLine("password_file = `"$(Escape-Toml $KeyPath)`"")
        if ($u -match '^(s3|b2|rest|tunnel):') {
            if ($EnvFilePath) {
                [void]$sb.AppendLine("env_file = `"$(Escape-Toml $EnvFilePath)`"")
            } elseif ($tunnelName) {
                Write-Warning "repo '$name' targets tunnel: but no REST credentials were provided (set SM_BACKUP_REST_USERNAME/SM_BACKUP_REST_PASSWORD); authentication will fail"
            } else {
                Write-Warning "repo '$name' targets $($u.Split(':')[0]): but no S3/REST credentials were provided (set SM_BACKUP_S3_* or SM_BACKUP_REST_*); authentication will fail"
            }
        }
        if ($u -match '^s3:') {
            if ($S3Region)   { [void]$sb.AppendLine("s3_region = `"$(Escape-Toml $S3Region)`"") }
            if ($S3PathStyle) { [void]$sb.AppendLine('s3_path_style = true') }
        }
    }
    if ($TunnelSection) {
        [void]$sb.AppendLine('')
        [void]$sb.AppendLine($TunnelSection.TrimEnd())
    }
    $utf8NoBom = New-Object System.Text.UTF8Encoding($false)
    [System.IO.File]::WriteAllText($TomlPath, $sb.ToString(), $utf8NoBom)
    Lock-Acl -Path $TomlPath -ServiceAccess None
}

function Register-BackupTask {
    param(
        [Parameter(Mandatory=$true)][string]$AgentExe,
        [Parameter(Mandatory=$true)][string]$ConfigPath,
        [Parameter(Mandatory=$true)][string]$Time
    )
    if ($Time -notmatch '^([01]?\d|2[0-3]):[0-5]\d$') { throw "backup time must be HH:MM (got: $Time)" }
    $parts = $Time.Split(':')
    $at = [datetime]::Today.AddHours([int]$parts[0]).AddMinutes([int]$parts[1])
    $action    = New-ScheduledTaskAction -Execute $AgentExe -Argument ("backup run --config `"{0}`"" -f $ConfigPath)
    $trigger   = New-ScheduledTaskTrigger -Daily -At $at
    $trigger.RandomDelay = 'PT15M'
    $principal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest
    $settings  = New-ScheduledTaskSettingsSet -StartWhenAvailable -MultipleInstances IgnoreNew
    Register-ScheduledTask -TaskName 'ServerMonitor Backup' -Action $action -Trigger $trigger -Principal $principal -Settings $settings -Force | Out-Null
}

function Register-BackupCheckTask {
    param(
        [Parameter(Mandatory=$true)][string]$AgentExe,
        [Parameter(Mandatory=$true)][string]$ConfigPath,
        [Parameter(Mandatory=$true)][string]$Time
    )
    if ($Time -notmatch '^([01]?\d|2[0-3]):[0-5]\d$') { throw "backup time must be HH:MM (got: $Time)" }
    $parts = $Time.Split(':')
    $at = [datetime]::Today.AddHours([int]$parts[0]).AddMinutes([int]$parts[1])
    $action    = New-ScheduledTaskAction -Execute $AgentExe -Argument ("backup check --config `"{0}`" --read-data-subset 5%" -f $ConfigPath)
    $trigger   = New-ScheduledTaskTrigger -Weekly -DaysOfWeek Sunday -At $at
    $trigger.RandomDelay = 'PT6H'
    $principal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest
    $settings  = New-ScheduledTaskSettingsSet -StartWhenAvailable -MultipleInstances IgnoreNew
    Register-ScheduledTask -TaskName 'ServerMonitor Backup Check' -Action $action -Trigger $trigger -Principal $principal -Settings $settings -Force | Out-Null
}

function Write-RecoveryKit {
    param(
        [Parameter(Mandatory=$true)][string]$KitPath,
        [Parameter(Mandatory=$true)][string]$KeyPath,
        [Parameter(Mandatory=$true)][string]$TomlPath,
        [Parameter(Mandatory=$true)][string]$EnvFilePath,
        [Parameter(Mandatory=$true)][string]$TunnelKeyPath,
        [string[]]$Repos,
        [string[]]$Names
    )
    $pw = (Get-Content -Raw -LiteralPath $KeyPath).Trim()
    $tunnelSection = New-TunnelSectionSnapshot -TomlPath $TomlPath
    $tunnelValues = @{}
    if ($tunnelSection) {
        foreach ($line in ($tunnelSection -split '\r?\n')) {
            if ($line -match '^\s*(endpoint|server_public_key|local_ip|server_ip|rest_port)\s*=\s*"([^"]*)"\s*$') {
                $tunnelValues[$Matches[1]] = $Matches[2]
            } elseif ($line -match '^\s*(endpoint|server_public_key|local_ip|server_ip|rest_port)\s*=\s*(\S+)\s*$') {
                $tunnelValues[$Matches[1]] = $Matches[2]
            }
        }
    }
    $lines = @()
    $lines += 'ServerMonitor backup recovery kit'
    $lines += "host: $env:COMPUTERNAME"
    $lines += "date: $((Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ'))"
    $lines += ''
    $lines += 'repositories:'
    for ($i = 0; $i -lt $Repos.Count; $i++) {
        $u = $Repos[$i]
        $tunnelName = if ($u -match '^tunnel:(.*)$') { $Matches[1] } else { '' }
        $name = if ($i -lt $Names.Count -and $Names[$i]) {
            $Names[$i]
        } elseif ($tunnelName) {
            $tunnelName
        } else {
            "repo$($i + 1)"
        }
        if ($tunnelName) {
            $lines += "  [$name] tunnel:$tunnelName via $($tunnelValues['endpoint'])"
        } else {
            $lines += "  [$name] $u"
        }
    }
    $lines += ''
    $lines += 'repository password:'
    $lines += "  $pw"
    if (Test-Path -LiteralPath $EnvFilePath) {
        $lines += ''
        $lines += 'REST credentials:'
        foreach ($line in [System.IO.File]::ReadAllLines($EnvFilePath)) {
            if ($line -match '^RESTIC_REST_(USERNAME|PASSWORD)=') { $lines += "  $line" }
        }
    }
    if ($tunnelSection) {
        $lines += ''
        $lines += 'wireguard tunnel:'
        foreach ($key in @('endpoint','server_public_key','local_ip','server_ip','rest_port')) {
            $lines += "  $key`: $($tunnelValues[$key])"
        }
        $lines += '  tunnel.key contents (WireGuard private key):'
        if (Test-Path -LiteralPath $TunnelKeyPath) {
            $lines += "    $(([System.IO.File]::ReadAllText($TunnelKeyPath)).Trim())"
        } else {
            $lines += '    unavailable'
        }
        $lines += ''
        $lines += 'disaster recovery for tunnel repositories:'
        $lines += '  1. Register the recovery host or reuse a host token in agent.toml.'
        $lines += '  2. If the old peer was revoked, run:'
        $lines += '     sm-agent backup tunnel-enroll --config backup.toml --agent-config agent.toml'
        $lines += '  3. Run: sm-agent backup proxy --config backup.toml'
        $lines += '  4. Use the printed RESTIC_REPOSITORY with restic and the repository password above.'
    }
    $lines += ''
    $lines += 'restore any repo on a bare machine (needs restic + this password):'
    $lines += '  restic -r <url> restore latest --target C:\recover'
    $lines += ''
    $lines += 'STORE THIS IN A PASSWORD MANAGER NOW. No other copy of this password'
    $lines += 'exists anywhere -- the monitoring server never sees it. Lose this kit'
    $lines += 'and the host, and the backups are unrecoverable.'
    $text = ($lines -join "`r`n")
    $utf8NoBom = New-Object System.Text.UTF8Encoding($false)
    [System.IO.File]::WriteAllText($KitPath, $text, $utf8NoBom)
    Lock-Acl -Path $KitPath -ServiceAccess None
}

function Show-RecoveryKit {
    param([Parameter(Mandatory=$true)][string]$KitPath)
    Write-Host ''
    Write-Host '================ BACKUP RECOVERY KIT (store offline NOW) ================'
    Write-Host ([System.IO.File]::ReadAllText($KitPath))
    Write-Host '========================================================================'
    Write-Host "(also saved to $KitPath, SYSTEM + Administrators only)"
    Write-Host ''
}

function Move-LegacyBackupFiles {
    param(
        [Parameter(Mandatory=$true)][string]$BackupDir,
        [Parameter(Mandatory=$true)][string]$BackupToml,
        [Parameter(Mandatory=$true)][string]$BackupKey,
        [Parameter(Mandatory=$true)][string]$RecoveryKit
    )
    $legacyToml = Join-Path $configDir 'backup.toml'
    $legacyKey  = Join-Path $configDir 'backup.key'
    $legacyKit  = Join-Path $configDir 'recovery-kit.txt'
    if ((-not (Test-Path -LiteralPath $legacyToml)) -and (-not (Test-Path -LiteralPath $legacyKey))) { return }
    if (-not (Test-Path $BackupDir)) { New-Item -ItemType Directory -Path $BackupDir | Out-Null }
    Lock-Acl -Path $BackupDir -ServiceAccess None
    if ((Test-Path -LiteralPath $legacyToml) -and (-not (Test-Path -LiteralPath $BackupToml))) {
        Move-Item -Force -LiteralPath $legacyToml -Destination $BackupToml
        $utf8NoBom = New-Object System.Text.UTF8Encoding($false)
        $content = [System.IO.File]::ReadAllText($BackupToml)
        $content = $content.Replace((Escape-Toml $legacyKey), (Escape-Toml $BackupKey))
        [System.IO.File]::WriteAllText($BackupToml, $content, $utf8NoBom)
        Lock-Acl -Path $BackupToml -ServiceAccess None
    }
    if ((Test-Path -LiteralPath $legacyKey) -and (-not (Test-Path -LiteralPath $BackupKey))) {
        Move-Item -Force -LiteralPath $legacyKey -Destination $BackupKey
        Lock-Acl -Path $BackupKey -ServiceAccess None
    }
    if ((Test-Path -LiteralPath $legacyKit) -and (-not (Test-Path -LiteralPath $RecoveryKit))) {
        Move-Item -Force -LiteralPath $legacyKit -Destination $RecoveryKit
        Lock-Acl -Path $RecoveryKit -ServiceAccess None
    }
    Write-Host "note: relocated backup config into $BackupDir"
}

function Invoke-BackupProvisioning {
    param([Parameter(Mandatory=$true)][string]$AgentExe)
    $backupDir    = Join-Path $configDir 'Backup'
    $backupToml   = Join-Path $backupDir 'backup.toml'
    $backupKey    = Join-Path $backupDir 'backup.key'
    $backupEnvFile = Join-Path $backupDir 'repo-credentials.env'
    $tunnelKey    = Join-Path $backupDir 'tunnel.key'
    $recoveryKit  = Join-Path $backupDir 'recovery-kit.txt'
    $backupStatus = Join-Path $configDir 'backup-status.json'
    Move-LegacyBackupFiles -BackupDir $backupDir -BackupToml $backupToml -BackupKey $backupKey -RecoveryKit $recoveryKit
    $repoArr = @()
    if ($BackupRepos)     { $repoArr = $BackupRepos.Split(',')     | ForEach-Object { $_.Trim() } | Where-Object { $_ } }
    $nameArr = @()
    if ($BackupRepoNames) { $nameArr = $BackupRepoNames.Split(',') | ForEach-Object { $_.Trim() } | Where-Object { $_ } }
    $pathArr = @()
    if ($BackupPaths)     { $pathArr = $BackupPaths.Split(',')     | ForEach-Object { $_.Trim() } | Where-Object { $_ } }
    if (-not $pathArr) { $pathArr = @('C:\Users') }
    $requestedTunnelRepo = $false
    foreach ($repo in $repoArr) {
        if ($repo -match '^tunnel:(.*)$') {
            $tunnelName = $Matches[1]
            if ($tunnelName -notmatch '^([A-Za-z0-9._-]+/)?[A-Za-z0-9._-]+$') {
                throw "invalid tunnel repository '$repo'; use tunnel:NAME (this server) or tunnel:NODE/NAME (a promoted backup node), each part matching ^[A-Za-z0-9._-]+$"
            }
            $requestedTunnelRepo = $true
        }
    }
    $hasTunnelRepo = $requestedTunnelRepo
    if ((-not $repoArr) -and (Test-Path -LiteralPath $backupToml)) {
        $hasTunnelRepo = [System.IO.File]::ReadAllText($backupToml) -match '(?m)^\s*tunnel_name\s*='
    }
    $preservedTunnelSection = if ($hasTunnelRepo) { New-TunnelSectionSnapshot -TomlPath $backupToml } else { '' }

    if ($EnableBackup) {
        if ((-not (Test-Path -LiteralPath $backupToml)) -and (-not $repoArr)) {
            throw '-EnableBackup requires -BackupRepos (or $env:SM_BACKUP_REPOS) on a fresh setup'
        }
        if (-not (Test-Path -LiteralPath $AgentExe)) {
            throw "agent binary not found at $AgentExe; the backup task runs the admin-only copy in Program Files, not the service-writable one. Set `$env:SM_REINSTALL=1 and re-run to restore it"
        }
        if ($hasTunnelRepo -and $Reconfigure) {
            $previousErrorActionPreference = $ErrorActionPreference
            try {
                $ErrorActionPreference = 'Continue'
                $tunnelUsage = @(& $AgentExe backup 2>&1) -join "`n"
            } finally {
                $ErrorActionPreference = $previousErrorActionPreference
            }
            if ($tunnelUsage -notmatch 'tunnel-enroll') {
                throw "$AgentExe does not support backup tunnel-enroll; set `$env:SM_REINSTALL=1 and re-run to download a current sm-agent"
            }
        }
        if ($BackupPruneMode -ne 'host' -and $BackupPruneMode -ne 'external') {
            throw 'SM_BACKUP_PRUNE_MODE / -BackupPruneMode must be "host" or "external"'
        }
        if ([bool]$env:SM_BACKUP_S3_ACCESS_KEY_ID -ne [bool]$env:SM_BACKUP_S3_SECRET_ACCESS_KEY) {
            throw 'SM_BACKUP_S3_ACCESS_KEY_ID and SM_BACKUP_S3_SECRET_ACCESS_KEY must be set together'
        }
        Write-BackupWarning
        Install-Restic -InstallDir $installDir
        if (-not (Test-Path $backupDir)) { New-Item -ItemType Directory -Path $backupDir | Out-Null }
        Lock-Acl -Path $backupDir -ServiceAccess None
        $keyFresh = New-BackupKey -KeyPath $backupKey
        $hasEnvFile = New-BackupEnvFile -EnvPath $backupEnvFile
        $envFileArg = if ($hasEnvFile) { $backupEnvFile } else { '' }
        $reposWritten = $false
        if ($repoArr) {
            Write-BackupToml -TomlPath $backupToml -StatusPath $backupStatus -KeyPath $backupKey -ResticPath (Join-Path $installDir 'restic.exe') -Repos $repoArr -Names $nameArr -Paths $pathArr -EnvFilePath $envFileArg -TunnelSection $preservedTunnelSection -PruneMode $BackupPruneMode -S3Region $BackupS3Region -S3PathStyle:$BackupS3PathStyle
            $reposWritten = $true
        } elseif (Test-Path -LiteralPath $backupToml) {
            Write-Host 'note: -BackupRepos not provided; keeping existing backup.toml'
            Lock-Acl -Path $backupToml -ServiceAccess None
        }
        if ($hasTunnelRepo) {
            $agentConfig = Join-Path $configDir 'agent.toml'
            $previousErrorActionPreference = $ErrorActionPreference
            try {
                $ErrorActionPreference = 'Continue'
                & $AgentExe backup tunnel-enroll --config $backupToml --agent-config $agentConfig
                $tunnelEnrollExit = $LASTEXITCODE
            } finally {
                $ErrorActionPreference = $previousErrorActionPreference
            }
            if ($tunnelEnrollExit -eq 0) {
                if (-not (Test-Path -LiteralPath $tunnelKey)) {
                    throw "sm-agent backup tunnel-enroll succeeded but $tunnelKey was not created"
                }
            } elseif ($preservedTunnelSection) {
                Write-Warning 'sm-agent backup tunnel-enroll failed; keeping the preserved [tunnel] section and continuing'
            } else {
                throw 'sm-agent backup tunnel-enroll failed and no existing [tunnel] section is available; backups could never work'
            }
        }
        & $AgentExe backup init --config $backupToml
        if ($LASTEXITCODE -ne 0) { throw 'sm-agent backup init failed; backup not scheduled' }
        Register-BackupTask -AgentExe $AgentExe -ConfigPath $backupToml -Time $BackupTime
        Register-BackupCheckTask -AgentExe $AgentExe -ConfigPath $backupToml -Time $BackupTime
        if ($keyFresh) {
            Write-RecoveryKit -KitPath $recoveryKit -KeyPath $backupKey -TomlPath $backupToml -EnvFilePath $backupEnvFile -TunnelKeyPath $tunnelKey -Repos $repoArr -Names $nameArr
            Show-RecoveryKit -KitPath $recoveryKit
        } elseif ($reposWritten) {
            Write-RecoveryKit -KitPath $recoveryKit -KeyPath $backupKey -TomlPath $backupToml -EnvFilePath $backupEnvFile -TunnelKeyPath $tunnelKey -Repos $repoArr -Names $nameArr
            Write-Host "note: recovery kit updated at $recoveryKit (password unchanged, not reprinted)"
        } else {
            Write-Host "note: backup recovery kit at $recoveryKit (password not reprinted)"
        }
    } else {
        $existingCheck = Get-ScheduledTask -TaskName 'ServerMonitor Backup Check' -ErrorAction SilentlyContinue
        if ($existingCheck) {
            Unregister-ScheduledTask -TaskName 'ServerMonitor Backup Check' -Confirm:$false
        }
        $existingTask = Get-ScheduledTask -TaskName 'ServerMonitor Backup' -ErrorAction SilentlyContinue
        if ($existingTask) {
            Unregister-ScheduledTask -TaskName 'ServerMonitor Backup' -Confirm:$false
            Write-Host ''
            Write-Host 'note: removed the ServerMonitor Backup scheduled task. backup.toml, backup.key'
            Write-Host 'and recovery-kit.txt were KEPT (they guard existing snapshots). Remove manually with:'
            Write-Host "  Remove-Item -Recurse '$backupDir'"
            Write-Host ''
        }
    }
}

$installDir = Join-Path $env:ProgramFiles 'ServerMonitor'
$configDir  = Join-Path $env:ProgramData 'ServerMonitor'
$runtimeDir = Join-Path $configDir 'bin'
if (-not (Test-Path $installDir)) { New-Item -ItemType Directory -Path $installDir | Out-Null }
if (-not (Test-Path $configDir))  { New-Item -ItemType Directory -Path $configDir  | Out-Null }
if (-not (Test-Path $runtimeDir)) { New-Item -ItemType Directory -Path $runtimeDir | Out-Null }
Lock-Acl -Path $installDir -ServiceAccess Read
Lock-Acl -Path $configDir  -ServiceAccess Modify
Lock-Acl -Path $runtimeDir -ServiceAccess Modify

$exe        = Join-Path $installDir 'sm-agent.exe'
$runtimeExe = Join-Path $runtimeDir 'sm-agent.exe'
$cfgPath    = Join-Path $configDir 'agent.toml'
$spoolPath  = Join-Path $configDir 'spool.db'

if ($Reconfigure) {
    if (-not (Test-Path -LiteralPath $runtimeExe)) {
        throw "found $cfgPath but no agent binary at $runtimeExe; set `$env:SM_REINSTALL=1 for a full install"
    }
    if (Test-Path -LiteralPath $exe) { Lock-Acl -Path $exe -ServiceAccess Read }
    Lock-Acl -Path $runtimeExe -ServiceAccess Modify
    Lock-Acl -Path $cfgPath    -ServiceAccess Modify
    if (Test-Path -LiteralPath $spoolPath) { Lock-Acl -Path $spoolPath -ServiceAccess Modify }
}

if (-not $Reconfigure) {
    if ($Insecure) {
        [System.Net.ServicePointManager]::ServerCertificateValidationCallback = { $true }
    }

    Write-Host "downloading $ServerUrl agent for $platform ..."
    Invoke-WebRequest -UseBasicParsing -Uri "$ServerUrl/api/v1/agent/binary?platform=$platform" -Headers @{ 'X-Agent-Token' = $Token } -OutFile $exe
    Copy-Item -Force -Path $exe -Destination $runtimeExe
    Lock-Acl -Path $exe        -ServiceAccess Read
    Lock-Acl -Path $runtimeExe -ServiceAccess Modify
}

if (-not $Reconfigure) {
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
}

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

$binPath = "`"$runtimeExe`" --config `"$cfgPath`""
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

if ($EnableSmart) { Install-Smartmontools }

Start-Service -Name 'sm-agent'

Invoke-BackupProvisioning -AgentExe $exe

Write-Host ''
if ($Reconfigure) {
    Write-Host "reconfigured. agent restarted as $svcAccount."
} else {
    Write-Host "installed. agent is running as $svcAccount."
}
Write-Host "service: Get-Service sm-agent"
Write-Host "logs:    Get-WinEvent -LogName Application -ProviderName 'sm-agent'"
if (-not $AdminService) {
    Write-Host ''
    Write-Host 'note: SMART and full-process collectors require admin. Pass -AdminService or set $env:SM_ADMIN_SERVICE=1 to install as LocalSystem if you need them.'
    Write-Host 'note: this includes NVMe SMART -- on Windows it works under the Admin service (LocalSystem) with a recent smartmontools, and unlike Linux needs no extra capability.'
}
if (-not $EnableSmart) {
    Write-Host 'note: pass -EnableSmart (or set $env:SM_ENABLE_SMART=1) to auto-install smartmontools for the SMART collector.'
}

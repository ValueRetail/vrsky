<#
.SYNOPSIS
  Installs, or upgrades, the VRSky remote agent on this Windows machine.

.DESCRIPTION
  Served by VRSky at /api/v1/agents/install.ps1. Settings -> Remote agents shows
  the exact command to run; it looks like

    & ([scriptblock]::Create((irm https://HOST/api/v1/agents/install.ps1))) -Url https://HOST -Token vrsky_reg_...

  Run it in a PowerShell opened with "Run as administrator". It
    1. downloads vrsky-agent.exe from VRSky and checks its SHA-256,
    2. writes the agent's config, asking which folders to use (kept if one exists),
    3. registers this machine with the one-time token (skipped if already registered),
    4. installs the VRSky Agent service and starts it.
  Running it again upgrades the agent and keeps the config and registration.

  The folders you choose stay in the config file on this machine. VRSky only
  ever sees their names ("inbox", "outbox"), never their paths.

.PARAMETER Url      VRSky's address, e.g. https://vrsky.valueretail.no
.PARAMETER Token    One-time registration token from Settings -> Remote agents.
                    Not needed when the machine is already registered.
.PARAMETER Name     This agent's name in VRSky. Default: the computer name.
.PARAMETER Inbox    Folder VRSky reads new files from. Asked for if not given.
.PARAMETER Outbox   Folder VRSky writes files into. Asked for if not given.
.PARAMETER ExePath  Use this local vrsky-agent.exe instead of downloading (USB stick, offline).
.PARAMETER NoService Configure and register only; do not install the service.
#>
[CmdletBinding()]
param(
  [Parameter(Mandatory = $true)][string]$Url,
  [string]$Token = '',
  [string]$Name = $env:COMPUTERNAME,
  [string]$Inbox = '',
  [string]$Outbox = '',
  [string]$ExePath = '',
  [switch]$NoService
)

$ErrorActionPreference = 'Stop'
$ServiceName = 'VRSkyAgent'
$InstallDir  = 'C:\Program Files\VRSky'
$Exe         = Join-Path $InstallDir 'vrsky-agent.exe'
$DataDir     = 'C:\ProgramData\VRSky\agent'
$ConfigPath  = Join-Path $DataDir 'config.json'

function Step($msg) { Write-Host "==> $msg" -ForegroundColor Cyan }
function Note($msg) { Write-Host "    $msg" -ForegroundColor Yellow }
function Fail($msg) { Write-Host "ERROR: $msg" -ForegroundColor Red; exit 1 }

# --- 1. Administrator ---------------------------------------------------------
$identity  = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = New-Object Security.Principal.WindowsPrincipal $identity
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
  Fail "Run this in a PowerShell opened with 'Run as administrator' (the prompt then starts with C:\Windows\system32)."
}

$Url = $Url.TrimEnd('/')
if ($Url -notmatch '^https?://') { Fail "-Url must start with https:// (got '$Url')." }
if ($Token -and $Token -notmatch '^vrsky_reg_[0-9a-f]+$') {
  Fail "-Token must be the vrsky_reg_... token from Settings -> Remote agents, nothing else. Got: '$Token'"
}

# PowerShell 5.1 on older Windows 10 may still default to TLS 1.0.
try {
  [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
} catch { }

# --- 2. The agent binary ------------------------------------------------------
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
$staged = "$Exe.download"

if ($ExePath) {
  if (-not (Test-Path $ExePath)) { Fail "-ExePath '$ExePath' does not exist." }
  Step "Using the agent at $ExePath"
  Copy-Item $ExePath $staged -Force
} else {
  Step "Downloading the agent from $Url"
  try {
    $release = (Invoke-RestMethod -UseBasicParsing -Uri "$Url/api/v1/agents/release").data
    Invoke-WebRequest -UseBasicParsing -Uri "$Url/api/v1/agents/download/windows-amd64" -OutFile $staged
  } catch {
    Fail "Could not download the agent from $Url : $($_.Exception.Message)"
  }
  $actual = (Get-FileHash -Path $staged -Algorithm SHA256).Hash.ToLower()
  if ($actual -ne $release.sha256.ToLower()) {
    Remove-Item $staged -Force
    Fail "The download is corrupt (checksum mismatch). Run the command again."
  }
  Step "Downloaded vrsky-agent $($release.version); checksum OK"
}

# A running service holds the .exe open, so an upgrade stops it first and
# starts it again below.
$service    = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
$wasRunning = ($null -ne $service) -and ($service.Status -eq 'Running')
if ($wasRunning) {
  Step "Stopping the running service for the upgrade"
  Stop-Service -Name $ServiceName
}
Move-Item -Path $staged -Destination $Exe -Force
Unblock-File -Path $Exe

# --- 3. Config ----------------------------------------------------------------
New-Item -ItemType Directory -Force -Path $DataDir | Out-Null
if (Test-Path $ConfigPath) {
  Step "Keeping the existing config at $ConfigPath"
} else {
  if (-not $Inbox) {
    $Inbox = Read-Host "Folder VRSky READS new files from [C:\VRSky\inbox]"
    if (-not $Inbox) { $Inbox = 'C:\VRSky\inbox' }
  }
  if (-not $Outbox) {
    $Outbox = Read-Host "Folder VRSky WRITES files into   [C:\VRSky\outbox]"
    if (-not $Outbox) { $Outbox = 'C:\VRSky\outbox' }
  }
  foreach ($dir in @($Inbox, $Outbox)) {
    if (-not [IO.Path]::IsPathRooted($dir)) { Fail "Folder paths must be absolute, e.g. C:\VRSky\inbox (got '$dir')." }
    New-Item -ItemType Directory -Force -Path $dir | Out-Null
  }
  $config = [ordered]@{
    agent_name  = $Name
    directories = [ordered]@{
      inbox  = [ordered]@{ path = $Inbox;  mode = 'read'  }
      outbox = [ordered]@{ path = $Outbox; mode = 'write' }
    }
  }
  $json = $config | ConvertTo-Json -Depth 5
  [IO.File]::WriteAllText($ConfigPath, $json, (New-Object Text.UTF8Encoding $false))
  Step "Wrote $ConfigPath"
}

Step "Checking the config"
$check = (& $Exe check 2>&1) | Out-String
Write-Host $check
if ($LASTEXITCODE -ne 0) { Fail "vrsky-agent check failed. Fix $ConfigPath and run the command again." }

# --- 4. Registration ----------------------------------------------------------
$registered = $true
if ($check -match 'Not registered yet' -or $check -match 'REVOKED') {
  if ($check -match 'REVOKED') {
    Note "This machine's registration was revoked in VRSky."
    Remove-Item (Join-Path $DataDir 'credential.json') -Force -ErrorAction SilentlyContinue
  }
  if ($Token) {
    Step "Registering as '$Name'"
    & $Exe register --url $Url --token $Token --name $Name
    if ($LASTEXITCODE -ne 0) { Fail "Registration failed (see above). Generate a new token and run the command again." }
  } else {
    $registered = $false
    Note "Not registered. Generate a token under Settings -> Remote agents and run this command again with -Token <token>."
  }
} elseif ($Token) {
  Note "Already registered; the token was not needed and is still unused."
}

# --- 5. Service ---------------------------------------------------------------
if ($NoService -or -not $registered) {
  if ($registered) { Note "Service not installed (-NoService). Try it with: & '$Exe' run" }
} else {
  $service = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
  if ($null -eq $service) {
    Step "Installing the VRSky Agent service (starts at boot)"
    & $Exe install
    if ($LASTEXITCODE -ne 0) { Fail "Service install failed (see above)." }
  }
  Step "Starting the service"
  & $Exe start
  if ($LASTEXITCODE -ne 0) { Fail "The service did not start. Log: $DataDir\logs\agent.log" }
  & $Exe status
  Write-Host ""
  Write-Host "Done. This machine now shows under Settings -> Remote agents." -ForegroundColor Green
  Write-Host "Log: $DataDir\logs\agent.log   Config: $ConfigPath"
}

<#
.SYNOPSIS
  Removes the VRSky remote agent from this Windows machine.

.DESCRIPTION
  Served by VRSky at /api/v1/agents/uninstall.ps1. Run in a PowerShell opened
  with "Run as administrator":

    & ([scriptblock]::Create((irm https://HOST/api/v1/agents/uninstall.ps1)))

  Stops and removes the VRSky Agent service and deletes vrsky-agent.exe. The
  config, credential and logs in C:\ProgramData\VRSky\agent are kept, so
  reinstalling needs no new token; pass -Purge to delete them too.

  Revoke the agent under Settings -> Remote agents as well: this script has no
  login and cannot do that for you.

.PARAMETER Purge  Also delete the config, credential and logs.
#>
[CmdletBinding()]
param([switch]$Purge)

$ErrorActionPreference = 'Stop'
$ServiceName = 'VRSkyAgent'
$InstallDir  = 'C:\Program Files\VRSky'
$Exe         = Join-Path $InstallDir 'vrsky-agent.exe'
$DataDir     = 'C:\ProgramData\VRSky\agent'

function Step($msg) { Write-Host "==> $msg" -ForegroundColor Cyan }
function Fail($msg) { Write-Host "ERROR: $msg" -ForegroundColor Red; exit 1 }

$identity  = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = New-Object Security.Principal.WindowsPrincipal $identity
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
  Fail "Run this in a PowerShell opened with 'Run as administrator'."
}

$service = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
if ($null -ne $service) {
  if ($service.Status -eq 'Running') {
    Step "Stopping the service"
    Stop-Service -Name $ServiceName
  }
  Step "Removing the service"
  if (Test-Path $Exe) {
    & $Exe uninstall
    if ($LASTEXITCODE -ne 0) { Fail "vrsky-agent uninstall failed (see above)." }
  } else {
    & sc.exe delete $ServiceName | Out-Null
  }
}

if (Test-Path $Exe) {
  Step "Deleting $Exe"
  Remove-Item -Path $Exe -Force
  Remove-Item -Path "$Exe.download" -Force -ErrorAction SilentlyContinue
  if (-not (Get-ChildItem -Path $InstallDir -Force -ErrorAction SilentlyContinue)) {
    Remove-Item -Path $InstallDir -Force
  }
}

if ($Purge) {
  if (Test-Path $DataDir) {
    Step "Deleting the config, credential and logs in $DataDir"
    Remove-Item -Path $DataDir -Recurse -Force
  }
} else {
  Write-Host "Kept the config, credential and logs in $DataDir (use -Purge to delete them)."
}

Write-Host ""
Write-Host "The agent is removed from this machine. Revoke it under Settings -> Remote agents too." -ForegroundColor Green

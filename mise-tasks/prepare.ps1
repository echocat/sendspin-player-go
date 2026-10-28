#MISE description="Prepare project build and coverage output directories"
$ErrorActionPreference = 'Stop'
New-Item -ItemType Directory -Force -Path 'var/bin','var/coverage' | Out-Null
if (-not (Test-Path 'var/go.mod')) {
    [System.IO.File]::WriteAllText((Join-Path (Get-Location) 'var/go.mod'), "module github.com/blaubaer/sendspin-player-go/var`n", [System.Text.UTF8Encoding]::new($false))
}

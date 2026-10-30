#MISE description="Prepare project build and coverage output directories"
$ErrorActionPreference = 'Stop'
New-Item -ItemType Directory -Force -Path 'var/bin','var/coverage' | Out-Null

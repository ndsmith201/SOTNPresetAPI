param(
    [Parameter(Mandatory)][string]$TableName,
    [Parameter(Mandatory)][string]$UserPoolId,
    [string]$Region = 'us-east-1',
    [string]$Profile,
    [switch]$Apply
)
$ErrorActionPreference = 'Stop'
$backfillArguments = @('run', './cmd/backfill-option-authors', '-table', $TableName, '-user-pool', $UserPoolId, '-region', $Region)
if ($Profile) { $backfillArguments += @('-profile', $Profile) }
if ($Apply) { $backfillArguments += '-apply' }
Push-Location (Split-Path -Parent $PSScriptRoot)
try {
    & go @backfillArguments
    if ($LASTEXITCODE -ne 0) { throw "Backfill failed with exit code $LASTEXITCODE. Review the summary before rerunning." }
} finally {
    Pop-Location
}

#!/usr/bin/env pwsh
<#
.SYNOPSIS
    Builds and deploys the Game of Life server to Google Cloud Run.

.DESCRIPTION
    Builds the Docker image, pushes it to Artifact Registry, and deploys
    (or re-deploys) the Cloud Run service.

    REDIS_ADDR and REDIS_PASSWORD are read from Cloud Secret Manager at
    runtime by the application. They are never stored in environment variables
    or passed through this script.

    Run with -Init on the very first deploy to create the Artifact Registry
    repository and grant the Cloud Run service account access to secrets.

.PARAMETER ProjectId
    GCP project ID. Defaults to the active gcloud project.

.PARAMETER Region
    GCP region for Cloud Run. Default: us-central1

.PARAMETER ServiceName
    Cloud Run service name. Default: golive

.PARAMETER Init
    Perform first-time setup: enable APIs, create Artifact Registry repo,
    and grant the Cloud Run service account Secret Manager access.

.EXAMPLE
    # First-time setup
    ./deploy.ps1 -ProjectId dumb-game-of-life -Init

    # Every subsequent deploy
    ./deploy.ps1
#>
param(
    [string]$ProjectId = (gcloud config get-value project 2>$null),
    [string]$Region = "us-central1",
    [string]$ServiceName = "golive",
    [switch]$Init
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

# Invoke-Cmd runs an external command and halts the script on non-zero exit.
# $ErrorActionPreference = "Stop" only catches PowerShell cmdlet errors;
# external programs (docker, gcloud) must be checked via $LASTEXITCODE.
function Invoke-Cmd {
    param([string]$Description, [scriptblock]$Command)
    Write-Host "  > $Description" -ForegroundColor DarkGray
    & $Command
    if ($LASTEXITCODE -ne 0) {
        Write-Host "ERROR: '$Description' failed (exit code $LASTEXITCODE)." -ForegroundColor Red
        exit $LASTEXITCODE
    }
}

# --- Derived names ---
$RepoName = "golive-repo"
$ImageBase = "$Region-docker.pkg.dev/$ProjectId/$RepoName/$ServiceName"
$ImageTag = "${ImageBase}:$(git rev-parse --short HEAD)"

if (-not $ProjectId) {
    Write-Host "ERROR: Could not detect GCP project. Pass -ProjectId or run: gcloud config set project YOUR_PROJECT" -ForegroundColor Red
    exit 1
}

Write-Host "Project : $ProjectId" -ForegroundColor Cyan
Write-Host "Region  : $Region"    -ForegroundColor Cyan
Write-Host "Service : $ServiceName" -ForegroundColor Cyan
Write-Host "Image   : $ImageTag"  -ForegroundColor Cyan

# --- First-time setup ---
if ($Init) {
    Write-Host ""
    Write-Host "[1/3] Enabling required APIs..." -ForegroundColor Yellow
    Invoke-Cmd "gcloud services enable" {
        gcloud services enable `
            artifactregistry.googleapis.com `
            run.googleapis.com `
            secretmanager.googleapis.com `
            --project $ProjectId
    }

    Write-Host ""
    Write-Host "[2/3] Creating Artifact Registry repository..." -ForegroundColor Yellow
    Invoke-Cmd "gcloud artifacts repositories create" {
        gcloud artifacts repositories create $RepoName `
            --repository-format=docker `
            --location=$Region `
            --description="Game of Life container images" `
            --project $ProjectId
    }

    Write-Host ""
    Write-Host "[3/3] Granting Cloud Run service account access to secrets..." -ForegroundColor Yellow
    $ProjectNumber = gcloud projects describe $ProjectId --format="value(projectNumber)"
    if ($LASTEXITCODE -ne 0) {
        Write-Host "ERROR: Could not retrieve project number for $ProjectId." -ForegroundColor Red
        exit 1
    }
    $RunSA = "$ProjectNumber-compute@developer.gserviceaccount.com"

    Invoke-Cmd "gcloud projects add-iam-policy-binding" {
        gcloud projects add-iam-policy-binding $ProjectId `
            --member="serviceAccount:$RunSA" `
            --role="roles/secretmanager.secretAccessor" `
            --quiet
    }

    Write-Host ""
    Write-Host "Setup complete." -ForegroundColor Green
    Write-Host ""
    Write-Host "Before deploying, ensure the following secrets exist in Secret Manager:" -ForegroundColor Yellow
    Write-Host "  REDIS_ADDR     - e.g. 10.0.0.3:6379 or your-redis-host:6379" -ForegroundColor White
    Write-Host "  REDIS_PASSWORD - the AUTH password for your Redis instance" -ForegroundColor White
    Write-Host ""
    Write-Host "Create them like this (PowerShell - no trailing newline):" -ForegroundColor Yellow
    Write-Host '  [IO.File]::WriteAllBytes("addr.tmp", [Text.Encoding]::UTF8.GetBytes("host:port"))' -ForegroundColor White
    Write-Host "  gcloud secrets create REDIS_ADDR --data-file=addr.tmp --project $ProjectId" -ForegroundColor White
    Write-Host '  [IO.File]::WriteAllBytes("pw.tmp",   [Text.Encoding]::UTF8.GetBytes("yourpassword"))' -ForegroundColor White
    Write-Host "  gcloud secrets create REDIS_PASSWORD --data-file=pw.tmp --project $ProjectId" -ForegroundColor White
    Write-Host "  Remove-Item addr.tmp, pw.tmp" -ForegroundColor White
    Write-Host ""
    Write-Host "Then run ./deploy.ps1 to deploy." -ForegroundColor Green
    exit 0
}

# --- Verify secrets exist before attempting deploy ---
Write-Host ""
Write-Host "Verifying secrets in Secret Manager..." -ForegroundColor Yellow
foreach ($SecretName in @("REDIS_ADDR", "REDIS_PASSWORD")) {
    $exists = gcloud secrets describe $SecretName --project $ProjectId --format="value(name)" 2>$null
    if ($LASTEXITCODE -ne 0 -or -not $exists) {
        Write-Host "ERROR: Secret $SecretName not found in project $ProjectId." -ForegroundColor Red
        Write-Host "       Create it or run ./deploy.ps1 -Init for instructions." -ForegroundColor Red
        exit 1
    }
    Write-Host "  $SecretName - OK" -ForegroundColor Green
}

# --- Build and push image ---
Write-Host ""
Write-Host "[1/2] Building and pushing image..." -ForegroundColor Yellow

Invoke-Cmd "gcloud auth configure-docker" {
    gcloud auth configure-docker "$Region-docker.pkg.dev" --quiet
}

Invoke-Cmd "docker build" {
    docker build --platform linux/amd64 -t $ImageTag .
}

Invoke-Cmd "docker push" {
    docker push $ImageTag
}

Write-Host "  Image pushed: $ImageTag" -ForegroundColor Green

# --- Deploy to Cloud Run ---
Write-Host ""
Write-Host "[2/2] Deploying to Cloud Run..." -ForegroundColor Yellow

Invoke-Cmd "gcloud run deploy" {
    gcloud run deploy $ServiceName `
        --image=$ImageTag `
        --region=$Region `
        --project=$ProjectId `
        --platform=managed `
        --allow-unauthenticated `
        --port=8080 `
        --min-instances=1 `
        --max-instances=1 `
        --cpu=2 `
        --memory=1Gi `
        --concurrency=1000 `
        --timeout=3600 `
        --session-affinity
}

$ServiceUrl = gcloud run services describe $ServiceName `
    --region=$Region `
    --project=$ProjectId `
    --format="value(status.url)"
if ($LASTEXITCODE -ne 0) {
    Write-Host "ERROR: Could not retrieve service URL." -ForegroundColor Red
    exit 1
}

$WssUrl = $ServiceUrl -replace 'https', 'wss'

Write-Host ""
Write-Host "Deploy complete!" -ForegroundColor Green
Write-Host "  URL      : $ServiceUrl" -ForegroundColor White
Write-Host "  WebSocket: $WssUrl/ws"  -ForegroundColor White

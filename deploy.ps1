#!/usr/bin/env pwsh
<#
.SYNOPSIS
    Builds and deploys the Game of Life server to Google Cloud Run.

.DESCRIPTION
    Builds the Docker image, pushes it to Artifact Registry, and deploys
    (or re-deploys) the Cloud Run service.  Run with -Init on the very first
    deploy to create the Artifact Registry repository and Memorystore instance.

.PARAMETER ProjectId
    GCP project ID. Defaults to the active gcloud project.

.PARAMETER Region
    GCP region for Cloud Run and Memorystore. Default: us-central1

.PARAMETER ServiceName
    Cloud Run service name. Default: golive

.PARAMETER Init
    Perform first-time infrastructure setup (Artifact Registry repo,
    Serverless VPC Connector, Memorystore instance).

.EXAMPLE
    # First-time setup
    ./deploy.ps1 -ProjectId my-gcp-project -Init

    # Subsequent deploys
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

# ── Derived names ─────────────────────────────────────────────────────────────
$RepoName = "golive-repo"
$ImageBase = "$Region-docker.pkg.dev/$ProjectId/$RepoName/$ServiceName"
$ImageTag = "${ImageBase}:$(git rev-parse --short HEAD)"
$RedisName = "golive-redis"
$ConnectorName = "golive-connector"
$Network = "default"

if (-not $ProjectId) {
    Write-Error "Could not detect GCP project. Pass -ProjectId or run: gcloud config set project YOUR_PROJECT"
    exit 1
}

Write-Host "▶ Project : $ProjectId" -ForegroundColor Cyan
Write-Host "▶ Region  : $Region"    -ForegroundColor Cyan
Write-Host "▶ Service : $ServiceName" -ForegroundColor Cyan
Write-Host "▶ Image   : $ImageTag"  -ForegroundColor Cyan

# ── First-time infrastructure ─────────────────────────────────────────────────
if ($Init) {
    Write-Host "`n[1/4] Enabling required APIs..." -ForegroundColor Yellow
    gcloud services enable `
        artifactregistry.googleapis.com `
        run.googleapis.com `
        redis.googleapis.com `
        vpcaccess.googleapis.com `
        --project $ProjectId

    Write-Host "`n[2/4] Creating Artifact Registry repository..." -ForegroundColor Yellow
    gcloud artifacts repositories create $RepoName `
        --repository-format=docker `
        --location=$Region `
        --description="Game of Life container images" `
        --project $ProjectId

    Write-Host "`n[3/4] Creating Memorystore Redis instance (may take ~5 min)..." -ForegroundColor Yellow
    gcloud redis instances create $RedisName `
        --size=1 `
        --region=$Region `
        --redis-version=redis_7_0 `
        --tier=STANDARD_HA `
        --network=$Network `
        --project $ProjectId
    Write-Host "  ✓ Redis created. Fetching host..." -ForegroundColor Green

    Write-Host "`n[4/4] Creating Serverless VPC Connector (Cloud Run → Memorystore)..." -ForegroundColor Yellow
    gcloud compute networks vpc-access connectors create $ConnectorName `
        --region=$Region `
        --network=$Network `
        --range="10.8.0.0/28" `
        --project $ProjectId

    Write-Host "`n✅ Infrastructure ready. Run the script again without -Init to deploy." -ForegroundColor Green
    exit 0
}

# ── Resolve Memorystore IP ────────────────────────────────────────────────────
Write-Host "`n[1/3] Resolving Memorystore host..." -ForegroundColor Yellow
$RedisHost = gcloud redis instances describe $RedisName `
    --region=$Region `
    --project $ProjectId `
    --format="value(host)" 2>$null

if (-not $RedisHost) {
    Write-Error "Could not find Memorystore instance '$RedisName' in $Region. Run: ./deploy.ps1 -Init"
    exit 1
}
$RedisAddr = "${RedisHost}:6379"
Write-Host "  ✓ Redis at $RedisAddr" -ForegroundColor Green

# ── Build & push image ────────────────────────────────────────────────────────
Write-Host "`n[2/3] Building and pushing image..." -ForegroundColor Yellow
gcloud auth configure-docker "$Region-docker.pkg.dev" --quiet
docker build --platform linux/amd64 -t $ImageTag .
docker push $ImageTag
Write-Host "  ✓ Image pushed: $ImageTag" -ForegroundColor Green

# ── Deploy to Cloud Run ───────────────────────────────────────────────────────
Write-Host "`n[3/3] Deploying to Cloud Run..." -ForegroundColor Yellow

$ConnectorFull = "projects/$ProjectId/locations/$Region/connectors/$ConnectorName"

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
    --set-env-vars="REDIS_ADDR=$RedisAddr" `
    --vpc-connector=$ConnectorFull `
    --vpc-egress=private-ranges-only `
    --session-affinity

$ServiceUrl = gcloud run services describe $ServiceName `
    --region=$Region `
    --project=$ProjectId `
    --format="value(status.url)"

$WssUrl = $ServiceUrl -replace 'https', 'wss'

Write-Host ""
Write-Host "✅ Deploy complete!" -ForegroundColor Green
Write-Host "   URL      : $ServiceUrl" -ForegroundColor White
Write-Host "   WebSocket: $WssUrl/ws"  -ForegroundColor White
Write-Host "   Metrics  : Internal only — scrape via Cloud Monitoring" -ForegroundColor DarkGray

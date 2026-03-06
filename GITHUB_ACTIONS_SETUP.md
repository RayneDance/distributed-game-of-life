# GitHub Actions — One-Time GCP Setup

Run this **once** per project to wire GitHub Actions to GCP without storing any
service-account key files.

## What this creates

| Resource | Name |
|---|---|
| Workload Identity Pool | `github-pool` |
| OIDC Provider (in pool) | `github-provider` |
| Service Account | `deploy-sa` |
| IAM bindings | Cloud Run Admin, Artifact Registry Writer, Secret Manager Accessor |

---

## 1 — Run the setup script

```powershell
$Project  = gcloud config get-value project
$Region   = "us-central1"
$Repo     = "your-github-org-or-username/distributed-game-of-life"  # ← change this

# Enable required APIs
gcloud services enable iamcredentials.googleapis.com --project $Project

# Create the Workload Identity Pool
gcloud iam workload-identity-pools create "github-pool" `
    --location="global" `
    --display-name="GitHub Actions pool" `
    --project $Project

# Create the OIDC provider inside the pool
gcloud iam workload-identity-pools providers create-oidc "github-provider" `
    --location="global" `
    --workload-identity-pool="github-pool" `
    --display-name="GitHub provider" `
    --issuer-uri="https://token.actions.githubusercontent.com" `
    --attribute-mapping="google.subject=assertion.sub,attribute.repository=assertion.repository" `
    --attribute-condition="assertion.repository=='$Repo'" `
    --project $Project

# Create the deploy service account
gcloud iam service-accounts create "deploy-sa" `
    --display-name="GitHub Actions deploy SA" `
    --project $Project

$SA = "deploy-sa@${Project}.iam.gserviceaccount.com"

# Grant the SA the permissions it needs
foreach ($Role in @(
    "roles/run.admin",
    "roles/artifactregistry.writer",
    "roles/secretmanager.secretAccessor",
    "roles/iam.serviceAccountUser"
)) {
    gcloud projects add-iam-policy-binding $Project `
        --member="serviceAccount:$SA" `
        --role=$Role `
        --quiet
}

# Allow GitHub Actions (for this specific repo) to impersonate the SA
$PoolId = gcloud iam workload-identity-pools describe "github-pool" `
    --location="global" `
    --project $Project `
    --format="value(name)"

gcloud iam service-accounts add-iam-policy-binding $SA `
    --role="roles/iam.workloadIdentityUser" `
    --member="principalSet://iam.googleapis.com/${PoolId}/attribute.repository/${Repo}" `
    --project $Project

# Print the values you'll need for GitHub
$ProviderId = gcloud iam workload-identity-pools providers describe "github-provider" `
    --location="global" `
    --workload-identity-pool="github-pool" `
    --project $Project `
    --format="value(name)"

Write-Host ""
Write-Host "=== Copy these into GitHub repo → Settings → Secrets and variables → Variables ===" -ForegroundColor Green
Write-Host "GCP_PROJECT_ID      = $Project"    -ForegroundColor Cyan
Write-Host "WIF_PROVIDER        = $ProviderId" -ForegroundColor Cyan
Write-Host "WIF_SERVICE_ACCOUNT = $SA"         -ForegroundColor Cyan
```

---

## 2 — Add GitHub repo variables

In your repository go to **Settings → Secrets and variables → Variables → New repository variable** and add:

| Variable | Value |
|---|---|
| `GCP_PROJECT_ID` | your GCP project ID |
| `WIF_PROVIDER` | printed by the script above |
| `WIF_SERVICE_ACCOUNT` | `deploy-sa@PROJECT.iam.gserviceaccount.com` |

> **Variables, not Secrets.** These three values are resource identifiers, not
> credentials. Someone who obtained them still cannot authenticate to GCP —
> authentication requires the GitHub Actions OIDC token, which GCP will only
> accept from a workflow running inside **your specific repository** (enforced by
> the `attribute-condition` set in step 1). The security lives in that binding,
> not in keeping the identifiers hidden.
>
> By contrast, a service-account JSON key *is* a credential on its own, which is
> exactly why we use Workload Identity Federation instead — there is no long-lived
> secret to store or rotate.

---

## 3 — Create GitHub Environments

Go to **Settings → Environments** and create **two** environments:

| Environment | Maps to branch | Protection rules |
|---|---|---|
| `staging` | `staging` | None — deploys automatically on every push to `staging` |
| `prod` | `main` + semver tag | None — the tag itself is the gate (see below) |

There is no manual approval step on `prod`. The protection is structural:
you must explicitly create and push a semver tag (`v1.2.3`) that points to
a commit on `main`. Accidental deploys require deliberate tagging — that is
sufficient for this project. If you later want a human approval step, add
**Required reviewers** to the `prod` environment.

The three branches and their CI behaviour:

| Branch | CI triggered | Deploy |
|---|---|---|
| `development` | Lint only (`lint.yml`) | ❌ Never |
| `staging` | Lint + deploy (`deploy.yml`) | ✅ `golive-staging` on every push |
| `main` | Deploy on tag only (`deploy.yml`) | ✅ `golive` when a `v*.*.*` tag is pushed |

---

## 4 — Verify the workflow

1. Push any commit to `development` — the Actions tab should show a **Lint** run.
2. Merge `development` → `staging` (or push directly) — should show a **staging deploy**.
3. Merge `staging` → `main`, then tag: `git tag v0.1.0 && git push origin v0.1.0` — should show a **prod deploy**.

---

## Secret names expected in Cloud Secret Manager

| Environment | `REDIS_ADDR` secret | `REDIS_PASSWORD` secret |
|---|---|---|
| `prod` | `REDIS_ADDR` | `REDIS_PASSWORD` |
| `staging` | `REDIS_ADDR_STAGING` | `REDIS_PASSWORD_STAGING` |

These are read by `main.go` at startup based on the `ENVIRONMENT` env var
injected by the deploy workflow.

# Versioning

This project uses **[Semantic Versioning 2.0.0](https://semver.org/)** — `MAJOR.MINOR.PATCH`.

---

## Version number rules

| Segment | Increment when… | Example |
|---|---|---|
| **MAJOR** | A breaking change is introduced — e.g. WebSocket message format changes in a way that forces existing clients to update | `1.0.0` → `2.0.0` |
| **MINOR** | A new feature is added that is backward-compatible — e.g. a new shape in the catalog, a new WebSocket message type | `1.0.0` → `1.1.0` |
| **PATCH** | A backward-compatible bug fix or internal improvement — e.g. a simulation tick fix, a rate-limit tuning change | `1.0.0` → `1.0.1` |

**MAJOR resets MINOR and PATCH to 0. MINOR resets PATCH to 0.**

---

## Branch → environment model

```
development ──► (lint only, no deploy)
     │
     │  PR + review
     ▼
  staging ──────► (lint + deploy to golive-staging on every push)
     │
     │  PR + review
     ▼
   main
     │
     │  git tag v{MAJOR}.{MINOR}.{PATCH}
     ▼
  PRODUCTION  ──► (deploy to golive, requires GitHub Environment approval)
```

A commit only reaches production if it has been through both staging and a
tagged release. No direct pushes to `main` without a PR.

---

## How to cut a release

```powershell
# 1. Merge staging → main via a PR on GitHub.

# 2. Pull the latest main locally.
git checkout main
git pull

# 3. Tag the release.  Use the next available version.
git tag v1.2.0

# 4. Push the tag.  This is what triggers the prod deploy workflow.
git push origin v1.2.0
```

GitHub Actions will:
1. Run lint against the tagged commit.
2. Pause at the **prod** GitHub Environment gate — approvers are notified.
3. After approval, build and deploy the image tagged `v1.2.0` to Cloud Run.

---

## Pre-release versions

For release candidates or beta builds, use the pre-release suffix:

```
v1.2.0-rc.1
v1.2.0-beta.2
```

The deploy workflow regex (`v[0-9]+.[0-9]+.[0-9]+`) **does not match** pre-release
suffixes intentionally — they will not trigger a prod deploy. Use them freely on
the `staging` branch for longer-lived testing cycles.

---

## Where versions appear

| Location | Format | Set by |
|---|---|---|
| Git tag | `v1.2.0` | Developer (see above) |
| Docker image in Artifact Registry | `golive:v1.2.0` | CI on prod deploy |
| Cloud Run revision | auto-named by GCP | Cloud Run |

There is currently no in-app version endpoint — add one if observability requires it.

---

## FAQ

**Do I need to tag for every staging deploy?**
No. Staging deploys happen automatically on every push to the `staging` branch,
unversioned (tagged with the git SHA). Only prod requires a version tag.

**Can I re-use a tag?**
Never. Tags are immutable references to a specific commit. If you need to fix a
bad release, increment the PATCH version and tag again (e.g. `v1.2.1`).

**What if I need to hotfix prod without going through staging?**
Create a `hotfix/*` branch off `main`, fix, PR directly to `main`, then tag.
For non-trivial hotfixes strongly prefer the full `development → staging → main`
flow even if it is faster than you'd like.

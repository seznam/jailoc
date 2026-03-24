# Image Resolution Reference

During `jailoc up`, the container image is resolved in two independent tiers. Tier 1 produces the base image. Tier 2 optionally builds a workspace-specific layer on top of it.

---

## Tier 1: Base Image

Three steps are evaluated in priority order. The first step that applies wins; later steps are skipped.

```
jailoc up
    │
    ▼
[1] image.dockerfile set?
    ├── yes → load (local path or HTTP URL), build → tag jailoc-base:preset-<hash>  ← STOP (failure is fatal)
    └── no
         │
         ▼
    [2] image.repository set?
         ├── yes → pull {repository}:{version} from registry  ← STOP
         │         (failure is fatal)
         └── no
              │
              ▼
         [3] build from embedded Dockerfile → tag jailoc-base:embedded  ← STOP
```

---

## Tier 2: Workspace Overlay

After tier 1 resolves a base image, tier 2 checks whether the workspace has its own Dockerfile configured.

```
base image (from tier 1)
    │
    ▼
workspace.dockerfile set?
    ├── yes → load (local path or HTTP URL), build with --build-arg BASE=<base>
    │         tag jailoc-{name}:<hash>  ← result used as final image
    └── no  → use base image as-is
```

Tier 2 is completely independent of which tier-1 step resolved the base. It always runs after tier 1 completes.

---

## Step Details

### Tier 1, Step 1: Dockerfile (base image)

**Trigger:** `dockerfile` is set in `[image]`.

**Behavior:**
- Loads the Dockerfile from the specified source (local path or HTTP URL).
- Builds a local image from the loaded content.
- Tags the result as `jailoc-base:preset-<content-hash>`, where `<content-hash>` is the first 8 characters of the SHA-256 hash of the Dockerfile content.

**Constraints:**
- Accepted sources: absolute local paths (`/...`), tilde paths (`~/...`), HTTP(S) URLs.
- Maximum download size for HTTP sources: 1 MiB. Files exceeding this limit cause a fatal error.
- Load or build failure is fatal. There is no fallback to subsequent steps.
- See [Configuration Reference](configuration.md#dockerfile-fields) for accepted formats and validation rules.

---

### Tier 1, Step 2: Registry Pull

**Trigger:** `dockerfile` is not set in `[image]`, and `repository` is set.

**Behavior:**
- Pulls `{repository}:{version}` from the registry.
- `{version}` is the jailoc binary version at runtime.
- Pull failure is fatal. There is no fallback to step 3 if a repository is explicitly configured.

---

### Tier 1, Step 3: Embedded Fallback

**Trigger:** Neither `dockerfile` nor `repository` is set in `[image]`.

**Behavior:**
- Builds an image from the Dockerfile embedded in the jailoc binary at compile time.
- Tags the result as `jailoc-base:embedded`.

This step always succeeds (assuming a functional Docker daemon), as the Dockerfile is bundled with the binary.

---

### Tier 2: Workspace Overlay

**Trigger:** `dockerfile` is set in `[workspaces.<name>]`.

**Behavior:**
- Loads the Dockerfile from the specified source (local path or HTTP URL).
- Builds an additional image layer on top of the base resolved in tier 1.
- The base image tag is passed via the `BASE` build argument.
- The workspace Dockerfile must begin with:

  ```dockerfile
  ARG BASE
  FROM ${BASE}
  ```

- Tags the result as `jailoc-{name}:<content-hash>`, where `<content-hash>` is the first 8 characters of the SHA-256 hash of the Dockerfile content.

**Build context:**
- If `build_context` is set in the workspace, that directory is used.
- If `build_context` is empty and `dockerfile` is a local path, the parent directory of the Dockerfile is used.
- If `build_context` is empty and `dockerfile` is an HTTP URL, a temporary directory is used.

---

## Image Tag Summary

| Source | Tag |
|--------|-----|
| `[image].dockerfile` (local or HTTP) | `jailoc-base:preset-<content-hash>` |
| `[image].repository` (registry pull) | `{repository}:{version}` (unchanged) |
| Embedded fallback | `jailoc-base:embedded` |
| Workspace overlay | `jailoc-{name}:<content-hash>` |

---

See the [custom images how-to](../how-to/custom-images.md) for practical instructions on each resolution step.

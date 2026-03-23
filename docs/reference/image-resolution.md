# Image Resolution Reference

During `jailoc up`, the container image is resolved through a five-step cascade. Steps are evaluated in priority order. Each step either produces a resolved image or falls through to the next. Step 5 (workspace layer) always executes on top of whichever base image was resolved in steps 1 through 4.

---

## Resolution Cascade

```
jailoc up
    │
    ▼
[1] dockerfile set (workspace or global)?
    ├── yes → download URL, build → tag jailoc-base:preset-<hash>  ← STOP (failure is fatal)
    └── no
         │
         ▼
    [2] ~/.config/jailoc/Dockerfile exists?
         ├── yes → build → tag jailoc-base:local  ← STOP
         └── no
              │
              ▼
         [3] pull {repository}:{version} from registry
              ├── success → use pulled image  ← STOP
              └── failure
                   │
                   ▼
              [4] build from embedded Dockerfile → tag jailoc-base:embedded  ← STOP
                   │
                   ▼
              [5] ~/.config/jailoc/{name}.Dockerfile exists?
                   ├── yes → build on top of base → tag jailoc-{name}:latest
                   └── no  → use resolved base as-is
```

---

## Step Details

### Step 1: Remote Dockerfile URL (preset)

**Trigger:** `dockerfile` field is set in `[workspaces.<name>]` or `[image]`. Workspace-level takes priority over the global `[image].dockerfile`.

**Behavior:**
- Downloads the Dockerfile from the specified HTTP(S) URL.
- Builds a local image using the downloaded Dockerfile.
- Tags the result as `jailoc-base:preset-<content-hash>`, where `<content-hash>` is derived from the Dockerfile content.

**Constraints:**
- Maximum download size: 1 MiB. Files exceeding this limit cause a fatal error.
- Download failure is fatal. There is no fallback to subsequent steps.
- URL must have an `http` or `https` scheme and a non-empty host. See [Configuration Reference](configuration.md#dockerfile-url-fields) for URL validation rules.

---

### Step 2: Local Dockerfile Override

**Trigger:** File `~/.config/jailoc/Dockerfile` exists on the host.

**Behavior:**
- Builds a local image using that Dockerfile.
- Tags the result as `jailoc-base:local`.
- Replaces the entire base image. Registry pull (step 3) is skipped.

---

### Step 3: Registry Pull

**Trigger:** Neither step 1 nor step 2 resolved an image.

**Behavior:**
- Pulls `{repository}:{version}` from the registry configured in `[image].repository`.
- `{version}` is the jailoc binary version at runtime.

**On failure:** Falls through to step 4. Registry pull failure is not fatal.

---

### Step 4: Embedded Fallback

**Trigger:** Registry pull (step 3) failed.

**Behavior:**
- Builds an image from the Dockerfile embedded in the jailoc binary at compile time.
- Tags the result as `jailoc-base:embedded`.

This step always succeeds (assuming a functional Docker daemon), as the Dockerfile is bundled with the binary.

---

### Step 5: Workspace Layer

**Trigger:** File `~/.config/jailoc/{name}.Dockerfile` exists on the host, where `{name}` is the workspace name.

**Behavior:**
- Builds an additional layer on top of the base image resolved in steps 1 through 4.
- The base image reference is passed via the `BASE` build argument.
- The workspace Dockerfile must begin with:

  ```dockerfile
  ARG BASE
  FROM ${BASE}
  ```

- Tags the result as `jailoc-{name}:latest`.

This step is independent of which step resolved the base. It always runs last, on top of the resolved base.

---

## Image Tag Summary

| Source | Tag |
|--------|-----|
| Remote Dockerfile URL | `jailoc-base:preset-<content-hash>` |
| Local `~/.config/jailoc/Dockerfile` | `jailoc-base:local` |
| Registry pull | `{repository}:{version}` (unchanged) |
| Embedded fallback | `jailoc-base:embedded` |
| Workspace layer | `jailoc-{name}:latest` |

---

See the [custom images how-to](../how-to/custom-images.md) for practical instructions on using each resolution step.

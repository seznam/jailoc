# Image Resolution Reference

During `jailoc up` and `jailoc restart`, jailoc resolves the workspace image with a Docker daemon, then converts its layers into read-only EROFS images in the VM image cache, which the workspace VM mounts as its root file system.

---

## Build location

| `[vm] builder` | `isolated_build` | Docker daemon used |
|---|---|---|
| any | `true` | Builder VM of the workspace (`~/.cache/jailoc-vm/workspaces/<workspace>/builder`), with its own build cache and the workspace's network policy |
| `"auto"` or empty | `false` | Host daemon when it answers, is Docker Engine 25 or later, and runs Linux on the host's CPU architecture, otherwise the shared builder VM |
| `"docker"` | `false` | Host daemon; `jailoc up` fails when it is unusable |
| `"vm"` | `false` | Shared builder VM (`~/.cache/jailoc-vm/builder`) |

With an external [`ca_bundle`](configuration.md#image-builds-with-a-ca-bundle) selected, the host daemon is never used: `"auto"` with `isolated_build = false` builds in the shared builder VM, `isolated_build = true` keeps the workspace's builder VM, and `"docker"` is rejected before the build starts, also with `isolated_build = true`. A builder VM that can't start fails the build, with no fallback to the host daemon. With `ca_bundle = false`, or when automatic discovery selects no file, the table applies unchanged.

`jailoc down --purge` deletes the workspace's builder VM and its build cache.

The data disk of the shared builder VM is sized by [`[vm] builder_disk`](configuration.md#vm) (default `20g`). The data disk of an `isolated_build` builder VM is sized by the workspace's `docker_disk`.

- jailoc builds with BuildKit when the daemon reports it as its default builder, and with the classic builder otherwise. A build with a forwarded `ca_bundle` requires BuildKit and the standard Dockerfile frontend, and fails with an error on a classic builder or a custom frontend.
- With BuildKit, the embedded Dockerfile, the agent layer and the Docker layer keep apt and npm downloads in BuildKit cache mounts, which survive image pruning.
- The classic builder gets the same Dockerfiles without the cache mounts.
- Preset and overlay Dockerfiles are built as written.

---

## Resolution Cascade

The first step that applies wins; later steps are skipped.

| Step | Trigger | Result |
|------|---------|--------|
| [1](#step-1-workspace-image) | `image` in `[workspaces.<name>]` | Used as is |
| [2](#step-2-defaults-image-with-workspace-overlay) | `[defaults] image` and workspace `dockerfile` | Built: `jailoc-<workspace>:<content-hash>` |
| [3](#step-3-defaults-image) | `[defaults] image`, no workspace `dockerfile` | Used as is |
| [4](#step-4-base-dockerfile) | `[base] dockerfile`, no workspace or default `image` | Built: `jailoc-base:preset-<content-hash>` |
| [5](#step-5-embedded-dockerfile) | none of the above | Built: `jailoc-base:embedded` |

Steps 2, 4 and 5 add the Docker layer, agent layer and workspace overlay below; their tags are in [Image Tag Summary](#image-tag-summary).

![Image resolution cascade: 1, a workspace image is used as is; 2, defaults.image with a workspace Dockerfile is built into jailoc-<workspace>:<hash> with the Docker layer, agent layer and workspace Dockerfile; 3, defaults.image is used as is; 4, base.dockerfile is built into jailoc-base:preset-<hash> with the Docker and agent layers; 5, otherwise jailoc-base:embedded is built with the Docker and agent layers. Every result is pulled if missing, exported with docker save and converted into EROFS layer images in the VM image cache.](img/image-resolution.svg)

### Step 1: Workspace image

**Trigger:** `image` is set in `[workspaces.<name>]`.

- No build runs.
- `dockerfile` and `build_context` are not consulted; combining them with `image` is a validation error.

### Step 2: Defaults image with workspace overlay

**Trigger:** `[defaults] image` and workspace `dockerfile` are both set.

- The [Docker layer](#docker-layer-steps-2-4-and-5) and the [agent layer](#agent-layer-steps-2-4-and-5) are built on top of `defaults.image`, then the workspace Dockerfile on top of that, passed as the `BASE` build argument.
- Build failure is fatal.

The workspace Dockerfile must start with:

```dockerfile
ARG BASE
FROM ${BASE}
```

### Step 3: Defaults image

**Trigger:** `[defaults] image` is set and the workspace has no `dockerfile`.

No build runs.

### Step 4: Base Dockerfile

**Trigger:** `[base] dockerfile` is set and no workspace or default `image` is set.

- The Dockerfile is loaded from a local path or HTTP(S) URL (maximum 1 MiB) and built.
- Load or build failure is fatal; there is no fallback to the embedded image.

### Step 5: Embedded Dockerfile

**Trigger:** none of the above apply.

- The Dockerfile embedded in the jailoc binary is built as `jailoc-base:embedded`, unless that image was already built from the same Dockerfile and build arguments.
- A changed embedded Dockerfile or changed build arguments rebuild the image automatically.
- Updates of the mutable `ubuntu:26.04` tag reach the image only after `docker rmi jailoc-base:embedded`.
- A running VM adopts a rebuilt image only after `jailoc restart`; `jailoc up` on a running VM reloads it without changing its image.
- See [Default Image](default-image.md).

### Docker layer (steps 2, 4 and 5)

Added when `enable_docker` is on:

- Docker CE, the buildx and compose plugins and `nftables` are added in a layer on top of the base image, tagged `jailoc-docker:<content-hash>`.
- The binaries are pinned and checksum-verified in the embedded `docker.Dockerfile` and copied to `/usr/local/bin` and `/usr/local/lib/docker/cli-plugins`.
- `nftables` is installed with `apt-get` unless `nft` is on `PATH`.

Skipped when `enable_docker = false` or `protect_git = true`. Images used directly (steps 1 and 3) get no Docker layer and must contain `dockerd` for Docker in the VM.

### Agent layer (steps 2, 4 and 5)

- The workspace's [coding agent](configuration.md#coding-agents) is installed with `npm install -g` of its pinned package (Antigravity CLI: its checksum-verified GitHub release binary).
- The layer sits on top of the Docker layer (or the base image when it is skipped) and is tagged `jailoc-agent-<agent>:<content-hash>`.
- The base image needs `npm` on `PATH` (Antigravity CLI: `curl`, `tar` and `sha256sum`).

Skipped with `agents.<agent>.install = false`. Images used directly (steps 1 and 3) get no agent layer and must contain the agent binary.

### Workspace overlay (steps 4 and 5)

After step 4 or 5, a workspace `dockerfile` is built on top of the agent layer (or the base image when it is skipped) with `BASE` set to it.

---

## Build details

- `<content-hash>` is derived from the SHA-256 of the Dockerfile content: 8 hex characters for workspace overlays, 16 for `[base] dockerfile` presets.
- The Docker and agent layer hashes also cover the base image ID, and a layer whose tag already exists is not built again.
- Host proxy variables (`HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY`, `FTP_PROXY`, `ALL_PROXY` and lowercase variants) are passed as build arguments to every build.
- With an external `ca_bundle` selected, every image jailoc builds (base, Docker and agent layers, workspace overlay) and every stage of your Dockerfiles gets the bundle's trust; see [Image builds with a CA bundle](configuration.md#image-builds-with-a-ca-bundle). Images used as is (steps 1 and 3) are not built. The bundle's digest is part of the tags of the images jailoc builds, of jailoc's build decisions and of the build's cache keys, so a changed bundle, including one edited in place, or `ca_bundle = false` rebuilds the affected images.
- Overlay build context: `build_context` when set; otherwise the Dockerfile's parent directory for a local path, or an empty temporary directory for a URL.
- Images from steps 1 and 3 are pulled for `linux/<host architecture>` when the daemon does not have them.
- The generated overlay Dockerfile is written only into the context archive sent to Docker, never into the build context directory.

### Build inputs and read-write shares

Build inputs are the local host paths an image build reads:

- the local `[base] dockerfile`, only when the base cascade is used (no workspace `image` and no `defaults.image`);
- the local workspace `dockerfile`;
- the whole `build_context` of the overlay build. Without `build_context`, a local Dockerfile's parent directory is the context; a URL Dockerfile without `build_context` reads nothing local.

A workspace `image`, or `defaults.image` without a workspace `dockerfile`, builds nothing and has no build inputs. `build_context` without a `dockerfile` is unused. `.dockerignore` does not exempt anything: jailoc sends the whole context. A context with more than 500,000 entries is refused; set a smaller `build_context`.

`jailoc up` of a stopped workspace, `jailoc restart` and the auto-start of `jailoc` refuse to start the workspace, before building or booting anything, when a read-write share contains a build input, equals it, lies inside a build context, or the input's path runs through a symbolic link inside the share or through a directory inside it that a later `..` leaves, which the guest could replace with a link. Read-write shares are the workspace `paths`, read-write `mounts`, profile state shares, the VM's guest data directory and the root file system it boots from, of the workspace itself, of every workspace VM that runs or another jailoc process is starting, and of a VM process the workspace left from an earlier start until the new start stops it. A Dockerfile, or any regular file in a build context, with other hard links on the device of a read-write share is refused too, because its other names could be in the share.

Paths are resolved the way the build opens them: symbolic links are followed before a later `..`, a dangling link counts by its target, and a missing input counts by where it would be. A path that goes up with `..` from a component that does not exist is refused. Directories are also compared by identity, so another spelling of a share on a case-insensitive file system, or a bind mount of the share itself, counts as the same directory. A host bind mount of a directory inside a share, and a file system mounted inside a share, are not detected; jailoc relies on the host's mount layout, which the guest cannot change.

A workspace whose read-write shares expose the build inputs of any other configured workspace, running or not, is refused too: its guest could change them before that workspace's next build. The build inputs of a workspace that runs or is starting count as well, also when another configuration file defines it. A read-write share whose own path runs through a symbolic link, or a directory left by `..`, inside a read-write share of another running or starting workspace is refused in both start orders: that guest could repoint the path after the check. A second start of the same workspace while one is in progress fails, and so does `jailoc down --purge` of it, or of a workspace whose VM processes still run.

Read-only shares (`mounts` with `:ro`) of build inputs are allowed. jailoc never converts a share to read-only, and there is no opt-out.

The error lists every conflict and ends with the fix:

```text
workspace "p": Dockerfile /home/u/p/Dockerfile of workspace "p" lies in the read-write share /home/u/p of workspace "p"
keep build inputs out of read-write shares: share directories without them, mount them read-only, or use a prebuilt image
```

When a running workspace is involved, the error adds that you stop it with `jailoc down <workspace>`.

`jailoc reload` and `jailoc up` on a running workspace never rebuild and cannot change shares. When the running VM's read-write shares expose any configured workspace's build inputs, they print a `WARNING`, and the affected builds are refused until the shares change and the workspace is restarted. Removing the share does not undo changes the guest already made: check the exposed files before the next build.

See [Keep build inputs out of read-write shares](../how-to/custom-images.md#keep-build-inputs-out-of-read-write-shares) for layouts.

## Converting for the VM

jailoc exports the final image with `docker save` and converts each layer it does not have yet into one read-only EROFS image file.

### Cache layout

| Item | Location or content |
|------|---------------------|
| Layer image | `~/.cache/jailoc-vm/images/erofs/layers/<DiffID>.erofs`, one file per layer |
| Workspace image | Directory `~/.cache/jailoc-vm/images/erofs/images/<image ID>/` (reference `jailoc.local/erofs:<image ID>`) |
| Image directory content | Hard links `000.erofs`, `001.erofs`, … to the image's layers, bottom layer first, and an `image.json` with the layer order and the image's `ENV` |

- No host directory tree is extracted.
- An image ID already in the cache is not converted again.
- Layers shared by images are stored once.
- A rebuild that changes only the top layer converts only that layer.

### Preserved metadata

The layer images keep these on Linux and macOS hosts alike:

- The image's own numeric owners.
- Modes (setuid, setgid, sticky).
- Extended attributes (including file capabilities).
- Hard links and device nodes.

File names stay case-sensitive on case-insensitive macOS volumes.

### Whiteouts, opaque directories and placeholders

- OCI whiteouts become overlayfs whiteouts (0/0 character devices) and opaque directories (`trusted.overlay.opaque`), so deletions in upper layers hide lower content as in Docker.
- overlayfs ignores an opaque mark on a layer's root. When a layer's root is opaque, the image directory starts with that layer and leaves out the layers below it.
- A layer can add entries to a directory without an entry for the directory itself. The layer image then has a placeholder directory, owned by root with mode 0755, that would cover the lower layer's directory. jailoc marks such placeholders, and for an image that has them it adds one more file to the image directory: a layer of only directories, mounted above the others, that gives each such directory its owner, mode, time stamp and extended attributes from the layers below.

### Entries left out or rewritten

- AUFS hard link targets (`.wh..wh.plnk`) and other AUFS metadata entries are left out, as in Docker; hard links to the targets keep their data.
- PAX global headers are skipped.
- Old GNU sparse files are stored with their holes filled in.

### Import limits

An import converts at most:

- 30 GiB of file data, counting sparse files at their full size.
- One million entries per layer.

Larger images fail to import.

### Cache pruning

After every import, jailoc removes:

- Workspace image directories of its own cache (under the current home directory) that no workspace uses any more and that are older than one hour.
- Layer files that no remaining image links to.

The builder VM image stays in go-microvm's directory image cache; only workspace VMs use EROFS images.

### Progress output and size warning

- `jailoc up` prints `Converting image <name> for the workspace VM...`, `Converting image layers (n/m)…` and `Linking image layers…` while it works.
- Before it converts an image, it warns when the workspace Dockerfile adds more than 20 layers or more than 5 GiB, as Docker reports the sizes, since the conversion takes a while and each layer slows down file lookups in the VM. Fewer `RUN` steps or a multi-stage build help.
- The base image and the Docker and agent layers jailoc adds do not count. An image used as is (`image`, or `defaults.image` without a workspace `dockerfile`) counts in full.

### Docker Engine requirement

`docker save` must write an OCI image layout, which requires Docker Engine 25 or later. With an older host daemon, `"auto"` builds in the shared builder VM and `"docker"` fails.

### Image labels and unused images

- jailoc labels the images it builds with `com.github.seznam.jailoc`.
- It never prunes the host Docker daemon, because a daemon can be shared with other users and other jailoc home directories. Unused images stay until their owner removes them.
- List them with `docker images --filter label=com.github.seznam.jailoc` and remove chosen ones with `docker image rm <image>`.

### Builder cache maintenance

jailoc maintains the cache of the builder VMs it owns, the shared builder VM and the `isolated_build` builder VMs, and only those.

- Maintenance runs under the builder's exclusive lock, before and after each build (also after a failed one), so it never overlaps a build on that builder. A maintenance failure only warns; it doesn't fail the build.
- Inside the builder VM, Docker 29 garbage collection works best effort towards a build cache of at most 50% of the disk with at least 20% of the disk free. The explicit prune through the Docker SDK uses the same targets, before and after the build. They are targets, not a hard quota.
- Only images that jailoc built (selected by the `com.github.seznam.jailoc` label) are removal candidates, and they are removed without force. These are kept:
    - images whose ID is converted into the VM image cache,
    - the base and agent ancestors of a kept image,
    - images created less than one hour ago,
    - images used by a container,
    - images with a foreign tag, one outside the `jailoc-*` repositories.
- The active build cache, the layers of the images being built and a single large build can exceed the targets.

A builder disk that is already full can't be pruned, because the Docker daemon can't run. See [Builder VM disk is full](../how-to/troubleshooting.md#builder-vm-disk-is-full).

### Ignored image settings

The image's `ENTRYPOINT`, `CMD`, `USER`, `WORKDIR` and `EXPOSE` are not used; see [Overlay Compatibility](overlay-compatibility.md).

---

## Image Tag Summary

| Source | Tag |
|--------|-----|
| Workspace `image` | unchanged |
| `defaults.image` with workspace overlay | `jailoc-<workspace>:<content-hash>` |
| `defaults.image` | unchanged |
| `[base] dockerfile` | `jailoc-base:preset-<content-hash>` |
| Embedded Dockerfile | `jailoc-base:embedded` |
| Docker layer on step 2, 4 or 5 | `jailoc-docker:<content-hash>` |
| Agent layer on step 2, 4 or 5 | `jailoc-agent-<agent>:<content-hash>` |
| Workspace overlay on step 4 or 5 | `jailoc-<workspace>:<content-hash>` |

With an external `ca_bundle` selected, the tags of the images jailoc builds also depend on the bundle's digest; the exact form is internal.

---

See the [custom images how-to](../how-to/custom-images.md) for practical instructions.

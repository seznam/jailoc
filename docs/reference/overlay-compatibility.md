# Overlay Compatibility

jailoc builds the workspace image with Docker and converts its layers into read-only EROFS images that the VM mounts as its root file system. This page lists which image settings carry over into the workspace VM and which overlay changes break it.

## Image settings in the VM

| Instruction | Used in the VM | Notes |
|-------------|----------------|-------|
| `ENV` | Yes | Passed to jailoc-guest and inherited by every session; config `env` and secrets are added on top |
| `RUN`, `COPY`, `ADD` | Yes | The resulting file system is the VM's root file system |
| `LABEL` | No | Metadata only |
| `ENTRYPOINT`, `CMD` | No | jailoc-guest is the VM's init; sessions run the coding agent over SSH |
| `USER` | No | Sessions run as UID 1000 |
| `WORKDIR` | No | The working directory is the first workspace path, or the target directory of a session |
| `EXPOSE` | No | Publish guest ports with `ports` in the configuration |
| `VOLUME` | No | Persistent state lives in the VM's persistent home; see [configuration](configuration.md#persistent-state) |
| `HEALTHCHECK`, `STOPSIGNAL`, `SHELL` | No | |

## User account

jailoc-guest looks for the user with UID 1000 in the image's `/etc/passwd` and uses its name, group, home directory and shell. When the image has no such user, jailoc-guest adds `agent` (UID and GID 1000, home `/home/agent`, shell `/bin/bash` if present, otherwise `/bin/sh`).

The image keeps its own numeric owners, modes and extended attributes on every host: `/etc/shadow` keeps its `shadow` group, `sudo` stays setuid root, and files the image gives UID 1000 belong to the agent. The agent cannot create files outside its home, the shares, `/tmp` and `/var/tmp`: use `sudo` or bake changes into the image.

If the guest kernel cannot idmap (possible only with a custom `[vm] runner` or `lib_dir` runtime), the agent runs as the host user's UID and GID, and jailoc-guest idmaps the image layers so that the agent's files in the image appear as the host user's. `sudo` does not grant root in that case.

## Incompatible changes

| Action | Severity | Effect |
|--------|----------|--------|
| Remove the agent binary (for example `opencode`) from `PATH` | Fatal | Sessions fail to start with an error naming the binary |
| Override `ENV PATH` without `/home/agent/.local/bin` | Breaking | Tools installed by the agent under `~/.local/bin` are not found |
| Remove `dockerd` from an image used directly with `enable_docker = true` | Fatal | `jailoc up` fails |
| Remove `nftables` while `enable_docker` is on | Breaking | `dockerd` cannot set up container networking |
| Use the `docker` CLI in a `RUN` step with `enable_docker = false` | Fatal | The build fails: Docker is installed only for workspaces with `enable_docker` on |
| Remove `ca-certificates` | Breaking | TLS fails, and a forwarded `ca_bundle` has no trust store to join |
| Remove `sudo` | Degraded | The agent cannot install packages at runtime |
| Remove `gpg` while `gpg_agent` is on | Degraded | The public keys are not imported and GPG signing is unavailable |
| Build for a CPU architecture other than the host's | Fatal | The VM cannot run the image |

**Severity definitions:**

| Severity | Meaning |
|----------|---------|
| Fatal | The VM or the session does not start |
| Breaking | The VM starts but core functionality is unavailable |
| Degraded | The VM starts; partial functionality is lost |

## Related pages

- [Custom images](../how-to/custom-images.md): build and configure overlay Dockerfiles
- [Image resolution](image-resolution.md): how base and overlay images are resolved
- [Architecture](../explanation/architecture.md): VM boot, guest init and sessions

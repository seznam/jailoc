# Run a workspace in a VM

!!! warning "Experimental"
    `backend = "vm"` runs on macOS (Apple Silicon) with libkrun. Linux support exists in the code but is not tested. Configuration and behaviour can change between releases.

With `backend = "vm"`, each workspace runs in its own lightweight virtual machine instead of on the host Docker daemon. The VM is the security boundary: the agent container, its Docker builds and any containers it starts all run inside the VM, and all network traffic leaves through a jailoc-owned network broker on the host. No host Docker installation is needed. See [VM Backend](../explanation/vm-backend.md) for the design.

## VM runtime

On macOS (Apple Silicon), jailoc needs nothing besides itself. The first `jailoc up` of a VM workspace downloads the pinned VM runtime (about 25 MB: the `go-microvm-runner` process that hosts the VM, libkrun, and the libkrunfw guest kernel), verifies its SHA-256 checksums and unpacks it into `~/.cache/jailoc-vm/runtime/`. Later starts reuse it.

To use your own runner and libraries instead, for example on Linux where no runtime is pinned, set them in [`[vm]`](../reference/configuration.md#vm):

```toml
[vm]
runner = "~/.local/bin/go-microvm-runner"
lib_dir = "/opt/homebrew/lib"
```

On Linux, the user running jailoc also needs read and write access to `/dev/kvm`.

## Switch a workspace to the VM backend

Set `backend` on the workspace, or in `[defaults]` for all workspaces:

```toml
[workspaces.myproject]
backend = "vm"
paths = ["~/src/myproject"]
cpu = 2.0
memory = "4g"
```

Start it as usual:

```bash
jailoc up myproject
jailoc
```

The first start pulls the VM image and the workspace image and builds overlays inside the VM. Later starts reuse the VM's Docker data disk and take a few seconds.

`cpu` and `memory` keep limiting the agent container. The VM gets `cpu` rounded up plus one vCPU, and `memory` plus 1 GiB, for its own Docker daemon.

## Use Docker from the agent

With `enable_docker = true` (the default), the agent container talks to the Docker daemon inside the VM through `/var/run/docker.sock`. No Docker-in-Docker sidecar runs. Images, volumes and build cache live on the VM's data disk and persist across `jailoc down` and `jailoc up`.

## Stop and remove the VM

```bash
jailoc down myproject           # stop the container and the VM, keep the data disk
jailoc down myproject --purge   # also delete the VM state and data disk
```

## Allow private hosts and networks

`allowed_hosts` and `allowed_networks` become rules of the network broker:

- `allowed_networks` entries open the CIDR for all ports. IPv6 entries are ignored, because the VM has no IPv6 connectivity.
- `allowed_hosts` names are resolved on the host when the VM starts. Their IPv4 addresses are allowed for all ports. Restart the workspace after the addresses change.

Host addresses, the broker gateway and cloud metadata endpoints stay blocked regardless of these settings.

## Restrictions

- `ssh_auth_sock` and `filtered_dns` are rejected: the SSH agent socket cannot cross into the VM, and DNS goes through the broker.
- `mounts` must name directories. A file mount is rejected at `jailoc up`; mount its parent directory instead.
- File secrets (`secrets.file`) are read on the host at `jailoc up` and passed to the container in memory.
- The agent container runs without its own iptables firewall; the VM boundary and the broker enforce network policy.
- On macOS, reaching hosts on the local network through `allowed_hosts` or `allowed_networks` needs the macOS network permission for the process that runs jailoc.

## Inspect a running VM

VM state lives in `~/.cache/jailoc-vm/workspaces/<workspace>/`:

| File | Content |
|------|---------|
| `supervisor.log` | VM supervisor output: broker and VM start, readiness, shutdown reason |
| `microvm/console.log` | Guest console: boot, Docker daemon start and shutdown |
| `broker.jsonl` | Network broker decisions, one JSON object per line |
| `state.json` | Supervisor PID and readiness |
| `docker.img` | Docker data disk |

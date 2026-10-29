---
status: accepted
---

# Agent state lives in profiles, used by one running VM at a time

An agent's state (logins, sessions, history) stays per workspace by default. An agent definition or a workspace can name a profile instead: a host directory shared by every workspace that uses it. Only one running VM may use a profile at a time, and `up` fails while another workspace holds it.

The lock exists because each workspace runs in its own VM with its own kernel. Processes sharing a directory on one kernel see a coherent page cache, POSIX locks and SQLite's memory-mapped WAL index. Two VMs writing one host directory over virtio-fs get none of that, and a harness keeping a database (OpenCode's session store) can corrupt it. Sharing sequentially is safe, sharing concurrently is not, so jailoc allows the first and refuses the second.

## Considered options

- **Shared state by default (per agent across workspaces).** Rejected: it exposes users who configure nothing to concurrent writes from several VMs.
- **Allow concurrent use and warn.** Rejected: jailoc can detect concurrent use but not the corruption, and a warning on every `up` is easy to ignore.
- **Allow a profile in only one workspace (config validation).** Rejected: it makes a profile no more than a slot inside one workspace and drops the main use case, one agent definition whose login and sessions several workspaces reuse.
- **Share only the auth files.** Rejected: which files hold auth differs per harness and version, and jailoc treats the harness's state directories as opaque.

## Consequences

- Two workspaces on the same profile cannot run at once. Use another profile, or none, to run them in parallel.
- Sharing a profile between versions of a harness (e.g. `opencode` and an older `opencode-legacy`) is allowed but can expose the older version to a newer on-disk format.
- Profiles are owned by no workspace, so `down --purge` does not delete them.
- A profile is a channel between the workspaces that use it: harness hooks, plugins and MCP settings, the login and resumable sessions travel through it. jailoc does not split executable configuration from data, because which files are which differs per harness and version, and several harnesses keep both in one file. Workspaces sharing a profile share one trust level, and the documentation says so.

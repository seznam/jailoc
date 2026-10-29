---
status: accepted
---

# Agent state lives in profiles, used by one running VM at a time

An agent's state (logins, sessions, history) stays per workspace by default, as in jailoc v1, where each workspace had its own `opencode-data-<workspace>` volume. An agent definition or a workspace can name a profile instead: a host directory shared by every workspace that uses it. Only one running VM may use a profile at a time, and `up` fails while another workspace holds it.

The lock exists because v2 runs each workspace in its own VM with its own kernel. In v1, containers sharing a bind mount ran on one kernel, so page cache, POSIX locks and SQLite's memory-mapped WAL index were coherent. Two VMs writing one host directory over virtio-fs get none of that, and a harness keeping a database (OpenCode's session store) can corrupt it. Sharing sequentially is safe, sharing concurrently is not, so jailoc allows the first and refuses the second.

## Considered options

- **Shared state by default (per agent across workspaces).** Rejected: it exposes users who configure nothing to concurrent writes from several VMs, which v1 never did.
- **Allow concurrent use and warn.** Rejected: jailoc can detect concurrent use but not the corruption, and a warning on every `up` is easy to ignore.
- **Allow a profile in only one workspace (config validation).** Rejected: it makes a profile no more than a slot inside one workspace and drops the main use case, one agent definition whose login and sessions several workspaces reuse.
- **Share only the auth files.** Rejected: which files hold auth differs per harness and version, and jailoc treats the harness's state directories as opaque.

## Consequences

- Two workspaces on the same profile cannot run at once. Use another profile, or none, to run them in parallel.
- Sharing a profile between versions of a harness (e.g. `opencode` and an older `opencode-legacy`) is allowed but can expose the older version to a newer on-disk format.
- Profiles are owned by no workspace, so `down --purge` does not delete them.

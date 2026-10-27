# Secrets Architecture

A workspace often needs credentials: an API token for the model provider, a registry password, a deploy key. jailoc keeps them out of the image, out of `config.toml` and out of the shared directories. The configuration names where each secret comes from on the host, and jailoc delivers the value into the running VM's memory.

## Two kinds of secret

Secrets are declared under `[defaults.secrets]` or `[workspaces.<name>.secrets]`:

- `secrets.env.NAME` becomes an environment variable `NAME` in every session. It isn't written to a file.
- `secrets.file.NAME` becomes the file `/run/secrets/NAME`.

Each secret has exactly one source: `from_env` (a variable in the environment of the `jailoc` process), `from_file` (a file on the host) or `from_command` (a command jailoc runs on the host, whose standard output is the value). The configuration holds the reference, never the value. `from_command` lets a password manager or keychain hold the secret, so it never sits in a plain file or the shell environment.

## Delivery

jailoc reads every secret on the host before it boots the VM or applies a reload: it takes the variable from its own environment, reads the file or runs the command with `/bin/sh -c`. Commands run with the terminal attached, so a password manager can prompt for an unlock. A source that can't be read, or a command that fails or prints nothing, stops the operation before anything changes.

Once the guest's SSH server is ready, jailoc sends the values, together with the workspace's `env` entries, as a single payload over the SSH connection that the broker forwards into the VM.

Inside the guest, `jailoc-guest` writes each file secret to `/run/secrets/NAME` with mode `0400`, owned by `agent`. `/run/secrets` is a tmpfs owned by `agent`, so the values exist only in guest memory and disappear when the VM stops. Environment entries, including env secrets, go to an in-memory environment file that every new session loads. An env secret value loses its trailing line break and must fit on one line; multi-line values belong in file secrets.

jailoc never writes secret values to host disk. They aren't part of the image, the VM state directory, a share or a snapshot.

A guest that stops for any reason takes the whole VM down, and every start delivers the secrets again. jailoc also records each delivery in the VM state directory: when a session attaches to a running VM that never received its secrets, for example because `jailoc up` was interrupted right after the boot, jailoc delivers them first.

## Reload and rotation

`jailoc reload` is how secrets rotate: it reads every source again, including running every `from_command`, and pushes the full set. Changed values replace the old files, and files for secrets that are no longer configured are removed from `/run/secrets`. New sessions get the new environment. Sessions that are already running keep the environment they started with, so a process that read a variable at start doesn't see the change until a new session starts. Processes that read `/run/secrets/NAME` see the new value immediately.

## What this protects against

The design keeps secrets out of places that outlive the VM or reach other parties: image layers, build caches, Git history, and files shared with the host. It doesn't hide them from the agent. The agent runs as `agent`, can read every env secret in its environment and every file in `/run/secrets`, and, unless `protect_git` turns it off, can use `sudo` to become root in the guest. Every secret configured for a workspace is visible to the agent, and network egress to public addresses is open, so the agent can send a secret anywhere it can reach. Configure only the secrets the workspace really needs, and prefer short-lived, narrowly scoped tokens. See [Threat Model](threat-model.md).

See [Manage secrets](../how-to/secrets.md) for configuration steps.

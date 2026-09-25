# Remote Agent

The Remote Agent connector (`config.type: "remote_agent"`) connects a folder on
another machine — a till, a warehouse PC, a customer's server — to a pipeline.
The machine runs the small `vrsky-agent` program, which connects **out** to
VRSky over HTTPS; no firewall port is opened on it. Installing and registering
the agent is covered in the operator guide:
[Remote agent](../operator/remote-agent.md).

The agent's own config file lists its folders by **name** and direction (`read`
or `write`). A pipeline picks an agent and one of those names. The folder's path
stays on the machine: VRSky never sees it and cannot choose another.

## As a source (consumer)

Files that appear in the agent's **read** folder become messages: one file, one
message. The message carries the file's bytes, a content type detected from the
name and contents, and metadata `filename`, `agent_id` and `directory`.

After a file is sent it is moved into `processed/` inside the folder, or
deleted. A file already in the folder when the pipeline starts is picked up.

Config reference:

- `remote_agent.agent_id` — the agent (required).
- `remote_agent.agent_name` — the agent's name, for display only.
- `remote_agent.directory` — a folder name the agent reports as `read` (required).
- `remote_agent.after` — `"move"` (default) or `"delete"`. If several pipelines
  watch the same folder, the file is deleted only if all of them ask for it.

```json
{
  "type": "remote_agent",
  "remote_agent": {
    "agent_id": "3f0c…",
    "agent_name": "LAGER-SERVER-01",
    "directory": "superpos-out",
    "after": "move"
  }
}
```

## As a destination (producer)

Each message is written as one file into the agent's **write** folder. The file
is written under a temporary name, checked against a checksum, then renamed, so
whatever reads the folder never sees half a file. A file of the same name is
replaced.

Config reference:

- `remote_agent.agent_id` — the agent (required).
- `remote_agent.agent_name` — display only.
- `remote_agent.directory` — a folder name the agent reports as `write` (required).
- `remote_agent.filename_pattern` — optional, same placeholders as the
  [file connector](file.md): `{id}`, `{timestamp}` (`20060102-150405`),
  `{extension}` (from the content type) and `{source}`. Without a pattern the
  incoming `filename` is kept (with a new extension if a converter changed the
  format), else `<id>.<extension>`.

```json
{
  "type": "remote_agent",
  "remote_agent": {
    "agent_id": "3f0c…",
    "agent_name": "LAGER-SERVER-01",
    "directory": "superpos-in",
    "filename_pattern": "orders-{timestamp}.{extension}"
  }
}
```

## Watching it

After deploy, the builder's **Remote Agent** tab shows the agent and folder for
each end, whether the agent is online (refreshed every 15 s), and live events:
files received, files written, and failures.

## Notes

- **Starting.** A pipeline refuses to start if the agent is revoked, belongs to
  another workspace, or does not report the folder with the right direction.
  Fix the agent's config and restart it — it re-announces its folders — then
  deploy again.
- **Offline agents.** Messages for an agent that is offline wait in VRSky and
  are written, in order, when it reconnects. That waiting has limits: **up to
  72 hours, and 24 hours for files over 256 KB**. Past that they are lost.
  Files waiting in a read folder simply wait on the machine.
- **One file at a time.** Each pipeline delivers to its agent one message at a
  time, in order. While the agent is offline the next message is held, not
  retried, so an outage does not use up retries. Stopping or redeploying the
  pipeline while a message is held does use one: after five, that message goes
  to Failed Messages, where it can be retried.
- **Failures.** A file the agent cannot write (disk full, checksum mismatch) is
  retried with backoff and goes to Failed Messages after 5 attempts.
- **Size.** Uploads are limited to 2 GiB by default. A file VRSky refuses is
  moved to `rejected/` on the machine with a note saying why.
- **Duplicates.** A retried upload reuses its ID, so a file sent twice after a
  lost response is delivered once.

---
title: Matrix
description: Archive text from joined Matrix rooms, including bridged messaging accounts.
---

Archive messages from a Matrix account into msgvault for search and browsing.
This integration connects to the homeserver directly. Element can remain your
chat UI and does not need to run during ingestion. Existing bridges make other
networks' conversations available as Matrix rooms.

This functionality is local fork work and is not part of an upstream release.
The integration supports text, notice, and emote messages from selected rooms
or all joined chats. It preserves provider events and decrypted snapshots
alongside message bodies, sender identities, and conversations. It does not download media,
apply edits, reconcile redactions, or import reactions.
Rooms currently use the generic group-chat conversation type. Only senders of
archived text appear in the participant roster; this is not a complete member
list.

## Connect an account

Each msgvault data directory supports one Matrix login in this implementation.
Commands use the selected daemon, including when the CLI runs remotely. Passwords
and recovery secrets are passed through the existing authenticated daemon
credential channel, never command arguments.

1. Create a dedicated archive device for the account whose joined rooms you
   want to read. Supply its password through standard input:

   ```bash
   msgvault add-matrix --homeserver https://matrix.example.com \
     --user @alice:example.com --password-stdin < /path/to/private-password-file
   ```

   This implementation supports password login. SSO-only accounts are not
   supported yet. Reauthentication reuses the archive device; switching accounts
   in the same session directory is refused.

2. For encrypted history, restore the account's room keys from an existing
   Matrix secret-storage backup:

   ```bash
   msgvault recover-matrix --secret-stdin < /path/to/private-recovery-key-file
   ```

   Supply the recovery key or passphrase. The client authenticates the server
   backup before importing keys. A fresh device does not automatically have old
   room keys. If a page remains undecryptable, ingestion fails without advancing
   its cursor. Run recovery again when newer keys become available in the backup.

3. List joined rooms and choose their IDs:

   ```bash
   msgvault matrix-rooms
   ```

The daemon keeps its session, crypto state, and local pickle-encryption key in
`<data_dir>/tokens/matrix`. It uses msgvault's secure file helpers: directories
use owner-only access, and credential, key, and crypto-store files use `0600`
permissions or their Windows equivalent. Crypto session pickles are encrypted;
the SQLite file also contains plaintext device and room metadata. Archive
message bodies are decrypted content. Protect the data directory as you would
chat exports.

Commands hold a file lock for the dedicated device. If another Matrix command
is using it, the next command fails with a retry message rather than sharing
encryption state concurrently.

Matrix runs inside msgvault's Go binary, using Mautrix's pure Go encryption
backend. Repository builds and checks include the `goolm` build tag alongside
`fts5 sqlite_vec`. No separate client process is required. Commands manage the
archive device and its encryption state; none sends chat messages, joins rooms,
or changes room state.

## Archive selected rooms

```bash
msgvault sync-matrix --room '!chat:example.com'
msgvault sync-matrix --room '!chat:example.com' --room '!another:example.com'
msgvault sync-matrix --room '!chat:example.com' --max-pages 10
```

The first sync reads history backward in pages of at most 100 timeline events.
Room IDs may include a server (`!chat:example.com`) or use the domainless format
introduced in [room version 12](https://spec.matrix.org/v1.18/appendices/#room-ids).
Use the exact ID returned by `matrix-rooms`.
Unsupported events still count toward a page. `--max-pages` bounds pages across
the entire invocation; zero means unlimited. Bounded runs rotate among selected
rooms on later invocations so a busy room does not starve the others.

Rerun the same command to resume an interrupted or bounded backfill. After a
room's backfill finishes, later runs read forward from its saved history head.
This captures messages that arrived while the backfill was running. Event IDs
deduplicate replays, and cursors advance only after supported messages on a page
are stored. An account or room mismatch stops ingestion.

Use `--full` to restart selected rooms' history walks and repair existing rows.
Do not repeat `--full` when resuming a bounded run: it restarts that walk each
time. Upstream changes to existing messages, such as edits and redactions, are
not reconciled by this implementation.

Search archived content through the existing search UI or CLI. Matrix messages
use `message_type` `matrix` and raw format `matrix_json`. Bridged conversations
remain Matrix records; this importer does not infer native WhatsApp or Signal
identity from a bridge-generated sender name. Encrypted snapshots retain the
original ciphertext event in `msgvault_encrypted_event`.

```bash
msgvault search --message-type matrix
msgvault search 'project update' --message-type matrix
```

## Enable automatic sync

Configure the daemon to poll the connected account:

```toml
[server]
daemon_idle_timeout = "0s"

[matrix]
enabled = true
schedule = "*/5 * * * *"
max_pages = 100
# rooms = ["!chat:example.com"]
```

Omit `rooms` to discover all joined chat rooms on each run, including newly
joined rooms. Matrix spaces are excluded. Set `rooms` to a list of exact IDs
to restrict the scheduled archive. Manual `sync-matrix` still requires `--room`.

Restart the daemon after changing its configuration. Keep it running with a
service manager or `msgvault daemon start`; the idle timeout above prevents
an idle background daemon from stopping the schedule. A foreground
`msgvault serve` process runs until stopped.

Each pass processes at most `max_pages` history pages across the selected
rooms. It resumes checkpoints on the next tick, rotates bounded runs among
rooms, and refreshes the existing browse cache after writes. Scheduled passes
never use `--full`. They do not overlap other archive mutations. A failed or
interrupted page retains its previous cursor for retry.

Automatic sync uses the saved device and recovered room keys. It does not
store a password or recovery secret in configuration. If encrypted history
needs additional keys, run `recover-matrix`, then retry the sync.

## Verify changes

```bash
go test -tags "fts5 sqlite_vec goolm" ./internal/matrix
```

The integration test runs the actual Go SDK against a synthetic homeserver. It
covers login, encrypted key-backup recovery, message decryption, persistent
crypto state, missing-key failure, and archive search. Importer tests cover
pagination, retries, duplicate ingestion, cancellation, bounded room rotation,
and mismatched account or room responses. These tests never use personal
accounts.

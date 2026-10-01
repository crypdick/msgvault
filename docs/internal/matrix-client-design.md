# Matrix archive client

Decision: use the Mautrix Go SDK inside msgvault, with its pure Go encryption
backend selected by the `goolm` build tag. Current setup and supported message
types live in the [Matrix guide](../usage/matrix.md).

Matrix's HTTP history API is straightforward, but encrypted history also needs
device keys, durable encryption sessions, authenticated server key-backup
recovery, and replay checks. Reimplementing those protocols would increase risk.
The Go SDK supplies them without a separate client process or a new libolm
system dependency. The tradeoff is an SDK dependency and a required build tag;
repository build, test, lint, and CI entry points include it.

The daemon owns a dedicated Matrix device and a separate SDK crypto database.
One file lock protects that device's credentials, keys, and sync cursor across
commands. The archive still uses `Store` and its existing sync-generation fence.
Crypto `/sync` receives device keys; archive ingestion independently paginates
`/messages`. A limited crypto sync therefore cannot skip archive history.

Recovery authenticates the backup version against the decrypted private backup
key before importing room sessions. An undecryptable page fails before archive
cursor advancement. Backfill keeps its first head token for later forward
pagination, and each completed page persists a resumable checkpoint. Encrypted
events retain their original envelope alongside decrypted archive content.

The initial integration deliberately supports one password-based login per data
directory and explicitly selected joined rooms. Adding SSO, multiple accounts,
media, mutation reconciliation, or scheduling should extend those concrete
paths rather than introduce another runtime.

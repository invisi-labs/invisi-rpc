# Portable index snapshots

`Export` streams one consistent storage transaction into a gzip-compressed
newline-delimited JSON file. Each record is a single JSON object. Version 2
contains a header, ordered block records, and a footer:

- The header pins the format version, source profile, ABI SHA256,
  chain ID, protocol address, runtime code hash, and deployment start block.
- Each block record contains one complete `model.Batch`, including raw logs and
  normalized events, insertions, deliveries, and nullifiers.
- The footer contains the canonical checkpoint returned by the same storage
  export transaction. The final block must match it exactly.

All blocks from the deployment start through the checkpoint are required,
including blocks with no protocol events. Restore verifies contiguous block
numbers and parent hashes, requires one footer and end of stream, and checks the
gzip trailer before allowing the storage transaction to commit. Metadata must
match the configured deployment exactly. Storage replays batches to reconstruct
Merkle roots, historical nodes, membership paths, and nullifiers. No historical
chain RPC calls are needed. Import requires an empty deployment.

Snapshots live under `<base>/<decimal-chain-id>/<lowercase-protocol-address>/`.
Files include the checkpoint number, block hash, and whole-file SHA256 in their
names. The snapshot, `.sha256` sidecar, and `latest.json` manifest have mode 0600.
Export closes gzip, syncs the snapshot, and atomically renames it into place. It
then publishes the checksum sidecar and finally `latest.json`, each through a
synced temporary file and atomic rename. Readers can follow `latest.json` only
after all referenced files are durable. Export does not remove older snapshots.

`Inspect` scans the complete stream and returns its validated header with the
footer checkpoint attached. `InspectContext` also accepts a cancellation context
and decompressed byte cap for large-file inspection. `Restore` optionally accepts an expected compressed
file SHA256, which is checked before database import starts and checked again
against the bytes read during restore. An independently trusted checksum is
needed to authenticate the downloaded file; a sidecar from the same untrusted
server only detects accidental corruption.

`InspectWithLimit` and `RestoreWithLimit` accept a decompressed byte cap. The
default is 8 GiB, with a separate 64 MiB maximum JSON record size on both export
and import. These limits bound decompression and single-record allocation.
Malformed, truncated, over-limit, mismatched, or noncontiguous streams return an
error; storage rolls back the entire restore.

A snapshot is not a cryptographic proof of the chain's complete history. Parent
hash continuity does not establish canonicality without a trusted chain anchor.
Replaying stored normalized batches checks local tree consistency but does not
independently re-decode raw logs or prove that the publisher included every real
contract event. Obtain snapshots and their expected hashes from a trusted source,
and validate the imported checkpoint against the configured chain before serving
it as canonical data. Configure an offline import without a network root
validator if the caller requires restore to make no historical RPC requests.

Format 2 includes each canonical block timestamp and binds each event/output candidate to that block. Credit limits recovery derives UTC periods from this timestamp. These historical timestamps inherit the trusted snapshot source/checksum requirement above; a checkpoint hash by itself does not authenticate arbitrary normalized historical records. Block records must contain an explicit uint64 timestamp; missing, null, or out-of-range values are rejected.

Source revisions are release provenance, not snapshot compatibility fields. Container releases record the tagged source commit in the OCI `org.opencontainers.image.revision` label and publish it with the image digest. Snapshot validation is independent of source commit identity.

Format 2 separates ordinary `outputs` from `creditStates`, with both encrypted state deliveries retained per Credit leaf, and records `creditNullifiers` separately from ordinary Note nullifiers. Credit grant history is rebuilt from canonical creation/revocation events during import. The source profile is `invisi-protocol-v1-postpaid-credit-typed`; earlier snapshots are rejected without writing their normalized records. Public API v1 is independent of these internal format identifiers. Preserve any local experimental data before selecting a separate empty database for an incompatible format; never relabel normalized records.

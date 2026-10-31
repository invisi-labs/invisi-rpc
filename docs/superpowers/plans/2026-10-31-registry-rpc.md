# Registry event discovery implementation plan

Goal: expose event-indexed receiving-key discovery through Protocol RPC and the local TS example.

Ruling: user requested event indexing after initial live-read implementation. Replace live reads with the shared canonical log stream and projection. No live read per query. Both protocol and registry deployment pins bind persisted state. Index starts at the earlier deployment height. Preserve all registration updates and order by block/log index; orphaned block deletion cascades to registration history. Snapshot normalized batches include registrations; bump index format and snapshot source profile so old state cannot claim complete coverage. No automatic database deletion. Selected RPC is trusted for keys; block metadata is not a storage proof.

- [x] Add server and typed client tests before implementation.
- [x] Decode registry events only from configured registry, index in shared contiguous batches and atomically project append-only key history in SQLite/PostgreSQL.
- [x] Return registered keys through indexed head, error before registry deployment is indexed, null for unknown owner.
- [x] Include history in snapshot export/import and bind registry metadata.
- [x] Expose client getReceivingAddress with owner/key/nonce/block validation; update flat Chinese 02-01b example.
- [x] Validate full Go/SDK tests, build/typechecks, local example checks; fresh-context review. No deploy, transaction execution, push or unrequested commit.

Verification: go test ./... and go vet ./... passed. Temporary local PostgreSQL full suite and final storage/integration tests passed; the temporary server was stopped. SDK pnpm build/lint/test passed, local example format/check/test passed (8 tests). Fresh-context review found no code issues; stale README format text was corrected. No deployment, public testnet transaction, commit or push performed.

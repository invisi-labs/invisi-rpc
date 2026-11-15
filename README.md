# Invisi Protocol RPC

Standalone Go service for public Invisi data: canonical events, shared Note Trees, historical membership paths. Depends only on the Go SDK protocol module, not the wallet SDK. Wallet clients retain decryption, private note storage, authorization and proving, and verify results through an independent Chain RPC.


```text
invisi-protocol-rpc (chain authentication / indexing / pagination)
  └── invisi-sdk-go/protocol v1.2.2
      └── settlement (canonical ABI and operation binding)
```

The service does not depend on the wallet SDK. Shared settlement fixtures are maintained in `../invisi-sdk-go/protocol/settlement`; regenerate them from that module with `node generate.mjs`.


## Release downloads

Pushing a stable `vX.Y.Z` tag runs tests and publishes the multi-platform GHCR image and GitHub Release binaries from the same tagged source. Manual workflow dispatch accepts an existing tag. Existing Release notes are preserved when assets are uploaded.

Release archives are named `invisi-protocol-rpc_vX.Y.Z_<os>_<arch>`, covering Linux, macOS (`darwin`) and Windows on `amd64` and `arm64`. Linux/macOS use `.tar.gz`; Windows uses `.zip`. Each archive contains the CLI, LICENSE, README and the CLI reference `cammand.md` when available in the tagged source. Binaries require no Go installation or C runtime. Download `SHA256SUMS-vX.Y.Z.txt` to verify archive checksums before extraction. Configure the deployment catalog and chain RPC as described below; credentials and local configuration are never bundled.

## Initialize and run

Requirements: Go 1.26+, an EVM Chain RPC for each selected chain, and either SQLite (default, embedded) or PostgreSQL 16+. The CLI uses [urfave/cli v3](https://cli.urfave.org/v3/).

```sh
go build -o ./bin/invisi-protocol-rpc ./cmd

# Use the catalog published by the Protocol deployment tooling.
export INVISI_RPC_DEPLOYMENT_FILE=../invisi-protocol/deployment/deployment.json
./bin/invisi-protocol-rpc init
./bin/invisi-protocol-rpc chain add 31337 --rpc http://127.0.0.1:8545
./bin/invisi-protocol-rpc run
```

The default data directory is `./invisi-protocol-rpc`. Set `INVISI_RPC_DATA_DIR` or pass `--dir` to select another directory. `init` also accepts an optional positional directory. The executable is built under `bin/` so it does not conflict with the data directory.

Full command syntax, flags, environment variables, database setup, snapshot bootstrap and operational examples are in [cammand.md](cammand.md).

`init` creates an empty configuration without contacting a Chain RPC and refuses to overwrite it. Chain changes are locked, published atomically and reloaded while running. Removing a chain retains its database and snapshots. Each chain has independent state and shares the HTTP listener; `GET /v1` lists active routes at `/v1/chains/<chainId>`.

The canonical [deployment catalog](../invisi-protocol/deployment/deployment.json) is owned by Protocol deployment tooling. No deployment records are embedded in this binary. `chain add` copies the selected deployment pins into the configuration; later catalog edits do not retarget an initialized node. Startup authenticates each upstream chain ID, runtime code and saved checkpoint before serving. The current catalog contains a controlled local deployment with mock verifiers.

Snapshots contain public normalized history and can bootstrap SQLite or PostgreSQL. Obtain snapshots and expected checksums from a trusted publisher: a checksum alone does not authenticate historical records. Imports are atomic and deployment-bound; clients retain independent chain verification. See the [snapshot trust and bootstrap reference](cammand.md#snapshots-and-bootstrap). No keys, private notes or wallet secrets belong in this service.

## JSON-RPC

POST JSON-RPC 2.0 with positional parameter arrays to `/v1/chains/<chainId>`. `GET /v1` discovers active chains. API namespace and on-chain `VERSION()` remain `v1` / `1`; deployment runtime pins distinguish the deployed contract. Service release 1.2.1 exposes independent Note and Credit methods under its v1 business API; mixed business methods are not available.

| Method | Parameters | Result |
| --- | --- | --- |
| `invisi_getVersion` | `[]` | API and Protocol versions |
| `invisi_getDeployment` | `[]` | Pinned public deployment profile |
| `invisi_getSettlementOperations` | `[{fromBlock?, toBlock?, limit, cursor?}]` | Canonical public logical operation page |
| `invisi_getProtocolTransactions` | `[{fromBlock?, toBlock?, limit, cursor?}]` | Same logical-operation page including supported direct failures |
| `invisi_getTransactionOperations` | `[transactionHash]` | Receipt availability and authenticated logical operations |
| `invisi_getStatus` | `[]` | Deployment, indexed head and native latest/safe/finalized availability |
| `invisi_readChainState` | `[{kind, block?, address?, from?, data?, gas?, transactionHash?}]` | Deployment-bound, restricted read-only header/code/call/receipt/transaction facts; see Trusted WalletSession read gateway below |
| `invisi_getBlock` | `[blockNumber]` | Low-level complete indexed block and raw logs |
| `invisi_getEvents` | `[range]` | Low-level canonical events |
| `invisi_getRoots` | `[treeId]` | Recent physical NoteTree roots |
| `invisi_getMembership` | `[treeId, leafIndex, root]` | Physical leaf membership; does not identify a Note or Credit |
| `invisi_getNote` | `[commitment, atBlock]` or `[commitment, {"finality":"safe"}]` / `[commitment, {"finality":"finalized"}]` | Legacy: `{output, membership}` or `null`; finality selector: versioned, deployment-bound `confirmed`/`pending` evidence with `confirmation` (see Trusted Note confirmation below) |
| `invisi_getNoteUpdates` | `[range]` | `{blockHash, notes}`; ordinary Notes only |
| `invisi_getTransactionNoteUpdates` | `[transactionHash]` | Availability metadata and `notes` |
| `invisi_isNoteSpent` | `[nullifier, atBlock]` | Ordinary Note consumption through the indexed block |
| `invisi_getCreditState` | `[commitment, atBlock]` | `{state, membership}` for Credit authority, or `null` |
| `invisi_getCreditTransitions` | `[range]` | CreditCreated / CreditDraw / CreditUpdated / CreditRevoked events |
| `invisi_getTransactionCreditUpdates` | `[transactionHash]` | Availability metadata, `states`, and `transitions` |
| `invisi_isCreditConsumed` | `[nullifier, atBlock]` | Credit state consumption through the indexed block |
| `invisi_getCreditAllocation` | `[owner, token, atBlock]` | `{block, shard, slot}`; next original-grant location |
| `invisi_getCreditRevocationBitmap` | `[owner, token, shard, word, atBlock]` | `{block, bitmap}` |
| `invisi_getReceivingAddress` | `[ownerAddress]` | Indexed registered keys and nonce |

`invisi_getUpdates`, `invisi_getTransactionUpdates`, `invisi_detectOutputs`, and `invisi_isSpent` are removed and return JSON-RPC method-not-found (`-32601`). Use the corresponding explicit Note or Credit method above. Low-level blocks/events and physical tree membership remain shared; public business responses do not mix Note and Credit collections.

`NoteOutput` retains `block`, `treeId`, `leafIndex`, `commitment`, `token`, `ciphertext`, `clue`, `transactionHash`, `logIndex`, and `kind`. An indexed Credit state has the same leaf metadata, with `deliveries:[{ciphertext,clue},{ciphertext,clue}]` instead of a single ciphertext/clue. Both owner and spender envelopes are retained in on-chain order, without inferring which recipient controls them. `getNote` never returns Credit and `getCreditState` never returns a Note. A missing result establishes no cryptographic absence claim. Clients recompute commitments, verify paths, authenticate roots through an independent Chain RPC, and check the appropriate derived nullifier.

Credit creation registers authority without principal funding; an optional authorized fee is pulled from the owner wallet. Credit draw consumes N prior states and appends N successor states plus exactly one ordinary receiver Note; only that receiver Note appears in Note queries. Payer totals and grant usage are gross; the ordinary Note amount is gross minus the authorized fee, which accrues to the shared broadcaster ledger. Credit update consumes one prior state and preserves its grant namespace; its optional fee is pulled from the owner. Revocation changes an original-grant bitmap and may pull its authorized owner fee; it creates no Note, state, or nullifier. Allocation starts at shard `"1"`, slot `0`, and advances on original creation only. A revoked grant may still have an unconsumed state: bitmap status and state consumption are separate facts. Public owner/token queries do not identify decrypted wallet identities.

Hashes, addresses, ciphertexts, clues and keys use `0x` hex. ABI uint256 and uint64 values inside decoded event payloads are decimal strings; uint16 slots are numbers. Allocation `shard` is a canonical positive uint64 decimal string; bitmap is a uint256 decimal string. Bitmap `word` is an integer from 0 through 255. Block/tree/leaf indices and header timestamps remain JSON numbers.

A range is `{ "fromBlock": 100, "toBlock": 110, "blockHash": "0x…" }`, anchored to indexed `toBlock`. It accepts at most 10,000 inclusive blocks, bounded result counts, and 8 MiB responses. Oversized results fail explicitly; request smaller ranges or complete blocks. `atBlock` must be indexed. Responses recheck anchors before returning; retain hashes and retry after reorganizations. Live indexing validates contract-recorded batch-final roots, not intermediate per-leaf roots. Blocks, typed projections, roots, nullifiers and grant history commit and roll back together.

## Updates for one transaction

Use `getTransactionNoteUpdates` and `getTransactionCreditUpdates` independently. Both return `transactionHash`, `status`, `indexedHead`, and optional `blockNumber`, `blockHash`, `receiptStatus`. `not_found` means no mined receipt (possibly pending); `pending_index` means its canonical block is not indexed yet; `indexed` means its indexed block was authenticated during the query. Typed collections are `null` before indexing and arrays afterward, including empty arrays for unrelated/reverted transactions. Receipt execution success is separate from availability. Other transactions in the same block are excluded. Upstream failures, predeployment transactions and observed reorganizations fail explicitly. The service reads a mined receipt and the existing local block, never substitutes upstream logs for indexed data.

## Local account discovery

Clients fetch public Note updates and Credit transitions, then detect and decrypt locally. RPC never receives account detection keys. Status contains deployment and indexing metadata only. Remove `detectionEnabled` and `detectionBits` from chain configuration before starting this binary; strict JSON validation rejects these fields. Update SDK clients before deploying because older clients require detection metadata in status.

## HTTP gateway and overload handling

The service accepts public requests. Optional API authentication and per-IP or per-issued-key rate limits belong to the HTTP gateway. An API key authorizes service access; it is unrelated to a token contract or a wallet detection key. See [the Nginx example](deploy/nginx.conf.example): public access is enabled, and a commented guard enables private access. Replace the placeholder key before enabling that guard. Serve production traffic over HTTPS and restrict direct access to the backend when gateway authentication is required. Behind another proxy, configure trusted proxy addresses before using forwarded client IPs.

The example limits HTTP requests by IP or recognized key. It does not meter individual JSON-RPC batch calls or their computational cost. Its rate and connection values are illustrative deployment settings, not benchmark results.

The backend independently limits requests to 1 MiB, batches to 20 calls and batch responses to 16 MiB. Ranged event/update responses are limited to 8 MiB and return an explicit smaller-range error rather than partial data. Query ranges and result limits still apply. `maxConcurrentRequests` defaults to 64 (allowed range 1–4096) across all chains on the listener. Exhausting this budget returns HTTP 429 with `Retry-After: 1`.

Clients should use bounded exponential backoff with jitter, respecting retry guidance. Authentication failures and invalid requests require correction rather than automatic retries.

## Development

| Directory | Responsibility |
| --- | --- |
| `cmd/main.go` | Process entry point |
| `internal/app`, `internal/config` | CLI, initialization, lifecycle and settings |
| `internal/indexer` | Chain verification, latest canonical blocks, native finality and reorgs |
| `internal/protocol` | Pinned ABI and event normalization |
| `internal/cryptography`, `internal/merkle` | Poseidon2, public Decaf377 key validation and membership paths |
| `internal/storage`, `internal/snapshot` | SQLite/PostgreSQL and portable bootstrap |
| `internal/rpc` | HTTP public queries and limits |

Run `go generate ./internal/protocol` to regenerate the event ABI after authenticating all artifact source hashes against the sibling Protocol checkout. `internal/protocol/protocol-source.json` records that provenance. The embedded ABI is synchronized with the current Protocol build; snapshot metadata pins its SHA-256 and Protocol v1 profile. Crypto source references and independent Noir/Barretenberg/Rust vectors are retained.

```sh
go test ./...
go vet ./...
go build -o ./bin/invisi-protocol-rpc ./cmd

# Include PostgreSQL integration and cross-backend snapshots.
TEST_DATABASE_URL='postgres://invisi:local-development-only@127.0.0.1:5432/invisi?sslmode=disable' go test -race ./...

# Include the real local contract lifecycle test on an existing local deployment.
TEST_CHAIN_RPC_URL=http://127.0.0.1:8545 TEST_DATABASE_URL='postgres://invisi:local-development-only@127.0.0.1:5432/invisi?sslmode=disable' go test ./internal/integration
```

SQLite tests run by default. PostgreSQL/EVM tests explicitly skip without their environment variables. EVM tests require loopback chain 31337 with the contract repository's local deployment; its mock verifiers do not establish real proving correctness.

### Container images

Versioned images are published to `ghcr.io/invisi-lab/invisi-protocol-rpc` for `linux/amd64` and `linux/arm64`:

```sh
docker pull ghcr.io/invisi-lab/invisi-protocol-rpc:1.1.1
docker run --rm ghcr.io/invisi-lab/invisi-protocol-rpc:1.1.1 --help
```

Pushing a stable `vMAJOR.MINOR.PATCH` tag runs Go tests and vet, then builds and publishes the corresponding image tag without the leading `v`. The **Publish container image** workflow also accepts an existing tag through its manual `tag` input. It compiles that tag's source with the workflow's Dockerfile, includes the Apache license, and records the tagged source commit in its OCI revision label and workflow release summary, alongside the image digest. Source commit identity does not constrain snapshot import. Use the digest to pin an exact image in production. Images have their own visibility settings; public images support anonymous pulls even while the source repository is private.

The Docker image contains no deployment catalog. Mount `invisi-protocol/deployment/deployment.json` or a private deployment output read-only at `/deployment.json`; the image sets `INVISI_RPC_DEPLOYMENT_FILE` to that path. It sets `INVISI_RPC_DATA_DIR=/data` and runs `run`. Mount a data directory writable by the non-root container user (UID/GID 65532), and set `INVISI_RPC_LISTEN=0.0.0.0:8080` inside the container. Initialize the volume with `init /data`, then use `chain add ID --dir /data --rpc URL` in the same mounted volume. Chain management commands may run in a separate container sharing the volume. Keep PostgreSQL credentials out of images.

### SDK deployment discovery

Clients call `invisi_getDeployment` on `/v1/chains/<chainId>` before constructing their Protocol object. The response contains `apiVersion`, `protocolVersion` and `deployment`. Deployment fields are `chainId`, `protocol` and optional `registry`. Both contracts use `address`, `runtimeCodeHash` and `fromBlock`, represented by one shared contract deployment type. Set `registry` in the chain's `deployment.json` profile before `chain add`; they are copied into the initialized configuration. Existing initialized configurations can set these metadata fields in their deployment section while stopped. Public and private installations use the same interface. Deployment metadata identifies the service's selected pool; clients choose a trusted service or independently pin the deployment, then verify chain facts through Chain RPC.

Encrypted output memos are opaque to this service. Present ciphertexts are opaque byte strings bounded at 4096 bytes, regardless of envelope version or note kind. Funds outputs may instead carry paired empty ciphertext/clue; active Credit role deliveries remain mandatory. The service does not interpret wallet codec versions, flags, return addresses or memo schemas. The current SDK uses codec version 1 for both funds and Credit (first-byte high bit 0/1 identifies the kind, low seven bits carry version 1..127; headers 0x01/0x81); adding another wallet codec within the shared resource bound requires no server changes. Aggregate outputs frame each nonempty delivery as a two-byte big-endian ciphertext length, ciphertext, then a 68-byte FMD clue. An omitted funds delivery uses only a zero two-byte length prefix, with no following clue. The decoder validates framing and preserves each output's token, commitment and leaf position; wallet SDKs decrypt and interpret payload data.

### Credit recovery block context

Every indexed event and output candidate includes `block` with `number`, `hash`, `parentHash`, and `timestamp` (uint64 Unix seconds). The timestamp comes from the canonical header whose hash is used to fetch logs during indexing. Credit draw recovery must use the transition block timestamp to derive UTC+0 Gregorian day/month usage; never use local wall-clock time or caller-supplied dates. Draw transaction parameters contain no date fields. The private per-grant limits and counters remain encrypted; this service does not decrypt them. Credit draw capacities are 1 through 5 and 10 through 80 in steps of 10; proof/circuit dimensions belong to the current Protocol manifest, not this indexer.

Index format 3 and snapshot format 2 persist this block context in SQLite and PostgreSQL. Block records require an explicit uint64 timestamp, including zero for the Unix epoch; malformed records and unsupported formats are rejected. Snapshots use the postpaid typed Credit source profile. Contract version remains 1; API version is v1.

Portable snapshots retain the existing trusted publisher/checksum model. Historical timestamps restored from a snapshot inherit that trust; hash continuity and a checkpoint hash alone do not authenticate arbitrary historical normalized records. The saved checkpoint timestamp is checked against its canonical chain header before serving.

### Indexed receiving-address discovery

`invisi_getReceivingAddress` accepts a nonzero 20-byte EVM owner address. The indexer follows `ReceivingKeysSet` events from the configured Registry as well as Protocol events. Its contiguous stream begins at the earlier contract deployment block. Registrations and updates are stored atomically with the containing block; latest keys are selected by block number and log index. Reorganizations remove orphaned records and restore the previous registration automatically. Polling canonical block logs also catches events missed while the service was offline.

A registered result has `ownerAddress`, `viewingPublicKey` (33-byte hex), `clueKey` (32-byte hex), `nonce` (decimal uint64 string) and `block` (the indexed head: number/hash/parentHash/timestamp). The SDK validates the keys and encodes the full `0i` address locally. Unregistered owners return `null`. If indexing has not reached the Registry deployment, the method returns an error instead of claiming an owner is unregistered. Use `invisi_getStatus` to inspect progress, including when lookup returns null. Newly registered or updated keys become visible after the configured confirmation depth and index progress, just like `invisi_isSpent`.

The returned block reference is not a proof of contract storage: callers trust the selected service for registered keys, or independently query their Chain RPC. Registry lookup performs no Chain RPC calls per request.

Index format 3 includes receiving-key and Broadcaster registry history; the current `invisi-protocol-v1-postpaid-credit-typed` snapshot profile also separates postpaid Credit state. Incompatible experimental databases and snapshots are rejected to avoid silently missing registrations or reinterpreting records. Fresh development databases index from both deployment blocks; no data is deleted automatically. Registry address, code hash and start block are pinned in the database and snapshot profile. No automatic deletion or reset is performed.

The embedded registry ABI is generated from `invisi-protocol/contracts/out/InvisiAddressRegistry.sol/InvisiAddressRegistry.json`.

### Indexed Broadcaster discovery

Set optional `deployment.broadcasterRegistry` to the independently deployed registry's
`address`, `runtimeCodeHash` and `fromBlock` in `deployment.json` before `chain add`.
The optional field has the same pinned contract shape as `registry`:

```json
"broadcasterRegistry": {
  "address": "0x...",
  "runtimeCodeHash": "0x...",
  "fromBlock": 123456
}
```

Use verified deployment values; no registry address is built into the service.
All event source addresses must be distinct. The indexer verifies runtime code and follows
`BroadcasterSet` and `AvailabilitySet` alongside Protocol and receiving-key events.
Registration updates preserve the current availability; `setEnabled` controls it separately.
Records are persisted transactionally in SQLite or PostgreSQL and restored on chain rollback.
Adding or changing registry pins on an existing populated index requires a fresh database;
configuration changes never silently skip earlier registrations.

`invisi_getBroadcasters` uses one positional query object:

```json
{"jsonrpc":"2.0","id":1,"method":"invisi_getBroadcasters","params":[{"enabled":true,"limit":100}]}
```

The result is `{block, broadcasters, next?}`. Each record contains `operator`, `endpoint`,
`metadataURI`, `quoteSigner`, `feeRecipient`, `enabled` and the last update `block`. `enabled` is an optional boolean
filter; omit it for all records. `limit` defaults to 100 and must be 1–100. Results are ordered
by operator address. If `next` is present, pass it as `after` with the exact returned `block`
anchor for the next page. Unknown or reorganized anchors are rejected. No registry configured,
or an index that has not reached its deployment block, produces an error rather than an empty
list. Clients must still verify signed quotes; registration and enabled status are public
operator declarations, not an endorsement or live availability guarantee. The RPC does not
register services, change their data or call their endpoints.

Snapshots use the `invisi-protocol-v1-postpaid-credit-typed` source profile and pin all three
ABIs and configured deployments. Earlier snapshot profiles must be rebuilt/exported with the
current indexer. This does not change on-chain Protocol VERSION 1.

## Canonical settlement facts

The three operation methods are independent chain-fact APIs. They do not assess funding provenance or combine the existing Note/Credit query methods. `invisi_getProtocolTransactions` returns the same logical-operation page as `invisi_getSettlementOperations`; consumers group `txHash` when presenting transaction rows.

A page query uses numeric block bounds and `limit` from 1 through 100. Omitted `fromBlock` begins at Protocol deployment, independently of earlier Registry indexing; omitted `toBlock` freezes the current indexed head. Responses contain `operations`, `snapshot:{blockNumber,blockHash}`, `indexedHead:{blockNumber,blockHash}`, optional `nextCursor`, and `indirectFailureDiscovery:"unsupported_without_traces"`. An opaque continuation binds chain, Protocol, fixed snapshot, range, transaction position and member offset. Follow the cursor even after an empty page; omit block bounds on continuation. Any missing indexed block, upstream failure, deployment mismatch or changed canonical snapshot returns an error. This implementation replays the existing indexed block/log data against canonical chain blocks, with no second persistent index or schema migration. Each page scans at most 16 blocks and 256 relevant receipts. Reaching a page or work limit returns a continuation, including within a dense block or an aggregate transaction; consumers must follow it until absent before treating the selected range as complete.

Each public operation includes decimal-string `chainId/blockNumber/timestamp/operationIndex/memberIndex/logIndex/mainCircuitCapacity`, block and transaction hashes, `kind/tokenAddress`, separate `noteNullifiers/creditNullifiers`, separate `noteOutputs/creditOutputs`, `grantRefs`, gross `funding:[{payer,tokenAddress,amount}]`, withdrawal/fee fields, `success`, and `operationDigest`. Output locations use `noteCommitment/treeId/leafIndex`; amounts are absent unless public on chain. Credit outputs remain typed authority state and never ordinary Notes. No private opening or witness is accepted.

Direct calldata is decoded with an exact generated-ABI round trip. Aggregate members retain their native capacity and original ordering. A transact digest is Keccak256 of `abi.encode(string("Invisi.Provenance.Operation.v1"),uint64(chainId),address(protocol),uint8(2),uint32(C),bytes32[] publicInputs)`, with the actual ordered `17+3*C` words including wallet authorization. The codec, source-authenticated ABI generator and independent ethers vectors live in the `settlement` package of the shared `github.com/invisi-lab/invisi-sdk-go/protocol` module (v1.2.2). RPC retains canonical chain receipt/log authentication, indexing, and pagination. BatchRequest native capacity and statement hash, typed output insertions, fee accrual, and actual ERC20 funding receipts are cross-checked. Same-token/same-payer pooled pulls are allocated by the authenticated ordered member requests, never by `tx.from`.

A transaction lookup returns `not_found`, `pending_index`, `failed`, or `indexed`, with receipt success separate from indexing availability. Read-only full block discovery includes direct reverted Protocol transactions. Indirect successful calls with Protocol logs and indirect failed hash lookups report `indirect_call_attribution_requires_traces`; their statement, payer and outputs are not guessed. Reverted indirect calls cannot be discovered as a complete list without traces. Unsupported selectors or unauthenticated funding receipts carry an explicit `unsupportedReason`; a consumer must not recognize such operations. This is a bounded replay capability, not a claim of complete trace-backed historical discovery.


`invisi_getStatus` includes `finality.latest`, `finality.safe`, and `finality.finalized`. Each contains `status` (`available`, `unsupported`, `unavailable`, or `inconsistent`) and `block` (the existing block shape, or `null`). These are native `eth_getBlockByNumber` heads from the configured Chain RPC, independent of `indexedHead`; no block-depth fallback is used. `unsupported` means an explicit unsupported-method/invalid-tag RPC response, while transport failures and null results are `unavailable`. Headers are hash-checked against canonical numeric reads and ordered finalized ≤ safe ≤ latest. Incoherent samples are retried once. Missing safe/finalized capability does not stop latest indexing or query service.

Accepted finalized anchors are stored durably per deployment in SQLite/PostgreSQL and survive index rollback and service restart. A changed canonical hash at that anchor pauses indexing and reports finalized as inconsistent; investigate the upstream before recovery. An upstream whose latest/finalized height regresses below the accepted anchor is unavailable and pauses indexing until it catches up, without creating a permanent conflict fence. Transient inability to verify an existing anchor also pauses indexing without changing the anchor. Stalled native finality remains available at its unchanged height; applications compare their transaction block against that height. Snapshot exports contain indexed data, not the operator's accepted-finality anchor; restoring a snapshot initializes native finality against the configured upstream.

The additive finality-state table is initialized automatically; standalone reversible SQL is in `deploy/migrations/001-native-finality.{up,down}.sql` for either backend. A finalized-history conflict creates a sticky persisted fence: correcting the provider or restarting does not clear it. The read-only service remains available with finalized `inconsistent`; indexing stays paused. Operator recovery requires stopping the service, investigating the conflicting upstream/index histories, backing up the database, and explicitly clearing `conflicted` only after validating the accepted anchor and indexed history. Never drop or reset the anchor merely to make a conflicting provider pass validation.

## Trusted Note confirmation

`invisi_getNote` preserves `[commitment, atBlock]` and its historic `output`/`membership` or null response. A new selector `[commitment, {"finality":"finalized"}]` (or `safe`) requests server-authenticated confirmation. `latest`, unknown selector fields and null selectors are rejected. Send JSON-RPC to `/v1/chains/{chainId}` as before.

Confirmation mode always returns `apiVersion`, `protocolVersion`, `deployment`, `indexedHead`, `status`, `reason`, `output`, `membership`, and `confirmation`. Confirmed results include the existing output/proof plus `{finality, verifiedAt, finalityHead}`. The verification anchor is the lower of the indexed head and the native finality head. The server authenticates canonical headers, successful transaction receipt, Merkle proof and the contract's recorded root at that anchor, then rechecks canonicality/finality before returning.

Pending results have null output, membership and confirmation. Reasons are `not_observed`, `pending_index`, `confirmation_pending`, `finality_unsupported`, `finality_unavailable`, or `finality_inconsistent`. Missing indexed data does not prove no payment occurred; retry after synchronization. Upstream query or binding failures return JSON-RPC errors, never confirmed evidence. There is no fixed-depth fallback for native finality.

This mode explicitly trusts the selected Protocol RPC for canonicality and finality. It lets server applications avoid direct Chain RPC access; it is not independent light-client verification or a spending-authority proof. It accepts no nonce, opening or merchant order. Consumers still compare the commitment with their private receiving materials and enforce local one-time redemption. Existing SDK independent chain-authentication paths remain available.

The cross-language wire fixture is `internal/rpc/testdata/note-confirmation.json`; regenerate intentionally with `UPDATE_NOTE_FIXTURE=1 go test ./internal/rpc -run TestNoteConfirmationWireFixture`.

### Trusted WalletSession read gateway

`invisi_readChainState` on `/v1/chains/{chainId}` provides read-only chain facts for clients that intentionally trust this Protocol RPC instead of connecting to an Ethereum node. It accepts one object with `kind` (`header`, `code`, `call`, `receipt`, or `transaction`), optional `block` (`latest`, `safe`, `finalized`, or a hex block number), and the corresponding `address`, `from`, `data`, `gas`, or `transactionHash`. Responses include protocol/API versions and the complete deployment profile; header hashes and receipt block bindings are checked by the SDK trusted providers.

Contract calls are restricted to allowed selectors and canonical argument encodings for pinned protocol/registry contracts, broadcaster lookup and token metadata/balance/allowance reads. Calldata, gas and response sizes are bounded. Transaction simulation uses read-only `eth_call` inside this service; there is no send/sign/write operation or arbitrary RPC forwarding. The public client still calls only `invisi_*`. This endpoint does not independently authenticate the node: clients trust the selected Protocol RPC for canonical chain state and finality, and pin its deployment.

The SDK trusted providers (Go `rpc.NewTrustedProvider`, TypeScript `TrustedProvider` from `@invisi-labs/rpc/trusted-provider`, Rust `rpc::TrustedProvider`) adapt these facts to each language’s chain-reader interface for WalletSession preparation and operation recovery. Deploy the updated RPC service before enabling Facilitator collection. Existing `invisi_getNote` finalized confirmation remains the source of receiving-note evidence.

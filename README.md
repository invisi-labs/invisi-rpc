# Invisi Protocol RPC

Standalone Go service for public Invisi data: canonical events, shared Note Trees, historical membership paths and optional delegated FMD filtering. No Invisi SDK dependency. Wallet clients retain decryption, private note storage, authorization and proving, and verify results through an independent Chain RPC.

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

`init` creates an empty configuration. Add and remove chains independently, including while the service is running:

```sh
./bin/invisi-protocol-rpc chain add 31337 --rpc http://127.0.0.1:8545
./bin/invisi-protocol-rpc chain list
./bin/invisi-protocol-rpc chain set-rpc 31337 --rpc http://127.0.0.1:9545
./bin/invisi-protocol-rpc chain remove 31337
```

Each command accepts `--dir`. Configuration updates are locked and published atomically. The running service automatically reloads changes. A new chain becomes available after deployment and saved-state validation; changing its RPC validates the replacement before switching. Failed replacement validation leaves the existing connection active and is retried. Removing a chain stops its workers and withdraws its HTTP route, retaining its database and snapshots. Adding it again resumes the saved checkpoint.

Each chain has independent indexing, database state and snapshots; all share one HTTP listener. An empty service can run while waiting for chains. `GET /v1` lists active chain IDs and RPC paths; `chain list` lists saved configuration, which can include chains still awaiting validation. Send JSON-RPC to `/v1/chains/<chainId>`, for example `/v1/chains/31337`.

### Deployment catalog

The canonical catalog is [invisi-protocol/deployment/deployment.json](../invisi-protocol/deployment/deployment.json). Protocol deployment tooling owns this single file; RPC does not keep a separate catalog. It contains Protocol addresses, runtime bytecode hashes and starting blocks for all networks, keyed only by chain ID. By default, `chain add` reads `./deployment.json` from the current working directory. Use `--deployment-file PATH` or `INVISI_RPC_DEPLOYMENT_FILE` to select its location. No deployment records are embedded in the binary. A missing or invalid file, or an absent chain ID, causes `chain add` to fail without modifying the configuration.

The current catalog contains the controlled **31337 local deployment with mock verifiers**. Only completed deployments belong in the catalog; absent chain IDs return an error. Mainnets and testnets use the same structure.

Deployment commands generate the records after checking successful receipts and runtime code. Point RPC at that output without manually copying its fields. For a private deployment file:

```sh
./bin/invisi-protocol-rpc init ./private-data
./bin/invisi-protocol-rpc chain add 42161 --dir ./private-data \
  --rpc https://YOUR_ARBITRUM_RPC \
  --deployment-file /path/to/private-protocol/deployment/deployment.json
```

The file structure is `{ "version": 1, "chains": { "42161": { "name": "Private Arbitrum", "protocol": { "address": "0x…", "runtimeCodeHash": "0x…", "fromBlock": 123456 } } } }`. Use real deployment values; zero addresses and hashes are rejected. `runtimeCodeHash` is Keccak-256 of deployed runtime bytecode, including library addresses and immutables. `fromBlock` is the Protocol creation block.

`chain add` resolves selected records into `config.json`. Subsequent catalog changes do not silently retarget an initialized node. `run` verifies every RPC's chain ID and runtime hash before serving. The database also rejects mismatched persisted deployment information.

After chains have been added and snapshots exported, the directory contains:

```text
invisi-protocol-rpc/
├── config.json                             # mode 0600; selected chains and settings
├── chains/
│   ├── 1/protocol.sqlite
│   └── 42161/protocol.sqlite
└── snapshots/
    ├── 1/0x<protocol-address>/
    └── 42161/0x<protocol-address>/
        ├── <height>-<block-hash>-<sha256>.snapshot.json.gz
        ├── <snapshot-file>.sha256
        └── latest.json
```

`init` refuses to overwrite an existing configuration. `run` resumes each checkpoint, reconciles reorganizations, and waits normally when caught up. A missing required block, log range or historical state produces a chain-specific error identifying the height; an upstream behind its saved checkpoint is rejected at startup. All chains already configured at startup must pass validation before the listener opens. Chains added while running are validated independently; an unavailable new upstream does not stop existing chains.

Settings live in `config.json`; see [config.example.json](config.example.json). Each entry under `chains` contains its own `chainRpc`, deployment, database and indexing settings. `chain add --confirmations` and `--snapshot-interval` set values for that chain; edit individual entries for other policies. Chain settings are reloaded while running; changing `listen` or `maxConcurrentRequests` requires a restart. `confirmations: 0` is for local development. Choose appropriate deployed-chain depths; confirmation depth is not a consensus finality guarantee. `detectionBits` defaults to 8 and `snapshotInterval` to `6h`; use `0s` to disable exports. `listen` defaults to `127.0.0.1:8080`.

## Environment variables

Command-line values override environment variables. Initialization settings are saved in `config.json`; `run --listen` or `INVISI_RPC_LISTEN` can override the saved listener without changing that file. An optional positional directory on `init` takes precedence over `--dir` and the environment.

```sh
export INVISI_RPC_DATA_DIR=./invisi-protocol-rpc
export CHAIN_RPC_URL=http://127.0.0.1:8545
export INVISI_RPC_LISTEN=127.0.0.1:8080

./bin/invisi-protocol-rpc init
./bin/invisi-protocol-rpc chain add 31337
./bin/invisi-protocol-rpc run
```

| Environment variable | CLI option / purpose |
| --- | --- |
| `INVISI_RPC_DATA_DIR` | `--dir`; default `./invisi-protocol-rpc` |
| `CHAIN_RPC_URL` | `chain add/set-rpc --rpc` |
| `INVISI_RPC_DEPLOYMENT_FILE` | `chain add --deployment-file` |
| `INVISI_RPC_DATABASE` | `chain add --database`; default `sqlite` |
| `INVISI_RPC_DATABASE_URL`, `DATABASE_URL` | `chain add --database-url`; PostgreSQL URL |
| `INVISI_RPC_LISTEN` | `init/run --listen` |
| `INVISI_RPC_DETECTION_ENABLED` | `chain add --detection-enabled`; default `true` |
| `INVISI_RPC_CONFIRMATIONS` | `chain add --confirmations` |
| `INVISI_RPC_SNAPSHOT_INTERVAL` | `chain add --snapshot-interval` |
| `INVISI_RPC_SNAPSHOT` | `chain add --snapshot`; local snapshot file |
| `INVISI_RPC_SNAPSHOT_SHA256` | `chain add --snapshot-sha256`; expected hash |
| `INVISI_RPC_SNAPSHOT_MAX_BYTES` | Import/inspect decompressed size limit |
| `INVISI_RPC_CHAIN` | `snapshot export --chain` |
| `INVISI_RPC_SNAPSHOT_OUTPUT` | `snapshot export --output` |

Environment variables are read from the process; `.env` files are not loaded automatically.

## Database selection and chain isolation

SQLite creates a database per chain, with WAL, serialized writes and independent readers. PostgreSQL is available for shared deployments:

```sh
docker compose up -d postgres
export DATABASE_URL='postgres://invisi:local-development-only@127.0.0.1:5432/invisi?sslmode=disable'

./bin/invisi-protocol-rpc init ./data-pg
./bin/invisi-protocol-rpc chain add 31337 --dir ./data-pg \
  --rpc http://127.0.0.1:8545 --database postgres

./bin/invisi-protocol-rpc run --dir ./data-pg
```

Set `--database-url POSTGRES_URL` separately when adding each chain. `DATABASE_URL` is the fallback when no explicit URL is supplied. **Use one database per chain operationally**, for example PostgreSQL databases `invisi_1` and `invisi_42161`, to isolate backups, rebuilds and load. Every backend also isolates records by `chainId + protocolAddress`, so sharing a PostgreSQL database is supported. There is no separate set of tables per chain.

## Snapshots and bootstrap

Snapshots are portable between SQLite and PostgreSQL. They contain public block/event records and deployment metadata; restoration reconstructs versioned public trees and nullifier state in one atomic database transaction. **No Chain RPC calls are made for blocks before the restored checkpoint.** Local reconstruction cost still grows with snapshot history.

A running service exports every configured interval when its checkpoint changes. Snapshots are separated by chain ID and Protocol address. Export is transaction-consistent while indexing continues; files, SHA-256 sidecars and `latest.json` are published atomically. Existing snapshots are retained; operators choose their retention policy.

```sh
# Export on demand. Prints the snapshot path.
./bin/invisi-protocol-rpc snapshot export --chain 31337

# Check format, deployment and checkpoint.
./bin/invisi-protocol-rpc snapshot inspect /trusted/index.snapshot.json.gz

# Bootstrap a fresh directory and resume from the checkpoint.
./bin/invisi-protocol-rpc init ./new-data
./bin/invisi-protocol-rpc chain add 31337 --dir ./new-data \
  --rpc http://127.0.0.1:8545 \
  --snapshot /trusted/index.snapshot.json.gz \
  --snapshot-sha256 YOUR_TRUSTED_SHA256

./bin/invisi-protocol-rpc run --dir ./new-data
```

Use `chain add --snapshot FILE --snapshot-sha256 HASH` for each chain to restore. Snapshot deployments must match the catalog. `snapshot export --chain` is required when multiple chains are configured and optional for one chain.

`--snapshot-sha256` checks the whole compressed file; otherwise an adjacent `.sha256` file is used when present. Obtain snapshots and checksums from a trusted publisher. A checksum detects corruption and authenticates only when its expected value came from a trusted source. Import rejects mismatched chain, Protocol, runtime hash, start block, source profile, discontinuous blocks, invalid footer, truncation and malformed gzip. Failure rolls back that chain's entire import. Retrying `chain add` reuses a previously completed import only when its complete persisted data matches the requested snapshot; differing populated targets are rejected. Configuration is published only after that chain’s database preparation succeeds.

Default decompressed size limit is 8 GiB, with 64 MiB per record. For larger published snapshots, use `chain add --snapshot-max-bytes <bytes>` and `snapshot inspect --max-bytes <bytes>`. Transport/distribution is external: copy or download files first, then pass a local path. Startup checks the canonical saved checkpoint and current roots against on-chain `rootHistory` before serving restored state. If the upstream has pruned the required checkpoint or subsequent state, use an RPC that retains those heights or a newer snapshot.

Snapshots replay stored normalized batches and are not a trustless proof of complete event history. Clients independently verify commitments, receipts, paths, known roots and spent state. A reorganization is reconciled by rolling back to an indexed common ancestor; a reorganization of the deployment starting block requires a reviewed reset.

## JSON-RPC

The service namespace is `/v1`. `GET /v1` returns `apiVersion` and active chain routes; `invisi_getVersion` and `invisi_getStatus` also return `apiVersion: "v1"` and the chain-read `protocolVersion: "1"`. The deployment's `VERSION()` is read and checked at startup; incompatible versions are rejected before HTTP publication.

POST JSON-RPC 2.0 to `/v1/chains/<chainId>` using positional parameter arrays. `GET /v1` provides chain discovery; the discovery path is not a JSON-RPC endpoint. Addresses, hashes, ciphertexts, clues and detection keys use `0x` hex. Block numbers, tree IDs and indices are JSON integers; ABI uint256 values inside decoded event payloads are decimal strings to preserve precision. Full blocks also retain original EVM logs.

| Method | Parameters | Result |
| --- | --- | --- |
| `invisi_getVersion` | `[]` | API and on-chain Protocol versions |
| `invisi_getDeployment` | `[]` | Protocol and optional registry deployment profile, API and Protocol versions; available before the first indexed block |
| `invisi_getStatus` | `[]` | Deployment, indexed head, detection settings |
| `invisi_getBlock` | `[blockNumber]` | Complete indexed block, raw logs and normalized data |
| `invisi_getEvents` | `[range]` | Events in canonical log order |
| `invisi_getRoots` | `[treeId]` | Up to 10,000 recent recorded roots, newest first |
| `invisi_getMembership` | `[treeId, leafIndex, root]` | Commitment, 16 siblings and historical anchor |
| `invisi_isSpent` | `[nullifier]` | Consumption through the indexed head |
| `invisi_getUpdates` | `[range]` | Public outputs and Credit transitions |
| `invisi_getCreditTransitions` | `[range]` | Credit creation, draw, cancellation and top-up |
| `invisi_detectOutputs` | `[range, detectionKey]` | FMD candidates and Credit transitions |

A range is `{ "fromBlock": 100, "toBlock": 110, "blockHash": "0x…" }`. The hash must match indexed `toBlock`, obtained through `getBlock` or `getStatus`. Queries accept at most 100 blocks and 10,000 results; overflow returns an error. Use smaller ranges or full blocks. Reorganizations invalidate old anchors; recheck when combining responses.

```sh
curl http://127.0.0.1:8080/v1/chains/31337 \
  -H 'Content-Type: application/json' \
  --data '{"jsonrpc":"2.0","id":1,"method":"invisi_getStatus","params":[]}'
```

Live indexing stores contract-recorded **batch-final roots**, never per-leaf intermediate roots. Each new root is checked against `rootHistory(treeId, root)` at the exact block before committing. Historical nodes are versioned by leaf count; events, outputs, nullifiers, roots and checkpoints commit or roll back together. The service does not calculate a user's private balance or prove response completeness.

## Optional delegated FMD

`chain add` enables FMD by default. Set `--detection-enabled=false` when adding a chain, or set its `detectionEnabled` configuration to `false`, to disable `invisi_detectOutputs`. A configuration entry without this field disables detection. This switch is independent of API authentication. Supply only a detection key, never a viewing or spending private key. Detection keys are not persisted and request bodies are not logged.

Senders generate 24-bit, 68-byte clues. The service checks a configurable 1–24-bit prefix (`detectionBits: 8` by default). Public preparation is cached; detection remains key-specific. Two scans may run concurrently, with at most 1,000 deliveries per request, independently of indexing.

Malformed/unsupported clues are returned with `fallback: true` for local inspection. Credit transitions are returned separately because remainder recovery may need a parent/shared key even when the child envelope cannot be decrypted. Clients decrypt and verify candidates. A valid but deliberately unrelated clue cannot be classified as malformed; complete recovery retrieves events without FMD filtering.

Detection is request-driven. Persistent per-user delegation queues and WebSocket delivery are not implemented. Applications manage cursors, retries and reorg handling.

## HTTP gateway and overload handling

The service accepts public requests. Optional API authentication and per-IP or per-issued-key rate limits belong to the HTTP gateway. An API key authorizes service access; it is unrelated to a token contract or a wallet detection key. See [the Nginx example](deploy/nginx.conf.example): public access is enabled, and a commented guard enables private access. Replace the placeholder key before enabling that guard. Serve production traffic over HTTPS and restrict direct access to the backend when gateway authentication is required. Behind another proxy, configure trusted proxy addresses before using forwarded client IPs.

The example limits HTTP requests by IP or recognized key. It does not meter individual JSON-RPC batch calls or their computational cost. Its rate and connection values are illustrative deployment settings, not benchmark results.

The backend independently limits requests to 1 MiB, batches to 20 calls and batch responses to 16 MiB. Ranged event/update responses are limited to 8 MiB and return an explicit smaller-range error rather than partial data. Query ranges and result limits still apply. `maxConcurrentRequests` defaults to 64 (allowed range 1–4096) across all chains on the listener. Exhausting this budget returns HTTP 429 with `Retry-After: 1`. Each chain also allows two concurrent FMD scans; exhausting that budget returns JSON-RPC error `-32005` with `data.retryAfterSeconds: 1`, including within an HTTP 200 batch response.

Clients should use bounded exponential backoff with jitter, respecting retry guidance. Authentication failures and invalid requests require correction rather than automatic retries.

## Development

| Directory | Responsibility |
| --- | --- |
| `cmd/main.go` | Process entry point |
| `internal/app`, `internal/config` | CLI, initialization, lifecycle and settings |
| `internal/indexer` | Chain verification, confirmed blocks and reorgs |
| `internal/protocol` | Pinned ABI and event normalization |
| `internal/cryptography`, `internal/merkle` | Poseidon2, Decaf377, FMD and public paths |
| `internal/storage`, `internal/snapshot` | SQLite/PostgreSQL and portable bootstrap |
| `internal/rpc` | HTTP queries, limits and detection |

The embedded ABI is synchronized with the current Protocol build; snapshot metadata pins its SHA-256 and Protocol v1 profile. Crypto source references and independent Noir/Barretenberg/Rust vectors are retained. Go FMD uses variable-time arithmetic and has not been independently audited.

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

Encrypted output memos are opaque to this service. Funds envelopes use version 2 and 160–416 bytes; Credit envelopes use version 5 and 498–754 bytes. Both allow up to 256 encrypted memo bytes. Aggregate outputs frame each delivery as a two-byte big-endian ciphertext length, ciphertext, then a 68-byte FMD clue. The decoder validates framing and preserves each output's token, commitment and leaf position; wallet SDKs decrypt and interpret memo data.

### Credit recovery block context

Every indexed event and output candidate includes `block` with `number`, `hash`, `parentHash`, and `timestamp` (uint64 Unix seconds). The timestamp comes from the confirmed canonical header whose hash is used to fetch logs during indexing. Credit draw recovery must use the transition block timestamp to derive UTC+0 Gregorian day/month usage; never use local wall-clock time or caller-supplied dates. Draw transaction parameters contain no date fields. The private per-grant limits and counters remain encrypted; this service does not decrypt them. Credit draw proof widths are 20/86/314/734 fields for capacities 1/12/50/120.

Index and snapshot format 1 persist this block context in SQLite and PostgreSQL. Block records require an explicit uint64 timestamp, including zero for the Unix epoch; malformed records and unsupported formats are rejected. Snapshots use the Credit limits source profile. Contract/API version remains 1.

Portable snapshots retain the existing trusted publisher/checksum model. Historical timestamps restored from a snapshot inherit that trust; hash continuity and a checkpoint hash alone do not authenticate arbitrary historical normalized records. The saved checkpoint timestamp is checked against its canonical chain header before serving.

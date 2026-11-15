# Invisi Protocol RPC command reference

This reference describes the executable CLI. JSON-RPC methods such as `invisi_getNote` are HTTP API methods, not shell subcommands; see the [API reference](api.md).

## Build and command tree

Use Go 1.26+ to build; a released binary requires no Go installation or C runtime. SQLite is embedded. PostgreSQL deployments require PostgreSQL 16+. Initialization and help need no network, deployment catalog or database server; running a configured chain requires an EVM Chain RPC with the required historical state.

```sh
go build -o ./bin/invisi-protocol-rpc ./cmd
./bin/invisi-protocol-rpc --help
```

```text
invisi-protocol-rpc [--dir DATA_DIR] COMMAND
  init [DATA_DIR] [--listen HOST:PORT]
  run [--listen HOST:PORT]
  chain
    add ID --rpc URL [options]
    remove ID
    set-rpc ID --rpc URL
    list
  snapshot
    export [--chain ID] [--output DIRECTORY]
    inspect SNAPSHOT_FILE [--max-bytes BYTES]
  help [COMMAND...]
```

`--help` / `-h` works at the root, group and leaf levels. Root/group invocation without a command displays help. There is no CLI version flag or signing/wallet command. The service indexes public data only. Unlike Provenance's chain file input, RPC selects a canonical positive decimal chain ID from a deployment catalog.

## Directory selection and argument parsing

`--dir` is inherited by every command and can appear before the command, between group and leaf, or after leaf arguments. The existing `-dir` spelling is also accepted. Later explicit directory flags override earlier explicit flags. Directory precedence is:

1. Optional `init DATA_DIR` positional argument.
2. Explicit `--dir` (the closest/later explicit value wins).
3. `INVISI_RPC_DATA_DIR`.
4. `./invisi-protocol-rpc`, relative to the current working directory.

An empty environment value, explicit `--dir ""`, or empty positional init directory uses the documented default, never the working directory itself. Relative paths are resolved from the current working directory. Flags remain recognized after positional arguments:

```sh
./bin/invisi-protocol-rpc --dir ./ancestor init ./chosen --dir ./leaf --listen 127.0.0.1:8090
# Initializes ./chosen with listener 127.0.0.1:8090.
./bin/invisi-protocol-rpc --dir ./chosen chain list
./bin/invisi-protocol-rpc chain --dir ./chosen list
./bin/invisi-protocol-rpc chain list --dir ./chosen
```

`init` accepts at most one positional directory; `run`, `chain list` and `snapshot export` accept none. Chain mutations and snapshot inspection require exactly one positional argument. Unknown commands/flags, excess arguments and help do not initialize a service, open its database or contact upstreams. No config JSON import is supported: `init config.json`, a directory ending in `.json` (case-insensitive), and an existing regular-file directory target are rejected. `--` ends flag parsing when a positional path begins with `-`.

## Initialization, listener and shutdown

```sh
./bin/invisi-protocol-rpc init ./data --listen 127.0.0.1:8080
./bin/invisi-protocol-rpc run --dir ./data
./bin/invisi-protocol-rpc run --dir ./data --listen 0.0.0.0:8080
```

`init` saves an empty chain list and the listener (default `127.0.0.1:8080`), creates private directories (0700) and configuration (0600), and refuses to overwrite an existing `config.json`. It generates no wallet keys. Keep RPC and PostgreSQL credentials in this private directory; do not commit them. A `.config.lock` coordinates atomic configuration publication. Existing directory permissions are not reset by initialization.

For `init`/`run`, explicit `--listen` overrides nonempty `INVISI_RPC_LISTEN`. Initialization saves the selected listener. Running uses the flag or nonempty environment value in memory only, otherwise the saved listener; it never rewrites `config.json` for a runtime override. An absent flag and empty listener environment fall back to the init default or saved runtime listener. An explicit empty `--listen ""` is rejected before reading or creating service state. Addresses must use host:port syntax with a numeric port from 1 through 65535; IPv6 uses brackets, for example `[::1]:8080`. Invalid overrides fail before configuration reads; invalid saved listeners fail before databases/network initialization. Hosts may be empty, DNS names or IP addresses, but cannot contain slashes, backslashes, whitespace or control characters. Address availability and hostname resolution are checked when binding.

`run` resumes indexing and stays in the foreground. Stop with Ctrl-C, SIGINT or SIGTERM; the process cancels its workers and closes its listener and databases. There is no `stop` command. An empty initialized service can run while waiting for chains. Protect any externally exposed listener with appropriate operator access controls.

## Chain and snapshot flags

Every leaf inherits `--dir`; every command supports help. `ID` must be a canonical positive decimal integer (for example `31337`, never `031337`).

| Command | Option | Default / effect |
| --- | --- | --- |
| `chain add`, `chain set-rpc` | `--rpc URL` | Required; HTTP(S) or WS(S) URL with host; `CHAIN_RPC_URL` fallback |
| `chain add` | `--deployment-file PATH` | `./deployment.json`; Protocol catalog, not initialized service config |
| `chain add` | `--database DRIVER` | `sqlite`; also `postgres` |
| `chain add` | `--database-url URL` | Empty; PostgreSQL only, environment fallbacks below |
| `chain add` | `--snapshot FILE` | Empty; local portable snapshot bootstrap |
| `chain add` | `--snapshot-sha256 HASH` | Empty; requires `--snapshot`; 64 hex digits, optional `0x` |
| `chain add` | `--snapshot-max-bytes BYTES` | 8589934592 (8 GiB); positive and below MaxInt64 |
| `chain add` | `--snapshot-interval DURATION` | `6h`; nonnegative Go duration, `0s` disables automatic export |
| `snapshot export` | `--chain ID` | Optional for one saved chain; required for multiple chains |
| `snapshot export` | `--output DIRECTORY` | `DATA_DIR/snapshots` |
| `snapshot inspect` | `--max-bytes BYTES` | 8589934592 (8 GiB) decompressed limit |

`chain add` prepares the per-chain database and optionally restores the snapshot before publishing configuration. Duplicate IDs fail. `chain set-rpc` changes only the upstream URL; `chain remove` removes configuration without deleting database files or snapshots. Both require an existing configured ID. `chain list` emits a JSON array containing saved `id`, `protocol`, `rpcPathV1` and `rpcHost`; only the upstream hostname is disclosed, not URL credentials, paths or query tokens. Snapshot inspection emits JSON header metadata; export prints the resulting snapshot path. Successful mutations print an operator confirmation.

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

Optional `registry` and `broadcasterRegistry` entries use the same `{address,runtimeCodeHash,fromBlock}` contract shape as `protocol`. Set them in the deployment catalog before `chain add`; they are copied into the initialized configuration. For a stopped existing installation, review and set the deployment metadata explicitly rather than expecting later catalog edits to retarget it. All configured event source addresses must be distinct. No registry address is built into the service. The contiguous event stream begins at the earliest configured deployment block, and registration updates commit and roll back with their blocks. Changing/adding registry pins to a populated index requires a reviewed fresh database; configuration changes must never silently omit earlier registrations.

Index format 3 and snapshot format 2 retain explicit canonical block timestamps, typed Note/Credit projections and registry histories. The current `invisi-protocol-v1-postpaid-credit-typed` snapshot source profile pins the Protocol, receiving Registry and Broadcaster Registry ABIs and all configured deployments. Incompatible experimental databases and older snapshot source profiles must be rebuilt/exported with the current indexer; the service never deletes or resets them automatically. Historical normalized records restored from a snapshot inherit publisher trust. Startup checks the saved checkpoint timestamp against its canonical header before serving.

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

Settings live in `config.json`; see [config.example.json](config.example.json). Each entry under `chains` contains its own `chainRpc`, deployment, database and indexing settings. `chain add --snapshot-interval` sets the snapshot policy for that chain; edit individual entries for other policies. Chain settings are reloaded while running; changing `listen` or `maxConcurrentRequests` requires a restart. Indexing follows canonical `latest` immediately and reconciles reorgs. The old `confirmations` config field and CLI flag have been removed; remove this field from existing config files before upgrading. `snapshotInterval` defaults to `6h`; use `0s` to disable exports. `listen` defaults to `127.0.0.1:8080`.

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


## Upgrades, failures and exit status

There is no automatic migration, deletion or reset of service directories. Reuse an existing installation explicitly with `--dir /existing/path` and back it up before upgrades. The old `confirmations` configuration field and CLI flag were removed; remove that field before upgrading. Incompatible database/snapshot source profiles require a reviewed rebuild/export with the current indexer; initialization never replaces existing data. Deployment pins, snapshot profiles and finalized-history fences remain security boundaries. Follow the [API native-finality recovery guidance](api.md#canonical-settlement-facts) for a sticky finalized-history conflict rather than resetting its anchor.

The executable returns status 0 for successful commands/help or graceful shutdown and nonzero (normally 1) for parsing, validation, configuration, database, snapshot or upstream errors. Errors are printed to stderr. Failed imports roll back their database transaction; files prepared before a later operational failure may remain for safe retry. There is no automatic rollback of already completed database preparation on subsequent configuration publication failure. No RPC network is needed for `chain add` itself, but it requires an initialized directory and a valid deployment catalog; `run` performs upstream authentication.

Container volume setup and image provenance remain in the [README](README.md#container-images). Running tests and optional PostgreSQL/EVM integration dependencies remain in [Development](README.md#development).

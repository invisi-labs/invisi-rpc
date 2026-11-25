# Invisi RPC

The latest release is [v1.3.6](https://github.com/invisi-labs/invisi-rpc/releases/tag/v1.3.6), with authenticated transaction sender reads, bounded native settlement batching and unchanged public JSON-RPC/cursor contracts. See [measured catch-up results](performance.md).

Standalone Go service for public Invisi data: canonical events, shared Note Trees, historical membership paths. Depends only on the Go SDK protocol module, not the wallet SDK. Wallet clients retain decryption, private note storage, authorization and proving, and verify results through an independent Chain RPC.

```text
invisi-rpc (chain authentication / indexing / pagination)
  └── invisi-sdk-go/protocol v1.3.6
      └── settlement (canonical ABI and operation binding)
```

The service does not depend on the wallet SDK. Shared settlement fixtures are maintained in `../invisi-sdk-go/protocol/settlement`; regenerate them from that module with `node generate.mjs`.

## Release downloads

Service and SDK version numbers are independent. This development branch consumes Go SDK v1.3.6; see [CHANGELOG.md](CHANGELOG.md) and `go.mod` for the dependencies of each service revision.

Pushing a stable `vX.Y.Z` tag runs tests and publishes the multi-platform GHCR image and GitHub Release binaries from the same tagged source. Manual workflow dispatch accepts an existing tag. Release notes include the complete matching section from the tagged CHANGELOG.md, followed by image digest and artifact details.

Release archives are named `invisi-rpc_vX.Y.Z_<os>_<arch>`, covering Linux, macOS (`darwin`) and Windows on `amd64` and `arm64`. Linux/macOS use `.tar.gz`; Windows uses `.zip`. Each archive contains the CLI, LICENSE, README, the API reference `api.md` and the CLI reference `cammand.md` when available in the tagged source. Binaries require no Go installation or C runtime. Download `SHA256SUMS-vX.Y.Z.txt` to verify archive checksums before extraction. Configure the deployment catalog and chain RPC as described below; credentials and local configuration are never bundled.

## Initialize and run

Building from source requires Go 1.26.1 or later. Released binaries need no Go installation. Running requires an EVM Chain RPC for each selected chain and either SQLite (default, embedded) or PostgreSQL 16+. The CLI uses [urfave/cli v3](https://cli.urfave.org/v3/).

```sh
go build -o ./bin/invisi-rpc ./cmd

# Use the catalog published by the Protocol deployment tooling.
export INVISI_RPC_DEPLOYMENT_FILE=../invisi-core/deployment/deployment.json
./bin/invisi-rpc init
./bin/invisi-rpc chain add 31337 --rpc http://127.0.0.1:8545
./bin/invisi-rpc run
```

The default data directory is `./invisi-rpc`. Set `INVISI_RPC_DATA_DIR` or pass `--dir` to select another directory. `init` also accepts an optional positional directory. The executable is built under `bin/` so it does not conflict with the data directory.

Full command syntax, flags, environment variables, database setup, snapshot bootstrap and operational examples are in [cammand.md](cammand.md).

`init` creates an empty configuration without contacting a Chain RPC and refuses to overwrite it. Chain changes are locked, published atomically and reloaded while running. Removing a chain retains its database and snapshots. Each chain has independent state and shares the HTTP listener; `GET /v1` lists active routes at `/v1/chains/<chainId>`.

The canonical [deployment catalog](../invisi-core/deployment/deployment.json) is owned by Protocol deployment tooling. No deployment records are embedded in this binary. `chain add` copies the selected deployment pins into the configuration; later catalog edits do not retarget an initialized node. Startup authenticates each upstream chain ID, runtime code and saved checkpoint before serving. The current catalog contains a controlled local deployment with mock verifiers.

RPC v1.3.1 requires the unified Core receipt events `NoteOperation`, `NoteOutputs` and `NoteStateTransition`; matching function selectors alone do not establish compatibility with older Core deployments. The BSC Testnet instance verified on 2026-11-20 is `0xF07174b5d9998456F1C6c03d12CbC071595Df07A`, from block `143673444`, with runtime code hash `0xadbbe6e0adb3f63f10c46d51e05881dd153948cffe8373de60171859fd5ffb68`. Use the [immutable deployment catalog](https://github.com/invisi-labs/invisi-core/blob/3dbf69e65a02599392e012ee0f46a2b4b2dd0bee/deployment/deployment.json) and [deployment record](https://github.com/invisi-labs/invisi-core/blob/3dbf69e65a02599392e012ee0f46a2b4b2dd0bee/deployment/chain97-20261120.json) for that instance, rather than assuming an arbitrary `main` catalog matches the running services. The deployment used Core source `f2b8bbbc316099f1305741584a356dd05d200864`; Core `627277ba45c10394b63c991602246f7f5b918c6c` has the same contract sources and Transact/Credit circuits. Pin the complete deployment, including registry addresses and start blocks, before initializing an index.

Snapshots contain public normalized history and can bootstrap SQLite or PostgreSQL. Obtain snapshots and expected checksums from a trusted publisher: a checksum alone does not authenticate historical records. Imports are atomic and deployment-bound; clients retain independent chain verification. See the [snapshot trust and bootstrap reference](cammand.md#snapshots-and-bootstrap). No keys, private notes or wallet secrets belong in this service.

## Public API and trust

`GET /v1` discovers active chain routes. POST JSON-RPC 2.0 with positional arrays to `/v1/chains/<chainId>`. API namespace and Protocol version remain `v1` / `1`; the deployment address, runtime hash and start block identify the selected pool. Call `invisi_getDeployment` and pin that identity before use. Full routes, method parameters/results, errors, paging, finality, gateway selectors, curl examples and source/test references are in [api.md](api.md).

The read-only API provides status/deployment, indexed blocks/events, physical roots/membership, separate Note and Credit queries, registered receiving keys/Broadcasters, canonical settlement operations and a restricted chain-read gateway. Removed mixed/detection methods are not available. Wallets detect/decrypt locally; no account detection key, wallet secret, private note or proof witness belongs on this service.

At the pinned receiving Registry, the chain-read gateway accepts only canonical SDK ABI calls to `isRegistered(bytes32)`, `nonceOf(bytes32)`, `receivingKeysOf(bytes32)` and `CHAIN_ID()`. The `bytes32 walletIdentity` for a Core EVM owner is its address zero-padded on the left to 32 bytes. The indexed `invisi_getReceivingAddress` API still takes a nonzero 20-byte owner address. Registry writes and other getters are outside the gateway whitelist; Protocol `VERSION()` remains `"1"`.

Clients independently authenticate roots and chain facts unless deliberately choosing trusted `invisi_getNote` safe/finalized confirmation or the trusted WalletSession read gateway. These modes trust this RPC and its upstream; they are not light-client proofs or spending-authority proofs. Native finality has no depth fallback. Native heads can lead indexing; a durable finality anchor advances only when its block matches the indexed branch, allowing normal optimistic reorg recovery. Single-block and batch commits bind validation to the transaction's durable anchor and retry stale snapshots. Pending, null or missing indexed data never proves payment absence or completion. Consumers bind commitments to their private receiving materials and enforce one-time redemption locally. Snapshots inherit publisher trust, not authenticity from checksums alone.

Note outputs and Credit authority state remain distinct. Credit creation is not principal funding, gross grant usage may differ from net Note value after authorized fees, and revocation is separate from state consumption. Registered receiving keys require service trust or independent chain verification. Broadcaster registration/enabled status is not an endorsement or live health guarantee; verify signed quotes independently. Canonical operation replay is bounded and cannot discover every indirect reverted call without traces. Credit usage recovery uses canonical block timestamps, never local wall-clock dates.

The backend is public by default. Put production traffic behind HTTPS and, where needed, gateway authentication and identity quotas. The [Nginx example](deploy/nginx.conf.example) includes optional access controls and CORS; gateway keys authorize access and are unrelated to wallet keys. Backend limits are 1 MiB bodies, 20-call batches, 16 MiB batch responses, 8 MiB ranged results, and a default 64 concurrent HTTP requests. Overload returns HTTP 429 with `Retry-After: 1`; back off with jitter. Full limits and transport behavior are in [api.md](api.md#http-routes-and-transport).

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

Core and receiving-registry event schemas come directly from `invisi-sdk-go/protocol/contracts`; RPC does not keep a second copy of those ABIs. `go generate ./internal/protocol` regenerates only the Broadcaster Registry schema, which is not yet exported by that Go SDK module. Snapshot metadata pins the combined event schema; the immediately preceding unified-receipt fingerprint remains readable, preserving historical administrative events after the screening hook is removed. Crypto source references and independent Noir/Barretenberg/Rust vectors are retained.

```sh
go test ./...
go vet ./...
go build -o ./bin/invisi-rpc ./cmd

# Include PostgreSQL integration and cross-backend snapshots.
TEST_DATABASE_URL='postgres://invisi:local-development-only@127.0.0.1:5432/invisi?sslmode=disable' go test -race ./...

# Include the real local contract lifecycle test on an existing local deployment.
TEST_CHAIN_RPC_URL=http://127.0.0.1:8545 TEST_DATABASE_URL='postgres://invisi:local-development-only@127.0.0.1:5432/invisi?sslmode=disable' go test ./internal/integration
```

Finalized-history catch-up uses bounded batches (default `indexBatchSize: 64`), with the existing sequential path near the head. See [catch-up design, benchmarks and validation](performance.md) for configuration, measured results and limits.

SQLite tests run by default. PostgreSQL/EVM tests explicitly skip without their environment variables. EVM tests require loopback chain 31337 with the contract repository's local deployment; its mock verifiers do not establish real proving correctness.

### Container images

Release workflows publish versioned images to `ghcr.io/invisi-labs/invisi-rpc` for `linux/amd64` and `linux/arm64`:

```sh
docker pull ghcr.io/invisi-labs/invisi-rpc:X.Y.Z
docker run --rm ghcr.io/invisi-labs/invisi-rpc:X.Y.Z --help
```

Pushing a stable `vMAJOR.MINOR.PATCH` tag runs Go tests and vet, then builds and publishes the corresponding image tag without the leading `v`. The **Publish container image** workflow also accepts an existing tag through its manual `tag` input. It compiles that tag's source with the workflow's Dockerfile, includes the Apache license, and records the tagged source commit in its OCI revision label and workflow release summary, alongside the image digest. Source commit identity does not constrain snapshot import. Use the digest to pin an exact image in production. Images have their own visibility settings; public images support anonymous pulls even while the source repository is private.

The Docker image contains no deployment catalog. Mount `invisi-core/deployment/deployment.json` or a private deployment output read-only at `/deployment.json`; the image sets `INVISI_RPC_DEPLOYMENT_FILE` to that path. It sets `INVISI_RPC_DATA_DIR=/data` and runs `run`. Mount a data directory writable by the non-root container user (UID/GID 65532), and set `INVISI_RPC_LISTEN=0.0.0.0:8080` inside the container. Initialize the volume with `init /data`, then use `chain add ID --dir /data --rpc URL` in the same mounted volume. Chain management commands may run in a separate container sharing the volume. Keep PostgreSQL credentials out of images.

## Contributing

Open an issue to discuss bugs or substantial changes, and submit focused pull requests with a clear description and relevant tests. Run `go test -race ./...` and `go vet ./...` before submitting; see the development or verification instructions above for optional integration prerequisites.

Use synthetic fixtures and redact logs: never post private keys, credentials, wallet secrets or private witnesses in issues, pull requests or test data. Preserve existing third-party licenses and attribution notices. This project is licensed under [Apache-2.0](LICENSE).

## Persistent runtime names

The project, CLI and future release artifacts use `invisi-rpc`. New installations default to `./invisi-rpc`. Existing deployments must explicitly retain their current data path until the release/deployment migration is performed; no data is moved automatically. Select the data path with `--dir` or the service data-directory environment variable.

## GitHub namespace migration

The source uses the published Go SDK v1.3.6 modules under `github.com/invisi-labs`. Container workflows target `ghcr.io/invisi-labs`.

## Local SDK development

Release and standalone builds resolve the published Go SDK v1.3.6 modules. No sibling SDK checkout, committed module replacements or named Docker build context is required.

```sh
GOWORK=off go list -m -json github.com/invisi-labs/invisi-sdk-go/protocol
GOWORK=off go test ./...
GOWORK=off go build ./...
```

The module listing must report version `v1.3.6` without a local replacement. For SDK source development, use an optional ignored `go.work` containing the service and the SDK modules being edited. Keep machine-specific workspace paths uncommitted and use `GOWORK=off` to verify release dependencies.

Container builds use the published modules:

```sh
docker buildx build -t invisi-rpc:local .
```

RPC imports public protocol modules and does not construct a prover or download proving resources. The SDK verification-key changes require no additional RPC configuration.


Current Credit indexing follows the committed SDK uint128 grant-ID ABI: `invisi_getCreditStatus(owner, token, decimalGrantId, atBlock)` returns canonical block metadata and status 0 (unknown), 1 (active), or 2 (permanently disabled). IDs are positive canonical decimal strings, scoped by chain, Core, owner and token; pre-revoke is supported and IDs cannot be reused. Fee-free zero-token pre-revocations are indexed and queryable without blocking checkpoint progress; grant owners remain nonzero. The retired allocation and bitmap endpoints are removed. Protocol/API version remains 1.

Index and snapshot format 4 reject legacy shard/slot history. Reset or recreate development indexes and snapshots for the freshly deployed current Core; legacy IDs cannot be inferred or converted. The historical testnet deployment above is provenance for that instance, not an assertion that it supports the current Credit ABI. See [API compatibility and upgrade policy](api.md).

## Peer-IP request limits

The optional top-level `rateLimit` configuration is shared across RPC, Provenance and Broadcaster:

```json
{
  "rateLimit": {
    "requestsPerMinute": 0,
    "exemptCIDRs": ["192.0.2.8", "2001:db8::/64"]
  }
}
```

The default is 0 (disabled), retaining the existing public frequency behavior. The global maxConcurrentRequests limit remains active. A positive value enables a shared per-peer-IP HTTP quota across all chains; a JSON-RPC batch counts as one HTTP request.

Omitting `rateLimit` or `requestsPerMinute` keeps the service default; an explicit zero disables the configurable frequency bucket. Exemptions default to an empty list. Literal IPv4/IPv6 addresses and CIDRs are supported, including canonical IPv4-mapped addresses. Invalid entries or rates outside 0–1,000,000 are rejected. At most 256 exemption entries may be configured.

Only the connection's `RemoteAddr` is used for quota identity and exemptions. `X-Forwarded-For`, `X-Real-IP`, `Forwarded` and similar client-supplied headers cannot grant an exemption. A reverse proxy is counted as the client under this default policy; true end-user IP processing requires a separately authenticated trusted-proxy layer. Keep Gateway/node networks out of the exemption list. Use firewall or NetworkPolicy rules to restrict direct access to any exempt workload network.

An exemption skips frequency admission only; it does not grant administrative access or remove any concurrency, queue, request-size, timeout or business-validation bound. Restart RPC/Provenance to load changes to listener-level settings.

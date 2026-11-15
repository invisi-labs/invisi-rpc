# Invisi Protocol RPC

Standalone Go service for public Invisi data: canonical events, shared Note Trees, historical membership paths. Depends only on the Go SDK protocol module, not the wallet SDK. Wallet clients retain decryption, private note storage, authorization and proving, and verify results through an independent Chain RPC.


```text
invisi-protocol-rpc (chain authentication / indexing / pagination)
  └── invisi-sdk-go/protocol v1.2.4
      └── settlement (canonical ABI and operation binding)
```

The service does not depend on the wallet SDK. Shared settlement fixtures are maintained in `../invisi-sdk-go/protocol/settlement`; regenerate them from that module with `node generate.mjs`.


## Release downloads

Pushing a stable `vX.Y.Z` tag runs tests and publishes the multi-platform GHCR image and GitHub Release binaries from the same tagged source. Manual workflow dispatch accepts an existing tag. Existing Release notes are preserved when assets are uploaded.

Release archives are named `invisi-protocol-rpc_vX.Y.Z_<os>_<arch>`, covering Linux, macOS (`darwin`) and Windows on `amd64` and `arm64`. Linux/macOS use `.tar.gz`; Windows uses `.zip`. Each archive contains the CLI, LICENSE, README, the API reference `api.md` and the CLI reference `cammand.md` when available in the tagged source. Binaries require no Go installation or C runtime. Download `SHA256SUMS-vX.Y.Z.txt` to verify archive checksums before extraction. Configure the deployment catalog and chain RPC as described below; credentials and local configuration are never bundled.

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

## Public API and trust

`GET /v1` discovers active chain routes. POST JSON-RPC 2.0 with positional arrays to `/v1/chains/<chainId>`. API namespace and Protocol version remain `v1` / `1`; the deployment address, runtime hash and start block identify the selected pool. Call `invisi_getDeployment` and pin that identity before use. Full routes, method parameters/results, errors, paging, finality, gateway selectors, curl examples and source/test references are in [api.md](api.md).

The read-only API provides status/deployment, indexed blocks/events, physical roots/membership, separate Note and Credit queries, registered receiving keys/Broadcasters, canonical settlement operations and a restricted chain-read gateway. Removed mixed/detection methods are not available. Wallets detect/decrypt locally; no account detection key, wallet secret, private note or proof witness belongs on this service.

Clients independently authenticate roots and chain facts unless deliberately choosing trusted `invisi_getNote` safe/finalized confirmation or the trusted WalletSession read gateway. These modes trust this RPC and its upstream; they are not light-client proofs or spending-authority proofs. Native finality has no depth fallback. Pending, null or missing indexed data never proves payment absence or completion. Consumers bind commitments to their private receiving materials and enforce one-time redemption locally. Snapshots inherit publisher trust, not authenticity from checksums alone.

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

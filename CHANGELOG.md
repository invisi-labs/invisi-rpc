# Changelog

## Unreleased

## 1.3.8

- Resolve client quotas through an explicit `rateLimit.trustedProxyCIDRs` allowlist and one Nginx-overwritten `X-Real-IP`; ignore all forwarded headers from other peers. Keep malformed proxy metadata in its own limited peer bucket and never derive Pod exemptions from forwarded identities. Retain concurrency, queues, body bounds, timeouts and business/proof validation.
- Enable the default RPC client-IP quota at 120 HTTP requests/minute across chains; explicit zero remains an operator opt-out.

## 1.3.7

- Upgrade all consumed Go SDK modules to v1.3.6 and refresh verified module checksums. Keep service/API versions, contract deployments and stored data unchanged.

- Add optional `rateLimit.requestsPerMinute` and direct-peer IP/CIDR exemptions with canonical IPv4/IPv6 matching and strict configuration validation. Ignore forwarded headers for quota identity and preserve existing public defaults, concurrency, queues and business checks.

## 1.3.6

- Add `transactionFrom` to transaction chain-state reads, recovered from the signed transaction after hash validation. Reject invalid signatures or protected transactions with a deployment-chain mismatch, while retaining valid unprotected legacy transactions.
- Keep JSON-RPC methods, existing indexes, finality and reorganization handling unchanged. The sender field is a signed transaction fact and does not identify the beneficial funding owner.

## 1.3.5

- Batch native settlement replay reads in bounded groups while preserving per-block chain, runtime-code and canonical identity checks, receipt validation, paging and fresh terminal anchors. Fall back to ordinary reads when native batch transport or batch size is unsupported.
- Verify native transaction/withdrawal body roots, preserve verified transactions when fetching uncle headers, and fetch later block groups only when the page reaches them. Public JSON-RPC methods and cursors remain unchanged.

## 1.3.4

- Guard single-block and range publication against the durable finalized anchor inside the writer transaction, reject rollback across accepted finalized history, and persist conflict fences even before the first anchor. Safe and finalized Note confirmations fail closed while an anchor or fence cannot be verified.
- Advance finalized anchors only when their complete block identity matches the indexed branch. Reconcile optimistic reorgs first; retry checkpoint/generation races, unaccepted range heads and wrong-height upstream anchor responses without creating a permanent conflict fence.
- Align the receiving Registry read gateway with the generated SDK ABI: accept canonical `isRegistered(bytes32)`, `nonceOf(bytes32)` and `receivingKeysOf(bytes32)` calls plus `CHAIN_ID()`. Preserve the full wallet-identity domain and reject noncanonical calldata; indexed receiving-address queries retain their existing owner-address input.
- Add branch and pull-request validation for Go vet/tests, PostgreSQL fixtures and clean committed-source Linux amd64/arm64 container builds with CLI and license smoke checks.
- Build release archives and images with the selected tag's own release script and Dockerfile, including manual publishing, so artifacts remain aligned with their source tag.
- Upgrade the consumed Go SDK protocol and primitives modules to published v1.3.4, using verified public module checksums without local workspace replacements. Existing protocol ABI and proving resources remain unchanged.
- Protocol/API version remains 1; retain existing indexes and durable finality state. A persisted finalized-history conflict still requires the documented operator investigation and recovery.

## 1.3.3

- Preserve canonical ABI field names, including uint128 `grantId`, when reconstructing receipt-bound Credit settlement calldata. Valid batch draws across different owners and Credit updates can be queried without rejection.
- Add public receipt/calldata regression fixtures for draw and update, including high uint128 IDs and mismatched-calldata rejection. Protocol version remains 1; existing indexes are retained.

## 1.3.2

### Credit identity and API compatibility

- Replace shard/slot allocation and revocation bitmaps with positive uint128 `grantId` values, represented as canonical decimal strings. Add `invisi_getCreditStatus(owner, token, grantId, atBlock)` with canonical block metadata and status 0 (unknown), 1 (active), or 2 (permanently disabled); remove the retired allocation and bitmap methods.
- Persist and reconstruct grant transitions at their exact canonical block on SQLite and PostgreSQL. Support pre-revocation, permanent non-reuse and rollback; index fee-free zero-token pre-revocations without stalling checkpoint progress, while retaining nonzero-owner and paid-operation validation.
- Require index/snapshot format 4 and the current Core ABI fingerprint. Reject legacy shard/slot databases, snapshots and events explicitly. Recreate development indexes from the newly deployed Core's configured start block; old shard/slot values cannot be converted into grant IDs automatically. Protocol/API version remains 1.

### Historical headers and wallet synchronization

- Add `invisi_getBlockHeaders` for inclusive ranges of 1–256 complete original Ethereum headers, with deployment, ordering, range-anchor and full header-hash checks. Persist headers atomically with indexed blocks, including empty blocks, and retain them in portable snapshots.
- Backfill missing historical headers in bounded upstream batches after checking saved block identity, a fresh terminal header and the durable finality fence. Backfill does not replay events or change checkpoints, spending state or grant history; rollback removes orphaned headers with their blocks.
- Serve warm historical ranges from a consistent database snapshot without repeat upstream reads. Cached history is not a live canonicality assertion: SDK clients still perform fresh anchor/finality checks. Only explicit unsupported-method or unavailable-history responses permit compatibility fallback.

### Router paths, dependencies and packaging

- Validate Core event indexing through direct, Router and sponsored execution paths while retaining the actual Core payer. RPC continues to index the Core contract; outer transaction senders and upstream swap sources are separate from the Core funding payer.
- Upgrade consumed Go SDK modules to published v1.3.3, including the current Core/Factory bindings. Remove committed local replacements and Docker SDK-source mounts so standalone builds resolve the published dependency cohort. RPC needs no proving resources or prover runtime.
- Refresh operator/API compatibility references and remove the obsolete root configuration template. Existing development databases must follow the format-4 reset requirement above.

## 1.3.1

- Upgrade the published Go SDK protocol and primitives modules to v1.3.2, including the previously unshipped v1.3.1 dependency alignment.
- Allow canonical `creditAllocation` and `creditRevocationBitmap` reads through `invisi_readChainState`, enabling WalletSession consumption and revocation checks without a separate chain RPC connection.
- Allow canonical `creditDraw` read-only simulations while continuing to reject unknown selectors, malformed argument widths and trailing calldata. Transaction submission remains outside the RPC API.
- Preserve the existing Protocol v1 deployment, indexed database, canonical receipt checks and durable finality anchors.

## 1.3.0

- Consume Go SDK v1.3.0 and the redeployed Core unified receipt events.
- Authenticate Note operation IDs, state transitions, ordered aggregate inclusion and insertion subranges through the SDK receipt decoder.
- Index Credit creation with an optional initial merchant payment, preserving gross funding, fees, net ordinary Note amounts and separate owner/spender Credit deliveries.
- Keep canonical receipt verification, rollback and checkpoint handling for all new outputs and nullifiers.
- Use snapshot format 3 with the current Core ABI fingerprint; pre-unified snapshots are incompatible.

## 1.2.6

- Pin the published Go SDK v1.2.6 modules under `github.com/invisi-labs` and publish images under `ghcr.io/invisi-labs`.

## 1.2.5

- Update the published Go SDK protocol and primitives dependencies to v1.2.5.
- Adopt the Invisi RPC project, module and binary names and consume the SDK-owned Invisi Core ABI instead of duplicating contract definitions.
- Preserve existing v1 APIs and indexed state; RPC does not require circuit assets or a prover runtime.

## 1.2.4

- Extend `invisi_getNote` with trusted confirmation evidence for payment verification and expose read-only chain state for SDK wallet sessions.
- Add detailed `api.md` and `cammand.md` references and align CLI directory/listener behavior.
- Normalize HTTP transport errors, content types, request limits and rate-limit responses; sanitize internal API diagnostics without changing successful results.
- Keep Protocol v1 endpoints and existing indexed state compatible; API clients should handle the documented HTTP error statuses.

## 1.2.1

- Use Go protocol module 1.2.2 for current shared protocol definitions and settlement encoding.
- Remove delegated account-detection RPC methods; account detection and independent scanning belong to SDK clients.

## 1.2.0

- Use the published Go protocol module 1.2.0 for canonical settlement encoding and shared protocol definitions.
- Include the current Note, deferred Credit and Provenance integration.

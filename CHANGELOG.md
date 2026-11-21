# Changelog

## Unreleased

- Persist complete original headers with indexed blocks and portable snapshots; serve bounded, deployment-pinned historical header ranges without repeat upstream reads. Backfill legacy history atomically without resetting indexed state, and preserve live canonical/finality, consumption, revocation and reorg checks.

- Replace shard/slot allocation and bitmap methods with canonical uint128 `invisi_getCreditStatus`; index scoped 0/1/2 history, pre-revoke, terminal disabled status and rollback on both SQLite and PostgreSQL.
- Require index/snapshot format 4 and the current Core ABI fingerprint. Refuse legacy shard/slot databases, snapshots and event history explicitly; reset/recreate development indexes for the newly deployed Core.
- Follow the latest sibling Go SDK source cohort for the planned v1.3.3 compatibility update, including the Core ABI without address-screening hooks. Keep source recognition, authorization and proof semantics unchanged.

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

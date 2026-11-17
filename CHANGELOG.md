# Changelog

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

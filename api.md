# Invisi RPC API

This is the public HTTP and JSON-RPC reference for this source checkout. Operator commands, configuration, database setup, snapshots and CLI environment variables stay in [cammand.md](cammand.md). The service indexes public chain facts; it never accepts wallet keys, note openings, signing requests, proof witnesses or transaction submissions.

## Compatibility and deployment identity

The API namespace is `v1`, on-chain Protocol `VERSION()` is `"1"`, and this checkout pins `github.com/invisi-labs/invisi-sdk-go/protocol v1.3.0` and go-ethereum `v1.17.2` in [go.mod](go.mod). These are independent of the server binary release. Pin a tagged binary/archive checksum or container digest for reproducibility, and independently pin the expected chain ID, Protocol address, runtime code hash and deployment start block. A version response alone does not authenticate a deployment.

The `deployment` object uses the shared contract profile:

```json
{
  "chainId": "31337",
  "protocol": {
    "address": "0x0000000000000000000000000000000000001234",
    "fromBlock": 8,
    "runtimeCodeHash": "0x0000000000000000000000000000000000000000000000000000000000000001"
  },
  "registry": {
    "address": "0x0000000000000000000000000000000000001235",
    "runtimeCodeHash": "0x0000000000000000000000000000000000000000000000000000000000000002",
    "fromBlock": 8
  }
}
```

The example pins are placeholders. `registry` and `broadcasterRegistry` are optional objects with the same three contract fields. `protocol` is required; its wire shape comes from Deployment.MarshalJSON, not the internal struct field tags. Discovery lists active authenticated runtime routes, rather than every configured or failed chain. It reveals neither upstream credentials nor endpoints and is not a live health or finality guarantee. See [model.go](internal/model/model.go), [deployment.go](internal/model/deployment.go), [service.go](internal/app/service.go) and [lifecycle.go](internal/app/lifecycle.go).

## HTTP routes and transport

| Route | Method | Response |
| --- | --- | --- |
| `/v1`, `/v1/` | GET | `{"apiVersion":"v1","chains":[{"chainId":"31337","protocolVersion":"1","rpcPath":"/v1/chains/31337"}]}`; `chains` is an empty array when none are active |
| `/v1/chains/<chainId>` | POST | JSON-RPC 2.0 request or batch; a single trailing `/` is also accepted |

Chain IDs are configured canonical positive decimal integer strings, with no leading zeros, signs or hex. Unknown chains/versions, `/`, unversioned paths, extra path components and double trailing slashes return HTTP 404. On a recognized route, other HTTP methods return 405 with `Allow: GET` for discovery or `Allow: POST` for JSON-RPC. The backend has no health, REST explorer, WebSocket or arbitrary Ethereum RPC forwarding route. OPTIONS also returns 405 at the backend.

Send `Content-Type: application/json` (parameters such as `charset=utf-8` are accepted). Existing Geth aliases `application/json-rpc` and `application/jsonrequest` also remain accepted. Missing, malformed or other media types return 415. Bodies larger than 1 MiB, including streamed bodies of unknown Content-Length, return 413 before any method executes. Bodies that cannot be read return 400. These transport errors and router errors use static sanitized JSON-RPC error envelopes with `id:null`, `error.code:-32600` and a short message. They never reflect request content or body-read diagnostics. Distinguish the HTTP status before interpreting the JSON-RPC code.

The listener admits at most `maxConcurrentRequests` simultaneous HTTP requests across all chains (default 64; configurable 1–4096). Exhaustion returns HTTP 429, `Retry-After: 1`, and `{"error":"request capacity reached"}`; it is an HTTP admission error, not a method result. The limit counts an entire batch as one HTTP request. There is no built-in identity-based rate limiting or authentication. The server reads headers within 5 s, has 35 s read/write deadlines, 60 s idle timeout, 16 KiB maximum headers, and gives dispatch a 30 s context budget after bounded body reading. Cancellation is cooperative; these are resource budgets, not latency promises.

No CORS or cache headers are added by the backend, and it does not reject requests based on Origin. Browser CORS, authentication, TLS and identity-based quotas belong to the gateway. The [Nginx example](deploy/nginx.conf.example) exposes wildcard CORS, handles OPTIONS with 204, exposes Retry-After, and illustrates optional recognized Bearer keys. Its gateway 401/429 bodies and limits are distinct from backend JSON-RPC. Configure trusted proxies before using forwarded IPs. A gateway key authorizes service access and must never be a wallet key. Do not cache mutable status, pending results or unanchored discovery as immutable facts; revalidate chain anchors when caching historical data.

## JSON-RPC envelope, types and errors

Use positional arrays: `{"jsonrpc":"2.0","id":1,"method":"invisi_getVersion","params":[]}`. All method arguments below are positional and required unless marked optional inside an object. Zero-argument methods accept omitted params or `[]`; named parameter objects are not supported. Geth handles request IDs and envelopes: string and numeric IDs are recommended, `id:null` is an identified request (Geth also accepts primitive boolean IDs), and valid id-less notifications execute read-only methods without a response. Notifications must not be used when a caller needs evidence of completion. Batches contain up to 20 calls, may mix notifications and identified requests, and responses must be matched by ID rather than array order. All-notification requests have an empty HTTP response body. Empty batches and batches over 20 calls return `-32600` (an oversized batch reports one error associated with the first call ID when available); batch response budget is 16 MiB. There are no subscriptions. Normal decoded requests, including business failures, use HTTP 200.

| Code | Meaning |
| --- | --- |
| `-32700` | Geth JSON parse/invalid message error |
| `-32600` | Invalid JSON-RPC envelope, or static transport failure with a non-2xx HTTP status |
| `-32601` | Unknown/removed method or unsupported subscription |
| `-32602` | Parameter count/type/encoding fails Geth argument decoding |
| `-32000` | Existing method/domain/storage/upstream error, including semantic validation, unavailable index, bad anchors and limits; business codes are unchanged |
| `-32002` | Geth request timeout |
| `-32003` | Geth batch response byte budget exceeded |
| `-32603` | Geth internal/panic/marshal error |

A normal API-returned error has `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"Request rejected"}}` and no result. The public adapter sanitizes all API-returned errors, including authored semantic validation messages and backend/upstream diagnostics. It preserves an existing `ErrorCode()` (including wrapped coded errors), otherwise uses `-32000`, and omits untrusted error data. Internal Go callers retain detailed errors. Geth's own parse/envelope/method/argument decoding errors occur outside the adapter and retain their standard messages/codes. Success values, nulls, typed pending reasons and confirmation evidence remain unchanged. The method-specific failure conditions below describe behavior, not public error-message strings; use the code and method context rather than substring matching. No business codes were reassigned for cross-service uniformity.

| Wire type | Encoding |
| --- | --- |
| Hash/commitment/nullifier/root | Exactly 32 bytes in `0x` hex, not an abbreviated hex integer |
| Address | Exactly 20 bytes in `0x` hex; methods requiring nonzero addresses reject zero |
| Ciphertext/clue/calldata/returned bytes | Even-length `0x` hex; empty bytes are `"0x"` |
| `Block` | `{number,hash,parentHash,timestamp}`; number/timestamp are JSON uint64 integers, timestamp is Unix seconds |
| Method block/tree indices | JSON uint64 integers; leaf indices uint32; no quoted numbers, negative values, fractional values or hex strings |
| ABI event uint256/uint64 | Decimal strings; uint16/uint8 remain JSON numbers |
| Settlement operation integers | Decimal strings (including blockNumber/timestamp/tree/leaf indices); see operation schema below |

Clients must preserve integer precision; JSON uint64 fields can exceed JavaScript's safe integer range. Decode using an integer-preserving parser when needed. A commitment must additionally be nonzero and a canonical BN254 scalar for Note/Credit lookup. Nullifier consumption methods require nonzero hashes. Generic Go-decoded query objects retain their existing unknown-field behavior; only operation queries and the Note finality selector explicitly reject unknown object fields. Do not assume Provenance's stricter duplicate-field/envelope policy applies here.

## Shared response schemas

`Root` is `{treeId,leafCount,root,block}`. `Membership` is `{anchor:Root,leafIndex,commitment,siblings:[Hash,...]}` (the field is `anchor`, not `root`); the fixed-depth physical NoteTree proof has 16 siblings. `NoteOutput` is `{block,treeId,leafIndex,commitment,token,ciphertext,clue,transactionHash,logIndex,kind}`. `CreditState` has the same metadata but replaces ciphertext/clue with `deliveries:[{ciphertext,clue},{ciphertext,clue}]` in on-chain owner/spender order. No recipient attribution is inferred.

`Event` is `{block,name,transactionHash,logIndex,data}`. `data` is the normalized ABI event argument object from the pinned [Core ABI](../invisi-sdk-go/protocol/contracts/abi/InvisiCore.abi.json); nested structs use lower-camel keys, addresses/bytes are hex, and uint256/uint128/uint64 are decimal strings. This low-level method includes all decoded event names; consumers should use the ABI corresponding to their pinned server version rather than assume an event-specific universal schema. Note receipts correlate `NoteOperation`, `NoteOutputs`, `NoteStateTransition`, `PublicDeposit` and `PublicWithdrawal` by `operationId`; `TransactAggregated.operationIds` records member order. Credit transitions select `CreditCreated`, `CreditDraw`, `CreditStateTransition`, `CreditUpdated`, `CreditRevoked`. Creation with an initial payment emits `CreditCreated` plus `CreditDraw`; the merchant Note remains an ordinary Note.

`Batch` is `{block,logs,events,createdTrees,insertions,outputs,creditStates,creditNullifiers,nullifiers}` with optional `header`, `registrations` and `broadcasters`. `header`, when present, is the complete original Ethereum header (hex quantities and fork fields), validated against `block`. Legacy indexed batches and snapshots can omit it. `logs` use Ethereum log JSON (hex quantities such as logIndex/blockNumber); normalized projections use the types above. Insertion entries are `{treeId,startLeafIndex,commitments,logIndex}`. Receiving-key updates are `{walletIdentity,viewingPublicKey,clueKey,nonce,logIndex}` (nonce decimal string). Broadcaster updates are `{operator,endpoint?,metadataURI?,quoteSigner,feeRecipient,enabled?,logIndex}`. Absent optional collections may be omitted; underlying nil collections may be JSON null. The typed per-transaction APIs below make explicit empty-array guarantees after indexing.

## Complete method reference

Business methods use the literal `invisi_` prefix; `rpc_modules` is the Geth metadata exception. These are read-only methods on the selected chain route; there is no cross-chain fallback.

| Method | Positional params | Result and method-specific behavior/errors |
| --- | --- | --- |
| `invisi_getVersion` | `[]` | `{apiVersion:"v1",protocolVersion:"1"}`; no index checkpoint required |
| `invisi_getDeployment` | `[]` | Versions plus `deployment` as above; available before the first checkpoint |
| `invisi_getStatus` | `[]` | Versions, deployment, `indexedHead:Block or null`, `finality:{latest,safe,finalized}`; each head is `{status,block:Block or null}`; checkpoint errors fail the method |
| `invisi_getBlock` | `[number:uint64]` | Complete indexed `Batch` or `null` when not indexed; does not fetch an upstream replacement |
| `invisi_getBlockHeaders` | `[{fromBlock:uint64,toBlock:uint64}]` | Versions, deployment, `range:{fromBlock,toBlock,blockHash}`, and ordered `headers:[{header,headerHash}]`; inclusive 1–256 complete indexed headers; historical semantics and compatibility errors below |
| `invisi_getEvents` | `[range]` | `Event[]`; anchored inclusive range, limits below; no silent partial results |
| `invisi_getRoots` | `[treeId:uint64]` | Up to 10,000 physical roots, newest leafCount first; no cursor; unknown tree has no roots |
| `invisi_getMembership` | `[treeId:uint64,leafIndex:uint32,root:Hash]` | `Membership`; unknown root or leaf outside that root fails; physical proof alone does not classify a Note/Credit |
| `invisi_getNote` | `[commitment:Hash,atBlock:uint64]` | Legacy `{output:NoteOutput,membership:Membership}` or `null`; atBlock must be indexed; noncanonical/zero commitment and reorg fail |
| `invisi_getNote` | `[commitment:Hash,{finality:"safe" or "finalized"}]` | Versioned confirmed/pending evidence below; latest/null/unknown selectors fail |
| `invisi_getNoteUpdates` | `[range]` | `{blockHash,notes:NoteOutput[]}`; only ordinary Notes; same anchor/range/response bounds |
| `invisi_getTransactionNoteUpdates` | `[transactionHash:Hash]` | Availability metadata plus `notes`; zero hash, upstream failure, predeployment transaction or changed canonicality fail |
| `invisi_isNoteSpent` | `[nullifier:Hash,atBlock:uint64]` | Boolean ordinary Note consumption through indexed atBlock, rechecked for reorg; nonzero nullifier required |
| `invisi_getCreditState` | `[commitment:Hash,atBlock:uint64]` | `{state:CreditState,membership:Membership}` or `null`; never an ordinary Note; same scalar/index/reorg requirements as legacy getNote |
| `invisi_getCreditTransitions` | `[range]` | `Event[]` restricted to the five Credit transition event names; the complete underlying event range must still fit limits before filtering |
| `invisi_getTransactionCreditUpdates` | `[transactionHash:Hash]` | Availability metadata plus `states:CreditState[]`, `transitions:Event[]`; only that transaction, same transaction validation as Note updates |
| `invisi_isCreditConsumed` | `[nullifier:Hash,atBlock:uint64]` | Boolean Credit state consumption through indexed atBlock; distinct from grant status and Note nullifiers |
| `invisi_getCreditStatus` | `[owner:Address,token:Address,grantId:string,atBlock:uint64]` | `{block:Block,status:0\|1\|2}`; canonical positive uint128 decimal string ID; nonzero owner; zero token is queryable for fee-free pre-revocation; block must be indexed, at/after deployment start, safe on JSON wire and unchanged across query |
| `invisi_getReceivingAddress` | `[ownerAddress:Address]` | `{ownerAddress,viewingPublicKey,clueKey,nonce,block}` or `null` if unregistered; 33-byte viewing key, 32-byte clue key, decimal uint64 nonce; errors when zero owner, no Registry, or Registry history not indexed |
| `invisi_getBroadcasters` | `[{enabled?:boolean,after?:Address,limit?:integer,block?:Block}]` | `{block,broadcasters,next?}`; paging and registry errors below; limit omitted or 0 defaults to 100, negative or >100 rejected |
| `invisi_getSettlementOperations` | `[{fromBlock?:uint64,toBlock?:uint64,limit:integer,cursor?:string}]` | `OperationsPage` below; required limit 1–100; unknown fields rejected; canonical replay/work bounds apply |
| `invisi_getProtocolTransactions` | Same as getSettlementOperations | Same logical operation page, including supported direct failures; group by txHash for transaction rows |
| `invisi_getTransactionOperations` | `[transactionHash:Hash]` | `{transactionHash,status,operations,indexedHead,receiptStatus?,block?,unsupportedReason?}`; status `not_found`, `pending_index`, `failed`, `indexed`; zero hash, noncanonical receipt/index/deployment or inconsistent transaction fail |
| `invisi_readChainState` | `[{kind,block?,address?,from?,data?,gas?,transactionHash?}]` | Versioned restricted gateway response below; allowed target/selector/size/receipt checks apply |
| `rpc_modules` | `[]` | Existing Geth metadata method: `{"invisi":"1.0","rpc":"1.0"}`; these registration labels are not API/Protocol versions or evidence of Ethereum forwarding |

`invisi_getUpdates`, `invisi_getTransactionUpdates`, `invisi_detectOutputs` and `invisi_isSpent` are removed and return `-32601`. The raw internal transaction helper is not separately registered. No `eth_*`, key management, submission or detection method is exposed.

## Ranges, null and transaction availability

A range is `{fromBlock:100,toBlock:110,blockHash:"0x..."}`; all fields are required semantically (omitted integer fields decode as zero). Its end hash must be nonzero and match the indexed toBlock before and after the query. It accepts at most 10,000 inclusive blocks. Events hitting 10,000 records fail conservatively; Note output storage rejects more than 10,000 records. Ranged responses have an 8 MiB encoded value limit. Excess counts/bytes produce explicit errors, never truncation; narrow the range or use complete indexed blocks. There is no automatic range continuation cursor. Maintain the end hash and retry from authenticated anchors after reorgs.

Legacy Note/Credit null results establish no cryptographic absence claim. An unindexed requested block is an error, not absence. Membership says nothing about private spending authority or the correct derived nullifier. Clients authenticate roots independently unless deliberately using the trusted confirmation mode.

Transaction Note/Credit responses share `{transactionHash,status,indexedHead,blockNumber?,blockHash?,receiptStatus?}`. `not_found` means no mined receipt (unknown or pending); `pending_index` means a canonical mined receipt exists but the local canonical block is not ready, including a stale indexed fork. Before `indexed`, Note `notes` and Credit `states`/`transitions` are null. After indexing they are arrays, including empty arrays for unrelated or reverted transactions. `receiptStatus` is a JSON uint64 number (0 failed, 1 successful) and is independent of availability. Only the selected transaction's indexed projections are returned; upstream logs never replace the local index. Results must fit the 8 MiB bound and are rechecked for canonicality. Settlement lookup instead always initializes `operations:[]`, uses `failed` for an indexed failed receipt, and does not share the typed null-array contract.

## Broadcaster and receiving-key trust

Broadcaster records are `{operator,endpoint,metadataURI,quoteSigner,feeRecipient,enabled,block}`. The result's top-level block is the indexed snapshot; each record's block is its last update. Results are ordered by operator address. `enabled` is an optional filter. If `next` is present, pass it as `after` together with the exact returned top-level `block`, retaining the filter. Nonzero after without an anchor, unknown/reorganized anchors, missing registry or incomplete registry indexing fail. Each page is bounded to 100 records and 8 MiB. A zero limit currently means the default 100; it is not rejected.

Registration/enabled flags are operator declarations, not endorsement, endpoint health, quote validity or safety guarantees. The RPC never calls those endpoints. Verify signed quotes independently. Registered receiving keys reflect indexed public events and the returned indexed head; they are not contract storage proofs. Verify independently or trust the selected RPC. Polling and reorg rollback restore history; null registration during recent synchronization is not immediate chain-wide absence. New updates appear once indexing reaches their canonical block. Indexing follows latest immediately; this availability is separate from native safe/finalized evidence.

## Restricted read gateway

### Complete historical headers

`invisi_getBlockHeaders` reads one inclusive indexed range, with both heights at most `9223372036854775807`. The response includes the same version and full deployment pins as other trusted chain responses. Each `header` retains the original Ethereum JSON fields, including zero-valued seal fields and all applicable fork fields. `headerHash` binds its full RLP hash; `range.blockHash` is the last header's hash. Results are ordered and complete: no partial success or reconstructed simplified headers.

New indexed blocks persist their complete headers atomically with events, roots and nullifiers, including empty blocks. Existing databases and format-3 snapshots remain usable without reset. Legacy indexed history is populated on demand in bounded upstream batches (at most 128 headers per native batch), after matching every saved block's number, hash, parent and timestamp. A fresh native terminal-header check and the persistent finality fence guard the atomic backfill. Backfill only writes the optional header; it never replays events, changes checkpoints, or reapplies spending/grant state. Rollback removes headers with their blocks. Snapshots preserve present headers and validate them; absent legacy headers can be repopulated later.

A successful warm range uses one consistent database statement snapshot, including the durable finality-conflict flag. It does **not** call upstream or assert that its stored anchor remains the current live chain. Clients must continue fresh `invisi_readChainState(kind:"header")` and native finality checks before committing a scan, and retain receipt, Merkle/root, consumption and revocation checks. Single-header/tag reads and all existing execution-state reads remain live.

Error `-32004` means indexed/header history is unavailable (for example a range beyond the checkpoint or a backend with no historical header support). An old server may return `-32601`. SDKs may use these two explicit errors to restart the whole read through live headers. Corruption, incomplete original header fields, conflicting finality, provider failures and invalid ranges are ordinary rejected requests, never missing-history compatibility errors. Public diagnostics remain sanitized. Range responses obey the existing 8 MiB size limit; header material itself is bounded to 7 MiB. Clients above JavaScript's exact integer range must use an integer-preserving encoder or the live hex-height gateway.

`invisi_readChainState` returns `{apiVersion,protocolVersion,deployment,data,receipt,pending,header?,headerHash?,transaction?}`. `data` is hex bytes (empty `"0x"`); receipt is null when inapplicable/not found; pending defaults false. Geth headers, receipts and transaction objects retain Ethereum JSON hex quantities and fork-dependent optional fields; they do not use normalized Block encoding. `headerHash` is a Hash field and may be the zero hash when no header is returned. The SDK trusted providers validate relevant hash bindings.

`kind` is required and is one of:

| kind | Required fields | Read/result |
| --- | --- | --- |
| `header` | none beyond kind | HeaderByNumber; `header` when found, plus computed `headerHash` |
| `code` | nonzero address | Runtime bytecode in data; arbitrary nonzero code targets are allowed |
| `call` | nonzero address, data | Allowed `eth_call`, result bytes in data; optional from, gas |
| `receipt` | nonzero transactionHash | Receipt or null; receipt txHash/height and canonical header binding checked |
| `transaction` | nonzero transactionHash | Transaction and pending flag, absent transaction on not found; hash checked; requires upstream transaction-read capability |

`block` defaults to latest (also accepts empty string), or `safe`, `finalized`, or a canonical hex quantity fitting nonnegative int64. It is parsed even for receipt/transaction but those reads are by hash. No pending block selector. `gas` is a JSON uint64, maximum 30,000,000; 0/omitted uses that maximum for calls. `data` maximum is 128 KiB. Returned data maximum is 1 MiB; the complete response is also checked against 8 MiB. Extra kind-irrelevant fields are not uniformly rejected; they do not create extra operations.

At the pinned Protocol target, allowed selectors are `VERSION()`, `addressRegistry()`, `rootHistory(uint256,bytes32)`, `isNullifierSpent(bytes32)`, `nonces(address)`, generated-ABI `creditStatus(address,address,uint128)` reads, and `transact`, `crossTreeTransact`, `shield`, `creditDraw` simulations. Credit reads and simulations require canonical ABI encoding, including exact argument widths and no trailing calldata. At receiving Registry: `isRegistered(address)`, `nonceOf(address)`, `receivingKeysOf(address)`, `CHAIN_ID()`. At BroadcasterRegistry: `broadcasterOf(address)`. Other nonzero targets only accept ERC20 `name()`, `symbol()`, `decimals()`, `balanceOf(address)`, `allowance(address,address)`. Fixed selectors require exact argument byte length; the three Protocol simulations require an exact unpack/repack ABI round trip. The server does not implement a general EVM argument semantic validator for fixed words; upstream contract decoding still applies. Simulation is read-only `eth_call`, never transaction submission. Unsupported kinds/selectors, bad blocks/targets/gas/data, missing reader capabilities, mismatched hashes and response limits return method errors.

This gateway intentionally trusts the selected Protocol RPC and its upstream; it is not independent node authentication. Go `rpc.NewTrustedProvider`, TypeScript `TrustedProvider` from `@invisi-labs/rpc/trusted-provider` and Rust `rpc::TrustedProvider` adapt it for WalletSession preparation/recovery. Keep the deployment pinned and use finalized Note confirmation for receiving evidence.

## Canonical settlement facts

The three operation methods are independent chain-fact APIs. They do not assess funding provenance or combine the existing Note/Credit query methods. `invisi_getProtocolTransactions` returns the same logical-operation page as `invisi_getSettlementOperations`; consumers group `txHash` when presenting transaction rows.

A page query uses numeric block bounds and `limit` from 1 through 100. Omitted `fromBlock` begins at Protocol deployment, independently of earlier Registry indexing; omitted `toBlock` freezes the current indexed head. Responses contain `operations`, `snapshot:{blockNumber,blockHash}`, `indexedHead:{blockNumber,blockHash}`, optional `nextCursor`, and `indirectFailureDiscovery:"unsupported_without_traces"`. An opaque continuation binds chain, Protocol, fixed snapshot, range, transaction position and member offset. Follow the cursor even after an empty page; omit block bounds on continuation. Any missing indexed block, upstream failure, deployment mismatch or changed canonical snapshot returns an error. This implementation replays the existing indexed block/log data against canonical chain blocks, with no second persistent index or schema migration. Each page scans at most 16 blocks and 256 relevant receipts. Reaching a page or work limit returns a continuation, including within a dense block or an aggregate transaction; consumers must follow it until absent before treating the selected range as complete.

Each public operation includes decimal-string `chainId/blockNumber/timestamp/operationIndex/memberIndex/logIndex/mainCircuitCapacity`, block and transaction hashes, `kind/tokenAddress`, separate `noteNullifiers/creditNullifiers`, separate `noteOutputs/creditOutputs`, `grantRefs`, gross `funding:[{payer,tokenAddress,amount}]`, withdrawal/fee fields, `success`, and `operationDigest`. Note output locations use `noteCommitment/treeId/leafIndex`; Credit locations use `creditCommitment/treeId/leafIndex`; amounts are absent unless public on chain. Credit outputs remain typed authority state and never ordinary Notes. No private opening or witness is accepted.

Direct calldata is decoded with an exact generated-ABI round trip. Aggregate members retain their native capacity and original ordering. A transact digest is Keccak256 of `abi.encode(string("Invisi.Provenance.Operation.v1"),uint64(chainId),address(protocol),uint8(2),uint32(C),bytes32[] publicInputs)`, with the actual ordered `17+3*C` words including wallet authorization. The codec, source-authenticated ABI generator and independent ethers vectors live in the `settlement` package of the shared `github.com/invisi-labs/invisi-sdk-go/protocol` module (v1.3.0). RPC retains canonical chain receipt/log authentication, indexing, and pagination. BatchRequest native capacity and statement hash, typed output insertions, fee accrual, and actual ERC20 funding receipts are cross-checked. Same-token/same-payer pooled pulls are allocated by the authenticated ordered member requests, never by `tx.from`.

A transaction lookup returns `not_found`, `pending_index`, `failed`, or `indexed`, with receipt success separate from indexing availability. Read-only full block discovery includes direct reverted Protocol transactions. Indirect successful calls with Protocol logs and indirect failed hash lookups report `indirect_call_attribution_requires_traces`; their statement, payer and outputs are not guessed. Reverted indirect calls cannot be discovered as a complete list without traces. Unsupported selectors or unauthenticated funding receipts carry an explicit `unsupportedReason`; a consumer must not recognize such operations. This is a bounded replay capability, not a claim of complete trace-backed historical discovery.


`invisi_getStatus` includes `finality.latest`, `finality.safe`, and `finality.finalized`. Each contains `status` (`available`, `unsupported`, `unavailable`, or `inconsistent`) and `block` (the existing block shape, or `null`). These are native `eth_getBlockByNumber` heads from the configured Chain RPC, independent of `indexedHead`; no block-depth fallback is used. `unsupported` means an explicit unsupported-method/invalid-tag RPC response, while transport failures and null results are `unavailable`. Headers are hash-checked against canonical numeric reads and ordered finalized ≤ safe ≤ latest. Incoherent samples are retried once. Missing safe/finalized capability does not stop latest indexing or query service.

Accepted finalized anchors are stored durably per deployment in SQLite/PostgreSQL and survive index rollback and service restart. A changed canonical hash at that anchor pauses indexing and reports finalized as inconsistent; investigate the upstream before recovery. An upstream whose latest/finalized height regresses below the accepted anchor is unavailable and pauses indexing until it catches up, without creating a permanent conflict fence. Transient inability to verify an existing anchor also pauses indexing without changing the anchor. Stalled native finality remains available at its unchanged height; applications compare their transaction block against that height. Snapshot exports contain indexed data, not the operator's accepted-finality anchor; restoring a snapshot initializes native finality against the configured upstream.

The additive finality-state table is initialized automatically; standalone reversible SQL is in `deploy/migrations/001-native-finality.{up,down}.sql` for either backend. A finalized-history conflict creates a sticky persisted fence: correcting the provider or restarting does not clear it. The read-only service remains available with finalized `inconsistent`; indexing stays paused. Operator recovery requires stopping the service, investigating the conflicting upstream/index histories, backing up the database, and explicitly clearing `conflicted` only after validating the accepted anchor and indexed history. Never drop or reset the anchor merely to make a conflicting provider pass validation.

## Trusted Note confirmation

`invisi_getNote` preserves `[commitment, atBlock]` and its historic `output`/`membership` or null response. A new selector `[commitment, {"finality":"finalized"}]` (or `safe`) requests server-authenticated confirmation. `latest`, unknown selector fields and null selectors are rejected. Send JSON-RPC to `/v1/chains/{chainId}` as before.

Confirmation mode always returns `apiVersion`, `protocolVersion`, `deployment`, `indexedHead`, `status`, `reason`, `output`, `membership`, and `confirmation`. Confirmed results have `status:"confirmed"`, `reason:null`, and include the existing output/proof plus `{finality, verifiedAt, finalityHead}`. Pending results have `status:"pending"` and a reason string. The verification anchor is the lower of the indexed head and the native finality head. The server authenticates canonical headers, successful transaction receipt, Merkle proof and the contract's recorded root at that anchor, then rechecks canonicality/finality before returning.

Pending results have null output, membership and confirmation. Reasons are `not_observed`, `pending_index`, `confirmation_pending`, `finality_unsupported`, `finality_unavailable`, or `finality_inconsistent`. Missing indexed data does not prove no payment occurred; retry after synchronization. Upstream query or binding failures return JSON-RPC errors, never confirmed evidence. There is no fixed-depth fallback for native finality.

This mode explicitly trusts the selected Protocol RPC for canonicality and finality. It lets server applications avoid direct Chain RPC access; it is not independent light-client verification or a spending-authority proof. It accepts no nonce, opening or merchant order. Consumers still compare the commitment with their private receiving materials and enforce local one-time redemption. Existing SDK independent chain-authentication paths remain available.

The cross-language wire fixture is `internal/rpc/testdata/note-confirmation.json`; regenerate intentionally with `UPDATE_NOTE_FIXTURE=1 go test ./internal/rpc -run TestNoteConfirmationWireFixture`.


## Settlement operation schema and continuation

`OperationsPage` is `{operations,snapshot:{blockNumber,blockHash},indexedHead:{blockNumber,blockHash},nextCursor?,indirectFailureDiscovery:"unsupported_without_traces"}`. Anchor blockNumber is a decimal string. Limit is always required (1–100), even with cursor; cursor maximum is 2048 characters. Initial fromBlock defaults to Protocol deployment, toBlock to indexed head, with inclusive bounds inside that interval. Continuation must omit both block bounds. Cursors are opaque deployment/range/snapshot/position bindings, not authentication tokens or signatures; do not edit them. An empty page with nextCursor is incomplete. Each page scans at most 16 blocks and 256 relevant receipts; it can continue within a dense block or aggregate. A changed canonical snapshot, missing indexed data, bad cursor or upstream authentication failure rejects the query.

Every Operation contains `chainId`, `protocolAddress`, `blockNumber`, `blockHash`, `parentHash`, `timestamp`, `txHash`, `operationIndex`, optional `memberIndex`, `logIndex`, `kind`, `tokenAddress`, `mainCircuitCapacity`, `publicInputs`, `noteNullifiers`, `creditNullifiers`, `noteOutputs`, `creditOutputs`, `grantRefs`, `funding`, `withdrawalAmount`, `feeAmount`, `feeRecipient`, `success`, `operationDigest`, and optional `unsupportedReason`. Integer values are decimal strings; hashes/addresses use hex; success is boolean. `publicInputs` and nullifiers are Hash arrays. Note outputs use `{noteCommitment,treeId,leafIndex,amount?}`; Credit outputs use `{creditCommitment,treeId,leafIndex}`. Grant refs use `{payerIndex,grantId}`. Funding entries use `{payer,tokenAddress,amount}`. Private output amounts are absent. Never substitute a Note commitment field for the distinct Credit commitment field. The exact shared wire fixture is [settlement-statements.json](internal/rpc/testdata/settlement-statements.json).

## Private recovery and public encrypted data

Clients fetch Note updates and Credit transitions, detect/decrypt locally, recompute commitments, verify paths, authenticate roots and check the appropriate derived nullifier. RPC never receives account detection keys. Status intentionally has no detectionEnabled/detectionBits; legacy detection fields in chain configuration are rejected. Missing results are not cryptographic absence proofs. Credit creation registers authority without principal funding; optional authorized owner fees may be pulled. Draw consumes prior states and emits successor Credit states plus one ordinary receiver Note; only that Note is returned by Note queries. Gross payer/grant usage differs from the net receiver amount after authorized fee. Update preserves grant namespace; revocation permanently disables the scoped grant ID and creates no Note, state or nullifier. An unconsumed state may be revoked: query both facts separately.

Ciphertexts are opaque, bounded to 4096 bytes when present. Funds deliveries may be paired empty ciphertext/clue, while active Credit role deliveries remain mandatory. The service does not interpret wallet codecs. Aggregate delivery framing is a two-byte big-endian ciphertext length, ciphertext, then 68-byte clue; an omitted funds delivery is only a zero length prefix. Credit recovery uses the canonical transition block timestamp to derive UTC+0 Gregorian day/month usage; never local time or caller dates. Index format 4/snapshot format 4 carry explicit timestamps. Snapshot history inherits trusted publisher provenance: a checksum and continuous hashes do not authenticate arbitrary restored normalized records. Independent Chain RPC verification remains a client responsibility unless explicitly choosing trusted server confirmation.

## Examples

Use a real deployment and complete 32-byte hashes obtained from your index/chain. Port 8080 is the default local listener.

```sh
BASE=http://127.0.0.1:8080
curl -sS "$BASE/v1"
curl -sS "$BASE/v1/chains/31337" -H 'Content-Type: application/json' \
  --data '{"jsonrpc":"2.0","id":1,"method":"invisi_getDeployment","params":[]}'
curl -sS "$BASE/v1/chains/31337" -H 'Content-Type: application/json' \
  --data '{"jsonrpc":"2.0","id":2,"method":"invisi_getSettlementOperations","params":[{"limit":20}]}'
curl -sS "$BASE/v1/chains/31337" -H 'Content-Type: application/json' \
  --data '{"jsonrpc":"2.0","id":3,"method":"invisi_getBroadcasters","params":[{"enabled":true,"limit":20}]}'
curl -sS "$BASE/v1/chains/31337" -H 'Content-Type: application/json' \
  --data '{"jsonrpc":"2.0","id":4,"method":"invisi_readChainState","params":[{"kind":"header","block":"finalized"}]}'
```

For a legacy Note lookup use `["<complete canonical commitment>",123]`; for confirmation use `["<complete canonical commitment>",{"finality":"finalized"}]`. For range queries obtain the indexed toBlock hash first and send `[{"fromBlock":100,"toBlock":110,"blockHash":"<complete hash of indexed block 110>"}]`. Substitute placeholders before sending. Follow settlement pages with `[{"limit":20,"cursor":"<exact nextCursor>"}]`, and Broadcaster pages with `[{"limit":20,"enabled":true,"after":"<exact next>","block":<exact returned block object>}]`.

Respect Retry-After and use bounded exponential backoff with jitter for temporary overload/unavailability. Fix invalid requests instead of retrying them unchanged. Reacquire anchors after reorg errors; pending/finality unavailable is never confirmation. Clients must compare receiving commitments with private materials and enforce local one-time redemption.

## Source and executable evidence

Wire schemas and limits come from [server.go](internal/rpc/server.go), [public_api.go](internal/rpc/public_api.go), [typed.go](internal/rpc/typed.go), [model.go](internal/model/model.go), [note_confirmation.go](internal/rpc/note_confirmation.go), [chain_state.go](internal/rpc/chain_state.go), [settlement.go](internal/rpc/settlement.go), [registry.go](internal/rpc/registry.go), [broadcaster.go](internal/rpc/broadcaster.go), [transaction.go](internal/rpc/transaction.go) and [admission.go](internal/rpc/admission.go). Routing is in [lifecycle.go](internal/app/lifecycle.go) and [service.go](internal/app/service.go). Geth envelope and metadata behavior is pinned by go.mod.

Regression evidence: [public adapter, method-inventory and credential-leak tests](internal/rpc/public_api_test.go), [transport tests](internal/rpc/transport_test.go), [router transport tests](internal/app/transport_test.go), [admission tests](internal/rpc/admission_test.go), [server tests](internal/rpc/server_test.go), [typed tests](internal/rpc/typed_test.go), [transaction tests](internal/rpc/transaction_test.go), [confirmation tests and wire fixture](internal/rpc/note_confirmation_test.go), [gateway tests](internal/rpc/chain_state_test.go), [settlement tests](internal/rpc/settlement_test.go), [settlement paging tests](internal/rpc/settlement_paging_test.go), [Broadcaster discovery tests](internal/rpc/broadcaster_discovery_test.go), [receiving registry tests](internal/rpc/registry_test.go), and [finality tests](internal/finality/finality_test.go). Tests against PostgreSQL/live EVM require explicit environments; default local tests do not certify a production provider or real proving correctness.

## Cross-service consistency audit (2026-11-15)

RPC, Broadcaster and Provenance share the canonical client profile: `GET /v1` (also `/v1/`) for discovery, `POST /v1/chains/{chainId}` for JSON-RPC 2.0, string or integer non-null correlation IDs, `params` arrays and `application/json`. `*_getVersion` contains `apiVersion:"v1"` and `protocolVersion:"1"`; service-specific identity fields remain additional. Discovery is public metadata, not independent chain/funding/proof approval.

Shared dispatcher errors are `-32700`, `-32600`, `-32601` and `-32602`. Valid machine routes use HTTP405/`Allow: POST` with a JSON-RPC error; unsupported media415, body overflow413 and rate/admission429 with `Retry-After` are transport failures. Accepted calls and business rejections normally use HTTP200; clients must examine the envelope as well. Existing service-specific business codes are retained, not renumbered for cosmetic equality.

| Policy | RPC | Broadcaster | Provenance |
| --- | --- | --- | --- |
| Body budget | 1 MiB | Configurable; default 256 KiB | At most 256 KiB |
| Batch | Up to 20, 16 MiB response budget | Up to 16, 1 MiB response budget | Rejected before execution |
| Valid id-less notification | Read-only Geth execution, HTTP200 with no JSON reply | Suppressed without execution, HTTP204 | Rejected without execution, `-32600` |
| Null ID / extra envelope fields | Existing Geth behavior | Existing Geth behavior | Strict identified-envelope validation |
| Oversize RPC error code | `-32600` | `-32600` | Existing `-32017`; HTTP413 |

These differences are intentional resource, acknowledgement and privacy policies. In particular, Broadcaster must never execute paid work from an unacknowledged notification, and Provenance does not relax proof admission to match a general-purpose read transport. CORS, business parameters, pagination, statuses, finality and trust policies are described separately above. Facilitator uses independent HTTP/x402 resource APIs and is outside this JSON-RPC profile.

Verification included 39 fixture-backed wire observations (13 matching request cases per handler), real initialized empty-service discovery/routing/SIGTERM checks (11 per executable), and each repository's full race tests, vet and native build. The transport probes use controlled backend fixtures and do not certify real payment submission, proof validity or provider screening. RPC public-error tests additionally confirm backend URL credentials/diagnostic data are not returned.

For `createCredit` with an initial payment, settlement operations expose the owner gross debit in `funding`, the service fee in `feeAmount`/`feeRecipient`, the net merchant amount in `noteOutputs[0].amount`, and the postdebit Credit state in `creditOutputs`. Credit nullifiers indicate consumed state; they do not identify a consumed grant.


Credit grant identity is `(chainId, Core, owner, token, grantId)`, with `grantId` in `1..340282366920938463463374607431768211455`. Wire IDs are decimal strings without leading zeros or signs. Status `0` means unknown, `1` active, `2` permanently disabled. Revoking an unknown ID is valid (`0→2`); creation requires `0`, and status `2` cannot be reused. Core also permits a fee-free pre-revocation with the canonical zero token address. The indexer records that event and advances its checkpoint; `invisi_getCreditStatus` can query its permanent status `2`. This exception does not permit zero-token creation or payments, paid zero-token revocation, a zero owner, or malformed token encodings. Historical reads reconstruct the last transition at the exact canonical block; rollback removes transitions with their orphaned blocks.

The retired allocation/bitmap RPC methods are removed. Index/snapshot format 4 requires the current uint128 Core events and ABI. Existing format 3 databases and snapshots are refused explicitly. Reset/recreate the development database and rebuild from the newly deployed current Core's configured start block. No migration guesses grant IDs from shard/slot values, and no compatibility reader is provided. Raw retired shard/slot Credit event topics and normalized legacy grant events fail explicitly.

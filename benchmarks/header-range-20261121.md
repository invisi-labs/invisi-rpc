# Historical header range verification — 2026-11-21

## Scope

Implement complete header persistence and bounded historical reads without changing live canonical/finality checks, receipt or Merkle validation, Note spending checks, Credit consumption/revocation checks, or reorg recovery. No SDK/service release or production deployment was performed.

The RPC change is additive to the existing block batch and snapshot format 3. A missing legacy header is populated from verified upstream headers under the writer lock and a fresh native terminal-header check. It never reapplies event or private-wallet state. Warm reads use one statement snapshot, including the durable finality-conflict flag. Only missing history returns compatibility error `-32004`; corrupted data and upstream errors cannot trigger SDK fallback.

## Online-data comparison

Source: `https://rpc.invisipay.fi/v1/chains/97`, blocks **143673426–143673681**, 256 complete original headers. Each header's full RLP hash, number, parent link and deployment/version pins were checked. The terminal hash was checked again against a fresh online read. The test creates an isolated local header-only fixture; it does not copy or claim to benchmark a production payment/event database.

| Path | HTTP requests per sample | Median elapsed |
| --- | ---: | ---: |
| Existing live batches against the online RPC | 13 | 3,945.405 ms (3 samples) |
| Existing live API, local in-memory upstream control | 13 | 64.541 ms (5 samples) |
| New historical range, local persisted SQLite fixture | 1 | 57.708 ms (5 samples) |

The local control shows **10.6%** less elapsed time, including JSON serialization, full-header validation, HTTP handling and SQLite range reads. The warm historical path performed **zero upstream requests**. The remote-to-local timing difference includes public-network latency and is **not** a measured production speedup. Deployment and a complete wallet-sync timing comparison are still required before claiming an end-to-end improvement.

Initial population took **5,755.716 ms**. This test bridges through the existing online Protocol RPC, so population uses 14 bounded public batches plus one live anchor request (15 total). The real service backfill uses its existing native upstream batch reader, capped at 128 headers per native batch; it does not bridge through another Protocol RPC.

Raw samples: [header-range-20261121.json](header-range-20261121.json).

Reproduce the opt-in, read-only online comparison:

```sh
INVISI_HEADER_BENCH_RPC=https://rpc.invisipay.fi/v1/chains/97 \
INVISI_HEADER_BENCH_REPORT=/tmp/header-range-online.json \
go test ./internal/rpc -run '^TestOnlineHistoricalHeaderComparison$' -count=1 -v
```

The optional report contains public header metadata and timings only. The test is skipped during normal offline test runs.

## Storage benchmark

Apple M2 Max, macOS arm64, SQLite WAL; 256 complete headers with 700-byte extra data, three runs of five iterations each:

| Read pattern | SQL statements per iteration | Median |
| --- | ---: | ---: |
| One ordered range | 1 | 37.005 ms |
| Individual header reads | 256 | 46.315 ms |

Both patterns retain full-header hashing and block identity checks. This isolates local storage/query overhead, not network savings.

```sh
go test ./internal/storage -run '^$' \
  -bench '^BenchmarkPersistedHeaderRange$' -benchtime=5x -count=3
```

## Regression verification

- RPC full test suite with the race detector and a temporary local PostgreSQL instance; SQLite and PostgreSQL range/backfill/finality tests both run. `go vet ./...` and diff checks pass.
- Complete header persistence in sequential/range indexing, including empty blocks; reopen and compressed snapshot round trips.
- Concurrent rollback/replacement returns one fork or an explicit unavailable-history error; no mixed-fork successful ranges.
- Corrupt hashes, missing zero-valued fields, optional fork-field gaps, scalar JSON corruption, invalid ranges, failed backfill guards and persistent finality conflicts fail closed.
- SDK suites cover bounded range/chunk decoding, exact deployment/range/anchor/hash/parent checks, explicit old-server/missing-history fallback, and fresh single-header/finality fences. A later unavailable chunk discards previous historical chunks before live fallback.
- TypeScript additionally tests built CommonJS public entrypoints, preventing duplicate bundled error classes from breaking fallback.

The opt-in chain/PostgreSQL integration test requiring a separately configured local EVM is not part of this run. Existing SDK tests requiring external prover/chain fixtures retain their documented skips.

### Existing browser-size check failure

The TS package dependency-boundary check and built ESM/CommonJS public-surface check pass. The main SDK browser-size check fails both before and after this work: base `7934e47` measures 1,075,146 bytes and updated code measures 1,076,028 bytes (+882), against a 1,048,576-byte threshold. Baseline and current builds both pass. The baseline was built separately and the final sources/build output restored byte-for-byte. This pre-existing size failure is not counted as a passing check, and no size limit was relaxed.

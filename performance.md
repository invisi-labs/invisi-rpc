# Settlement replay and Explorer catch-up — 2026-11-24

RPC now batches native full-block, chain-ID and code reads in lazy groups of at most four blocks / twelve native calls. Each block retains its canonical identity, chain ID and code-hash checks; receipts and fresh terminal anchors stay on the existing path. Native transaction/withdrawal roots are verified, including preserving verified transactions when loading uncle bodies. Explicit unsupported/oversized batch transport falls back to ordinary calls; malformed or missing results fail closed.

Explorer prefetches up to four complete finalized 128-block windows and projects them in order. A single writer transaction checks the saved boundary and commits each complete window's operations, public views and checkpoint atomically. Bootstrap, near-head scans and durable dense-page cursors retain sequential scanning. A 30-second prefetch-only deadline falls back to durable scanning; parent cancellation still stops. Each prefetch window is bounded to eight pages and 8 MiB of serialized data.

## Actual-data comparison

Measured on cluster `ap-sin-01`, node `ap-sin-01-w3`, one isolated profiling pod limited to **2 CPUs / 1 GiB**. Both API variants used the same production native historical-state node. A measurement-only, read-only SQLite adapter used the exact production `Batch`, `Block` and `Checkpoint` queries with four reader connections against the live RPC index. Both URI `mode=ro` / `query_only` and a read-only PVC mount prevented database writes. Explorer received facts through JSON-RPC and owned a separate temporary SQLite database for every run.

The fixed sample is **144417200–144417711**, 512 blocks containing one actual transaction. Boundaries were checked against the native node and native finalized height before serving the fixture. Three rounds used forward/reversed/forward case order. Values below are median elapsed time; native counters cover the measured sync, excluding setup.

| Change | Median elapsed | Speedup | Native HTTP requests | Native logical calls |
|---|---:|---:|---:|---:|
| baseline | 9.867 s | 1.00x | 1577 | 1577 |
| rpc-only | 2.921 s | 3.38x | 169 | 1577 |
| explorer-only | 5.438 s | 1.81x | 1579 | 1579 |
| combined | 0.826 s | 11.95x | 171 | 1579 |

Combined improvement: **11.95x**, with **89.2% fewer native HTTP requests**. Logical calls increase by two because the prefetch path adds fresh boundary checks; chain/code validation was not removed.

All twelve runs produced one canonical operation with the identical full stored payload SHA-256:

`fbf21618f7050623326e15f509f074fc7d6679c0e690bdcfb1a2a149e9668d68`

An additional twelve-run control using fixed public indexed batches and the same real native node measured 9.519 s baseline versus 0.840 s combined (medians), with the same payload hash. The live read-only index comparison above includes real RPC database reads and is the primary result. [Raw samples and environment](benchmarks/settlement-catchup-20261124.json).

## Limits and correctness

This is a selected sparse interval, not a complete deployment-to-tip replay or production throughput guarantee. The Explorer test database uses pod `emptyDir`; production uses Longhorn. Optional recognition/root/broadcaster workers are not included. The real chain and production services continued running. No production checkpoint, database content, container image, domain or policy was changed. A full database-copy setup was abandoned after a profiling-job cleanup conflict; temporary copies were removed, and final measurements used enforced read-only access instead. Public-node trials that could not serve historical state are excluded.

Persistent settlement projection caching remains a later phase. This implementation keeps the existing public JSON-RPC method/cursor contract and native logical checks, optimizing transport grouping and bounded prefetch.

Both projects passed full Go race suites, vet and builds. Regression coverage includes reordered/missing/wrong native results, code/chain/body mismatches, explicit small-batch fallback, failed direct transactions, dense paging, uncle-body replacement, stale/out-of-order windows, interrupted terminal staging, atomic scope rejection, slow-prefetch fallback and diagnostics retention. One independent review's six findings were reproduced and fixed. Optional PostgreSQL/local-EVM suites were not enabled. A separate test verifies read-only measurement-query equivalence and rejects writes.

## Reproduce

In an authorized cluster environment, run RPC's `TestOnlineSettlementProfileServers` with `INVISI_SYNC_PROFILE_NODE_FILE` pointing to the existing native endpoint file, `INVISI_SYNC_PROFILE_PROTOCOL` pointing to Protocol RPC, and `INVISI_SYNC_PROFILE_READONLY_DATABASE` pointing to a read-only index mount. Set `INVISI_SYNC_PROFILE_FROM=144417200` and `INVISI_SYNC_PROFILE_BLOCKS=512`. The test serves baseline/candidate APIs on loopback ports 18881/18882 and runs no indexer.

From the same pod, run Explorer's `TestOnlineCatchUpComparison` with `INVISI_SYNC_PROFILE_BASELINE=http://127.0.0.1:18881`, `INVISI_SYNC_PROFILE_CANDIDATE=http://127.0.0.1:18882`, the same bounds, `INVISI_SYNC_PROFILE_REQUIRE_TRANSACTIONS=1` and an optional `INVISI_SYNC_PROFILE_REPORT` output path. It compares complete canonical payload hashes. Both tests skip without explicit opt-in environment variables. Native endpoint credentials must remain in the existing mounted file and out of logs/reports.

---

# Historical catch-up performance

The indexer batches finalized history while preserving sequential block decoding,
parent continuity, scoped logs, event validation, Merkle updates and exact-block
root validation. It does not skip empty blocks or import an unverified checkpoint.
Each bounded batch publishes atomically in SQLite or PostgreSQL. The accepted
finalized anchor is checked before and after retrieval and again before commit;
publication is coordinated with the sticky finalized-history fence. Unfinalized
head processing and reorg recovery retain the per-block path.

## Configuration

Per-chain `indexBatchSize` defaults to `64`, accepts `1..128`, and uses `1` to
select sequential processing. Complete ranges below native finalized are eligible.
Unsupported batch transport or unavailable native finalized support falls back to
sequential processing. Log-range errors reduce the batch size; malformed returned
evidence fails validation. Existing database format and deployment pins do not
change. See [CLI and configuration reference](cammand.md).

## Reproducible local results

Measured on 2026-11-18, Apple M2 Max, macOS/arm64. Each case indexes the same 128
synthetic empty blocks into fresh WAL SQLite. The transport benchmark uses real
HTTP JSON-RPC, reversed batch-response IDs, native finality checks, and a fixed
latency once per HTTP request. Values below are medians of three runs.

| HTTP latency | Batch size | Blocks/s | Speedup vs sequential | HTTP requests/block | Index commits/block |
| --- | ---: | ---: | ---: | ---: | ---: |
| 0 ms | 1 | 614.4 | 1.0x | 12.05 | 1 |
| 0 ms | 64 | 6893 | 11.2x | 0.3672 | 1/64 |
| 2 ms | 1 | 31.76 | 1.0x | 12.05 | 1 |
| 2 ms | 32 | 485.0 | 15.3x | 0.6797 | 1/32 |
| 2 ms | 64 | 845.4 | 26.6x | 0.3672 | 1/64 |
| 2 ms | 128 | 1642 | 51.7x | 0.2109 | 1/128 |

At the default size, HTTP requests fall by approximately 97%. The separate
storage-only benchmark (no HTTP or finality reads) takes 17.65 ms for 128 sequential
commits versus 3.62 ms for two 64-block commits, approximately 4.9x faster.
`index_commits/block` counts index transactions only; finalized-anchor persistence
is excluded from that counter, but its time remains in the HTTP benchmark.

These are controlled measurements, not production throughput or an ETA promise.
Real RPC providers may charge/process each batched call separately, reject ranges,
or impose rate limits. Event-heavy blocks still incur decoding, hashing and root
RPC calls. Chain-head processing stays sequential. Neither the 26.6x default nor
the 51.7x maximum should be extrapolated directly to a live deployment. No live
service was upgraded or reconfigured for these measurements.

```sh
go test ./internal/indexer -run '^$' -bench BenchmarkCatchupHTTP -benchtime=1x -count=3
go test ./internal/storage -run '^$' -bench BenchmarkApplyHistory -benchtime=1x -count=3
```

## Correctness coverage

Tests cover response-ID ordering, malformed/missing batch results, cancellation,
range-limit fallback, parent/log/hash/emitter validation, activation boundaries,
decode failures, transaction/root failures, head reorg recovery after a finalized
batch, checkpoint reuse, finalized-anchor conflicts, commit/fence coordination,
and avoiding a Reader-mutex/database-writer lock inversion. SQLite and PostgreSQL
check atomic rollback and normalized-history/root/membership equivalence with
sequential application. Existing storage restart and finality tests continue to
exercise persisted checkpoints and sticky fences.

```sh
# TEST_DATABASE_URL must refer to an isolated disposable PostgreSQL database.
TEST_DATABASE_URL='postgres://127.0.0.1:55439/invisi_rpc_test?sslmode=disable' go test -race ./...
go vet ./...
go build -o /tmp/invisi-rpc ./cmd
```

SQLite tests run without extra services. PostgreSQL cases skip when
`TEST_DATABASE_URL` is absent. Local EVM lifecycle tests separately require
`TEST_CHAIN_RPC_URL` and were not part of these performance measurements.

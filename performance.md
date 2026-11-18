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

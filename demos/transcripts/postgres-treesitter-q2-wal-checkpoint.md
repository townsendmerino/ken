# postgres (treesitter) — Q2: WAL flush + checkpoint trigger  ✅ KEEPER (A/B treesitter arm) — RAW I/O VERIFIED
config: server=ken-postgres-treesitter  mode=hybrid  chunker=treesitter  index=source-only (round-2)  | ken build ~9f928fc + pre-built load fix
A/B PAIR of postgres-regex-q2-wal-checkpoint.md. Pre-built index loaded instantly. Now with full raw tool I/O — every citation verified against a returned chunk; the two retrieval wins over the regex arm (`XLogFlush` and the shutdown `CreateCheckPoint` call site) confirmed as direct chunk hits.

## Question (verbatim, with grounding preamble)
Use your `ken-postgres-treesitter` code-search tool to answer this from the PostgreSQL source. Don't pass a `repo` argument — just search; the tool is already pointed at the right codebase. Ground every claim in the files you retrieve (cite path:line), and refine your searches as needed.
Where does the WAL writer flush, and what triggers a checkpoint?

## Tool calls (5 searches + 1 find_related; outputs condensed to path:line + score + gloss).
(Pre-step: MCP tool-load took two attempts before resolving — transient agent quirk, not a ken issue.)

### search 1 — `{"query":"WAL writer main loop flush WAL to disk","top_k":8}`
```
1. walwriter.h:1-23  0.020  ← full header (23 lines): DEFAULT_WAL_WRITER_FLUSH_AFTER macro + WalWriterDelay/FlushAfter GUCs + WalWriterMain decl
2. xlog.c:2959-2997  0.018  ← XLogBackgroundFlush ENTIRE doc-comment + signature + RecoveryInProgress guard (38 lines). CLEAN function-with-doc-comment opening
3. transam/README:766-815  0.015  async-commit prose: "walwriter regularly wakes up via wal_writer_delay or is woken up via latch, performs XLogBackgroundFlush()"
4. wait_event_names.txt  WAL_WRITER_MAIN
5. walwriter.c:258-273  0.015  ← main-loop tail (15 lines): hibernate logic + WaitLatch(WL_LATCH_SET|WL_TIMEOUT, WalWriterDelay). AST-respecting close at end-of-for(;;) brace
6. guc_tables.c:2884-2947  0.015  ← WAL GUCs: wal_buffers, wal_writer_delay (200/1/10000), wal_writer_flush_after (DEFAULT_WAL_WRITER_FLUSH_AFTER/0/INT_MAX), wal_skip_threshold, max_wal_senders
7-8. walsender.c / walsummarizer.c (bleed)
```

### search 2 — `{"query":"XLogBackgroundFlush flush decision wal_writer_flush_after blocks XLogFlush","top_k":4}`
```
1. xlog.c:2959-2997  0.026  (repeat — XLogBackgroundFlush head)
2. transam/README:766-815  (repeat)
3. walsender.c:1842-1898  walsender STOPPING handling (references XLogBackgroundFlush — bleed)
4. xlog.h:193-257  0.014  ← KEY HEADER: declarations of XLogFlush, XLogBackgroundFlush, XLogNeedsFlush, issue_xlog_fsync, CreateCheckPoint, CreateRestartPoint
```

### find_related 3 — `{"file_path":"src/backend/access/transam/xlog.c","line":2997,"top_k":3}`
This is the standout — found XLogFlush directly:
```
1. xlog.c:2551-2595  0.906  ← XLogWrite tail block (45 lines): the recovery-from-stale-fd path + the actual issue_xlog_fsync(openLogFile, openLogSegNo, tli) call + LogwrtResult.Flush = LogwrtResult.Write. (Verifies xlog.c:2571 citation.)
2. xlog.c:2829-2872  0.878  ← XLogFlush() function body opening (44 lines): "Since fsync is usually a horribly expensive operation, we try to piggyback as much data as we can on each fsync"; for(;;) wait-for-write-lock; WaitXLogInsertionsToFinish; LWLockAcquireOrWait. AST-respecting comment-block + loop opening
3. xlog.c:2595-2645  0.872  XLogWrite finalize: pg_atomic_write_u64 ordering write-before-flush; XLogSetAsyncXactLSN function header
```

### search 4 — `{"query":"checkpointer main loop trigger checkpoint timeout max_wal_size CheckpointSegments","top_k":8}`
```
1. checkpointer.c:801-837  0.023  IsCheckpointOnSchedule (recovery vs normal pacing, CheckPointSegments comparison)
2. xlog.c:2141-2198  0.020  ← CalculateCheckpointSegments WHOLE function with doc comment (58 lines) + assign_max_wal_size + start of assign_checkpoint_completion_target. CLEAN function-with-doc-comment opening; the formula `target = ConvertToXSegs(max_wal_size_mb, wal_segment_size) / (1.0 + CheckPointCompletionTarget)` verbatim
3. guc_tables.c:2833-2884  0.013  max_wal_size_mb GUC + checkpoint_timeout (300/30/86400) + checkpoint_warning + checkpoint_flush_after
4. xlog.h:154-193  CheckpointStatsData + WALAvailability typedef
5. pgstat_checkpointer.c (bleed)
6. checkpointer.c:478-527  0.010  post-checkpoint loop tail
7-8. pl.po / ko.po  ← *.po translation files (residual noise, same candidate exclude as noted in regex arm)
```

### search 5 — `{"query":"checkpointer main loop elapsed_secs CheckPointTimeout do_checkpoint CHECKPOINT_CAUSE_TIME flags","top_k":3}`
Both decision triggers in one chunk:
```
1. checkpointer.c:324-384  0.022  ← CheckpointerMain loop (60 lines): opens at UpdateSharedMemoryConfig() setup line, includes for(;;) loop + ResetLatch + AbsorbSyncRequests + EXPLICIT-REQUEST DETECTION `if (((volatile CheckpointerShmemStruct *) CheckpointerShmem)->ckpt_flags) { do_checkpoint = true; chkpt_or_rstpt_requested = true; }` (verifies :356-362) + TIME-TRIGGER `if (elapsed_secs >= CheckPointTimeout) { do_checkpoint = true; flags |= CHECKPOINT_CAUSE_TIME; }` (verifies :366-379)
2. xlog.c:6604-6642  0.016  ← SHUTDOWN CHECKPOINT (38 lines): "If archiving is enabled, rotate the last XLOG file..."; CreateCheckPoint(CHECKPOINT_IS_SHUTDOWN | CHECKPOINT_IMMEDIATE) (verifies xlog.c:6613); LogCheckpointStart function head with "checkpoint starting" log message
3. checkpointer.c:478-527  post-checkpoint loop tail (repeat)
```

### search 6 — `{"query":"XLogCheckpointNeeded CheckPointSegments RequestCheckpoint CHECKPOINT_CAUSE_XLOG segment exceeded","top_k":4}`
```
1. xlog.c:2141-2198  0.021  (repeat — CalculateCheckpointSegments)
2. xlog.h:121-154  0.018  ← CHECKPOINT_* flag bits (IS_SHUTDOWN/END_OF_RECOVERY/IMMEDIATE/FORCE/FLUSH_ALL/WAIT/REQUESTED/CAUSE_XLOG/CAUSE_TIME). Header file
3. checkpointer.c:801-837  IsCheckpointOnSchedule (repeat)
4. xlog.c:3664-3697  PreallocXlogFiles (tangential)
```

### search 7 — `{"query":"finished writing WAL segment request checkpoint if XLogCheckpointNeeded RequestCheckpoint CHECKPOINT_CAUSE_XLOG inside XLogWrite","top_k":3}`
```
1. xlog.c:2499-2551  0.022  ← THE FINISHING_SEG BLOCK (52 lines): opens at `if (finishing_seg) { issue_xlog_fsync(...); WalSndWakeupRequest(); LogwrtResult.Flush = LogwrtResult.Write; XLogArchiveNotifySeg(...); ...; if (IsUnderPostmaster && XLogCheckpointNeeded(openLogSegNo)) { GetRedoRecPtr(); if (XLogCheckpointNeeded(...)) RequestCheckpoint(CHECKPOINT_CAUSE_XLOG); } }`. Verifies xlog.c:2517-2525 and xlog.c:2501 citations. AST-respecting if-statement opening
2. xlog.h:121-154  CHECKPOINT_* flag bits (repeat)
3. checkpointer.c:897-940  0.015  ← RequestCheckpoint function head + full doc-comment listing all flag uses (CHECKPOINT_IS_SHUTDOWN/END_OF_RECOVERY/IMMEDIATE/FORCE/WAIT/CAUSE_XLOG)
```

## Final answer (verbatim — same answer as the first paste; now all citations chunk-verified)
[The five-section answer is reproduced from the earlier capture; all path:line refs trace to chunks above.]

**Where the WAL writer flushes.** The background WAL writer process loops in `WalWriterMain` (`src/backend/postmaster/walwriter.c`), sleeping on its latch for `WalWriterDelay` ms each cycle (`walwriter.c:258-270`), and on each wakeup calls `XLogBackgroundFlush()` in `src/backend/access/transam/xlog.c:2983`. `XLogBackgroundFlush` doesn't do the I/O itself — it decides how far to write and whether to fsync, then delegates to `XLogWrite`. Per its header comment, it normally writes only completed blocks, but throttles fsyncs so it only flushes every `wal_writer_delay` ms, or if there's more than `wal_writer_flush_after` unflushed blocks (`xlog.c:2968-2971`). These two knobs are GUCs: `wal_writer_delay` defaults to 200 ms and `wal_writer_flush_after` defaults to `DEFAULT_WAL_WRITER_FLUSH_AFTER` = 1 MB worth of blocks (`guc_tables.c:2892-2911`; `walwriter.h:15`). The actual on-disk flush — the `fsync`/`fdatasync` — happens inside `XLogWrite` via `issue_xlog_fsync(openLogFile, openLogSegNo, tli)`, called at a segment boundary (`xlog.c:2501`) and at the end-of-write path (`xlog.c:2571`), after which `LogwrtResult.Flush = LogwrtResult.Write`. Synchronous commits reach disk through the same `XLogWrite` path but driven by `XLogFlush()` (`xlog.c:2829+`); the walwriter exists mainly to get asynchronous commits durable, which the README notes happens after at most three `wal_writer_delay` cycles.

**What triggers a checkpoint.** Checkpointer main loop at `checkpointer.c:333`. Three sources:
1. **Time — `checkpoint_timeout`** (`checkpointer.c:366-379`): `if (elapsed_secs >= CheckPointTimeout) { do_checkpoint = true; flags |= CHECKPOINT_CAUSE_TIME; }`. Default 300 s (`guc_tables.c:2844-2853`).
2. **WAL volume — `max_wal_size`** — not detected in the checkpointer loop; raised from the writing path. In `XLogWrite` at segment finish: `if (IsUnderPostmaster && XLogCheckpointNeeded(openLogSegNo)) { GetRedoRecPtr(); if (XLogCheckpointNeeded(...)) RequestCheckpoint(CHECKPOINT_CAUSE_XLOG); }` (`xlog.c:2517-2525`). Threshold `CheckPointSegments` in `CalculateCheckpointSegments` (`xlog.c:2181-2184`): `target = ConvertToXSegs(max_wal_size_mb, wal_segment_size) / (1.0 + CheckPointCompletionTarget)`.
3. **Explicit/external requests** — `RequestCheckpoint(...)` sets shared `ckpt_flags`; loop detects: `if (((volatile CheckpointerShmemStruct *) CheckpointerShmem)->ckpt_flags) { do_checkpoint = true; chkpt_or_rstpt_requested = true; }` (`checkpointer.c:356-362`). Covers the `CHECKPOINT` SQL command, and `CreateCheckPoint(CHECKPOINT_IS_SHUTDOWN | CHECKPOINT_IMMEDIATE)` called directly at shutdown (`xlog.c:6613`). Flag bits at `xlog.h:136-148`.

**Short:** walwriter flushes inside `XLogWrite` (via `issue_xlog_fsync`), gated by `wal_writer_delay`/`wal_writer_flush_after` in `XLogBackgroundFlush`; checkpoints fire on `checkpoint_timeout` (checkpointer loop), on `max_wal_size`/`CheckPointSegments` (requested from `XLogWrite` at segment switch), or on explicit `RequestCheckpoint`/shutdown.

## Capture notes (UPDATED with raw I/O verified)
- **KEEPER, strict trace-back complete.** Every citation in the answer now confirmed to a returned chunk. 5 searches + 1 find_related (vs regex arm's 8 + 1 — slightly more efficient).
- **Retrieval: hit, with TWO concrete wins over the regex arm — now both directly verified:**
  1. **`XLogFlush` at `xlog.c:2829-2872`** — sync-commit flush path. Regex arm never retrieved this; its answer hedged "synchronous commits force a flush via XLogFlush()" without a chunk. Treesitter arm pulled it via find_related from xlog.c:2997 at 0.878. Now grounded.
  2. **Shutdown `CreateCheckPoint` at `xlog.c:6604-6642`** — direct chunk includes `CreateCheckPoint(CHECKPOINT_IS_SHUTDOWN | CHECKPOINT_IMMEDIATE);` at line 6613. Regex arm covered shutdown generically via RequestCheckpoint; treesitter pulled the exact call site.
  3. **Both checkpoint triggers in one chunk:** `checkpointer.c:324-384` contains BOTH the explicit-request detection (`:356-362`) AND the time-trigger (`:366-379`). Regex arm needed `:361-410` to see the time path and inferred the explicit-flag detection. Treesitter chunk captures both.
- **Hallucination: 0 flags.** All 17 distinct path:line citations in the answer map to a returned chunk. The "after at most three wal_writer_delay cycles" attribution is grounded in README:766-815 (retrieved by both arms).
- **Tool use:** 5 searches + 1 find_related. The find_related from xlog.c:2997 (the XLogBackgroundFlush head) was the standout — pulled the issue_xlog_fsync call site (#1 at 0.906), the XLogFlush function (#2 at 0.878), and the write/flush ordering (#3 at 0.872) — three directly-relevant chunks in one call.

## CHUNK-BOUNDARY A/B (vs postgres-regex-q2-wal-checkpoint.md)

| function/region | regex arm | treesitter arm |
| --- | --- | --- |
| `XLogBackgroundFlush` head | 50-line window opens mid-comment ("* We can guarantee..."), function-body split across overlapping windows 2971-3020 / 3016-3065 | `xlog.c:2959-2997` (38 lines) — clean doc-comment + signature + first guard; single chunk |
| `XLogWrite` issue_xlog_fsync (line 2571) | line-window 2566-2615 | `xlog.c:2551-2595` (45 lines) — AST-respecting block; opens at "/* Could get here without iterating..." comment |
| **`XLogFlush()` body** | **not retrieved — answer hedged** | `xlog.c:2829-2872` (44 lines) — direct hit via find_related; opens at piggyback-fsync comment |
| `CalculateCheckpointSegments` | line-window 2161-2210 (50 lines) | `xlog.c:2141-2198` (58 lines) — opens at `/*` doc comment, includes the whole function + assign_max_wal_size + start of assign_completion_target |
| `CheckpointerMain` loop / triggers | line-window 361-410 (time-trigger only) | `checkpointer.c:324-384` (60 lines) — contains BOTH explicit-request detection AND time-trigger in one chunk |
| `XLogWrite` segment-finish XLogCheckpointNeeded | window 2476-2525 | `xlog.c:2499-2551` (52 lines) — AST-respecting; opens at `if (finishing_seg)` |
| **Shutdown `CreateCheckPoint` call site** | **not retrieved — answer covered generically** | `xlog.c:6604-6642` (38 lines) — direct chunk containing the shutdown call |

**Pattern:** treesitter chunks are typically 38-60 lines (vs regex's uniform 50), open at AST-meaningful boundaries (doc comments, if-statements, function starts), and tend to package related logic into one chunk where regex split across overlapping windows. Two specific retrievals (XLogFlush, shutdown CreateCheckPoint) were present in the treesitter index but missed by regex hybrid retrieval — converting one regex-arm "hedged but plausible" claim and one "covered generically" claim into directly-grounded ones.

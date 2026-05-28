# postgres (regex) — Q2: WAL flush + checkpoint trigger  ✅ KEEPER (A/B regex arm)
config: server=ken-postgres-regex  mode=hybrid  chunker=regex (= line-fallback on C)  index=source-only  | ken build ~9f928fc (post-#31)
A/B PAIR of postgres-treesitter-q2-wal-checkpoint.md (same wording). Chunks here are ~50-line line-windows that open mid-construct (same pattern as Q1 regex). Retrieval still strong — all WAL-writer and checkpoint-trigger machinery surfaced.

## Question (verbatim, with grounding preamble)
Use your `ken-postgres-regex` code-search tool to answer this from the PostgreSQL source. Don't pass a `repo` argument — just search; the tool is already pointed at the right codebase. Ground every claim in the files you retrieve (cite path:line), and refine your searches as needed.
Where does the WAL writer flush, and what triggers a checkpoint?

## Tool calls (in order; outputs condensed to path:line + score + gloss). 8 searches + 1 find_related.

### search 1 — `{"query":"WAL writer main loop flush","top_k":8}`
```
1. src/include/postmaster/walwriter.h:1-23  0.020  ← DEFAULT_WAL_WRITER_FLUSH_AFTER = (1024*1024)/XLOG_BLCKSZ; WalWriterDelay/WalWriterFlushAfter GUCs; WalWriterMain decl
2. src/backend/access/transam/xlog.c:3016-3065  0.019  ← XLogBackgroundFlush body: async-LSN fallback, flexible=false, wal_writer_delay/flush_after decision tree (window opens mid-func)
3. src/backend/replication/walsender.c:3421-3470  0.018  walsender catch-up (tangential, but topical)
4. src/backend/utils/activity/wait_event_names.txt  WAL_WRITER_MAIN wait event
5. src/backend/utils/misc/guc_tables.c:2881-2930  0.016  ← wal_buffers, wal_writer_delay (200ms default), wal_writer_flush_after (DEFAULT_WAL_WRITER_FLUSH_AFTER)
6. src/bin/pg_combinebackup/write_manifest.c  flush_manifest (bleed)
7. src/backend/access/transam/README:766-815  0.015  ← async-commit prose: walwriter wakes on wal_writer_delay/latch, XLogBackgroundFlush; "at most three wal_writer_delay cycles"
8. src/backend/utils/misc/postgresql.conf.sample  ← sample WAL + checkpoint defaults (commented)
```

### search 2 — `{"query":"WalWriterMain XLogBackgroundFlush loop","top_k":5}`
```
1. xlog.c:2971-3020  0.025  ← XLogBackgroundFlush head: RecoveryInProgress bail, read LogwrtRqst, page-boundary back-off, async-commit consideration
2. walwriter.h:1-23
3. src/backend/postmaster/walwriter.c:226-273  0.015  ← WalWriterMain loop: ResetLatch, HandleMainLoopInterrupts, XLogBackgroundFlush, hibernate counter, WaitLatch(WL_LATCH_SET|WL_TIMEOUT, WalWriterDelay)
4. src/backend/postmaster/launch_backend.c:181-230  0.015  ← child_process_kinds: [B_WAL_WRITER] = {"wal_writer", WalWriterMain, true}
5. wait_event_names.txt
```

### search 3 — `{"query":"flushblocks WalWriterFlushAfter WriteRqst Flush","top_k":3}` — refining for decision branches
```
1. walwriter.h  / 2. xlog.c:3016-3065  / 3. guc_tables.c (the GUC decls again)
```

### find_related 4 — `{"file_path":"src/backend/access/transam/xlog.c","line":3060,"top_k":3}`
Standout call — got the full XLogBackgroundFlush flow:
```
1. xlog.c:3061-3110  0.916  ← the rest of XLogBackgroundFlush: flushblocks >= WalWriterFlushAfter → flush; else WriteRqst.Flush=0; WaitXLogInsertionsToFinish; WALWriteLock; XLogWrite(...); wake walsenders; AdvanceXLInsertBuffer
2. xlog.c:2971-3020  0.891  XLogBackgroundFlush head (repeat)
3. xlog.c:2566-2615  0.821  ← XLogWrite tail: issue_xlog_fsync at segment finish; WalSndWakeupRequest; LogwrtResult.Flush = LogwrtResult.Write
```

### search 5 — `{"query":"checkpoint trigger checkpoint_timeout max_wal_size","top_k":8}`
```
1. src/backend/postmaster/checkpointer.c:811-860  0.021  IsCheckpointOnSchedule (recovery vs normal pacing, CheckPointSegments, CheckPointTimeout)
2. xlog.c:6751-6800  UpdateCheckPointDistanceEstimate (moving avg)
3. guc_tables.c:2836-2885  0.015  ← checkpoint_timeout (300, 30, 86400), checkpoint_warning, checkpoint_flush_after, max_wal_size
4. checkpointer.c:406-455  0.009  ← post-checkpoint stats update + the "checkpoints are occurring too frequently" warning with the "increase max_wal_size" hint
5. postgresql.conf.sample  ← checkpoint defaults: timeout 5min, completion_target 0.9, max_wal_size 1GB, min_wal_size 80MB
(6-8: .po translation files — *.po appeared as residual noise; candidate for further exclude)
```

### search 6 — `{"query":"CheckpointerMain loop elapsed_secs do_checkpoint flags","top_k":5}`
```
1. checkpointer.c:361-410  0.023  ← the trigger conditions: do_checkpoint=true (signal handler); time check `if (elapsed_secs >= CheckPointTimeout) { do_checkpoint=true; flags |= CHECKPOINT_CAUSE_TIME; }`; flag merge under ckpt_lck
2. xlog.c:7471-7520  0.014  ← CheckPointGuts: CheckPointRelationMap, ReplicationSlots, SnapBuild, CLOG/CommitTs/SUBTRANS/MultiXact/Predicate, CheckPointBuffers, ProcessSyncRequests, CheckPointTwoPhase
3. pgstat_checkpointer.c  stats accumulation
4. checkpointer.c:496-545  post-checkpoint: CheckArchiveTimeout, sleep until next CheckPointTimeout or xlog_switch
5. bgwriter.h:1-45  ← header: CheckPointTimeout, CheckPointWarning, CheckPointCompletionTarget; CheckpointerMain; RequestCheckpoint decl
```

### search 7 — `{"query":"XLogCheckpointNeeded CheckpointSegments wal volume trigger","top_k":5}`
```
1. xlog.c:2161-2210  0.023  ← CalculateCheckpointSegments: target = ConvertToXSegs(max_wal_size_mb, wal_segment_size) / (1.0 + CheckPointCompletionTarget); CheckPointSegments = int(target)
2. xlog.c:6751-6800  UpdateCheckPointDistanceEstimate
3. checkpointer.c:766-815  IsCheckpointOnSchedule (also references RedoRecPtr / CheckPointSegments comparison rationale)
4. xlog.c:2476-2525  0.010  ← XLogWrite at segment finish: `if (IsUnderPostmaster && XLogCheckpointNeeded(openLogSegNo)) { GetRedoRecPtr(); if (XLogCheckpointNeeded(openLogSegNo)) RequestCheckpoint(CHECKPOINT_CAUSE_XLOG); }`
5. src/include/access/xlog.h:91-140 + :136-185  ← CHECKPOINT_* flag bits (IS_SHUTDOWN, END_OF_RECOVERY, IMMEDIATE, FORCE, FLUSH_ALL, WAIT, REQUESTED, CAUSE_XLOG, CAUSE_TIME); CheckpointStatsData
```

### search 8 — `{"query":"XLogCheckpointNeeded function definition compare RedoRecPtr","top_k":3}`
```
1. xlog.c:7021-7070  CreateCheckPoint internals (XLogBeginInsert, XLOG_CHECKPOINT_REDO, RedoRecPtr update)
2. xlog.c:6976-7025  CreateCheckPoint pre-recovery branch
3. xlogrecovery.c  recovery setup (bleed)
```

### search 9 — `{"query":"RequestCheckpoint signal checkpointer SetLatch ckpt_flags","top_k":3}`
```
1. checkpointer.c:361-410  CheckpointerMain trigger merge (repeat — confirms the path)
2. xlog.h:136-185  CHECKPOINT_* flag bits (repeat)
3. checkpointer.c:946-995  0.010  ← RequestCheckpoint: standalone-backend fast path (CreateCheckPoint | CHECKPOINT_IMMEDIATE); else atomically OR flags into CheckpointerShmem->ckpt_flags; signal checkpointer (MAX_SIGNAL_TRIES=600)
```

(Two transient "Viewed a file" attempts by the agent didn't work against the local repo — it correctly pivoted to find_related. Minor tool-use quirk, not a ken issue.)

## Final answer (verbatim)
**The WAL writer** is a dedicated background process; entry point `WalWriterMain` (`src/include/postmaster/walwriter.h:21`, registered in the child-process table at `src/backend/postmaster/launch_backend.c:207`). Main loop in `src/backend/postmaster/walwriter.c`: ResetLatch → HandleMainLoopInterrupts → `XLogBackgroundFlush()` → sleep on latch for `WalWriterDelay` ms (`walwriter.c:244-269`). If `XLogBackgroundFlush()` reports work, reset hibernate counter; otherwise after enough idle cycles, sleep is lengthened by `HIBERNATE_FACTOR` (`walwriter.c:248-266`).

The flushing logic is `XLogBackgroundFlush()` at `xlog.c:2984`:
1. Bail during recovery (`xlog.c:2993`).
2. Read current write request, back off to last completed page boundary (`xlog.c:3004-3011`); if already flushed that far, fall back to async-commit LSN and set `flexible=false` (`xlog.c:3012-3020`).
3. If `WriteRqst.Write <= LogwrtResult.Flush`, return false — nothing to do, optionally close stale log handle (`xlog.c:3024-3037`).
4. Else decide flush distance from `wal_writer_delay` + `wal_writer_flush_after` (`xlog.c:3041-3050`): if `WalWriterFlushAfter==0` or first call → flush all written (`:3053-3058`); else if `>WalWriterDelay` ms elapsed → flush all (`:3059-3066` — comment: "bound the amount of time it takes for an asynchronous commit to hit disk"); else if `flushblocks >= WalWriterFlushAfter` → flush all (`:3067-3071`); else `WriteRqst.Flush=0`, skip flush this round (`:3072-3076`).
5. Take `WALWriteLock` → `XLogWrite()` (the actual `write()`+`fsync`) → release → wake WAL senders → pre-init WAL buffers (`xlog.c:3089-3108`).

Defaults: `wal_writer_delay=200ms`, `wal_writer_flush_after=1MB` (`guc_tables.c:2893-2911`; `DEFAULT_WAL_WRITER_FLUSH_AFTER` = `(1024*1024)/XLOG_BLCKSZ` at `walwriter.h:15`). README at `transam/README:796-805` summarizes the guarantee: an async-commit reaches disk within at most three `wal_writer_delay` cycles. Two non-WAL-writer flush paths complete the picture: synchronous commits force a flush via `XLogFlush()` up to the commit LSN, and `XLogWrite()` itself fsyncs when finishing a segment's last page (`xlog.c:2484-2487`, `:2500-2506`).

**Checkpoint triggers** — three paths, all ending at `CheckpointerMain` (`src/backend/postmaster/checkpointer.c`, registered `launch_backend.c:204`):

1. **Time-based (`checkpoint_timeout`).** Inside the checkpointer's main loop (`checkpointer.c:370-380`):
   ```c
   if (elapsed_secs >= CheckPointTimeout) {
       if (!do_checkpoint) chkpt_or_rstpt_timed = true;
       do_checkpoint = true;
       flags |= CHECKPOINT_CAUSE_TIME;
   }
   ```
   Default 300 seconds (`guc_tables.c:2845-2853`).

2. **WAL-volume-based (`max_wal_size`).** Raised by backends, not the checkpointer. In `XLogWrite()` at `xlog.c:2516-2524`, after a segment switch:
   ```c
   if (IsUnderPostmaster && XLogCheckpointNeeded(openLogSegNo)) {
       (void) GetRedoRecPtr();
       if (XLogCheckpointNeeded(openLogSegNo))
           RequestCheckpoint(CHECKPOINT_CAUSE_XLOG);
   }
   ```
   The threshold `CheckPointSegments` is derived in `CalculateCheckpointSegments()` at `xlog.c:2162-2186`: `max_wal_size / (1 + checkpoint_completion_target)` segments — kicks early enough that the next checkpoint's writes complete before `max_wal_size` is hit. This is what produces the `checkpoints are occurring too frequently` warning at `checkpointer.c:438-444` (hint: raise `max_wal_size`).

3. **Explicit requests via `RequestCheckpoint(flags)`** — the `CHECKPOINT` SQL command, shutdown, end-of-recovery, base backups, and the XLOG-consumption trigger above. Body at `checkpointer.c:947-994`: standalone-backend fast-path runs `CreateCheckPoint(flags|CHECKPOINT_IMMEDIATE)` inline; otherwise atomically ORs flags into `CheckpointerShmem->ckpt_flags` under spinlock, bumps `ckpt_started`, signals checkpointer. Flag bits at `xlog.h:140-150`: `CHECKPOINT_IS_SHUTDOWN`, `END_OF_RECOVERY`, `IMMEDIATE`, `FORCE`, `FLUSH_ALL`, `WAIT`, `REQUESTED`, plus cause bits `CAUSE_XLOG` / `CAUSE_TIME`.

On wake the checkpointer ORs in any pending `ckpt_flags`, runs `CreateCheckPoint` → `CheckPointGuts` (`xlog.c:7475-7501`): flushes SLRUs, replication slots, snapshot build, logical rewrite heap, replication origin, CLOG/CommitTs/SUBTRANS/MultiXact/Predicate, the buffer pool (`CheckPointBuffers`), queued fsyncs (`ProcessSyncRequests`), then 2PC state — in that order.

**One sentence:** the WAL writer flushes inside `XLogBackgroundFlush` whenever the next page boundary is past `LogwrtResult.Flush` and either `wal_writer_delay` has elapsed, `wal_writer_flush_after` blocks have accumulated, or block-based throttling is off; a checkpoint is triggered by `checkpoint_timeout` elapsing in the checkpointer loop, by a backend hitting `CheckPointSegments` (derived from `max_wal_size`) after a segment switch, or by any caller of `RequestCheckpoint` (shutdown, end-of-recovery, base backup, explicit `CHECKPOINT`).

## Capture notes
- **KEEPER.** Local index, no `repo` arg, mode=hybrid.
- **Retrieval: strong hit.** All load-bearing files surfaced: walwriter.h/.c, xlog.c (XLogBackgroundFlush head + tail + XLogWrite segment-finish trigger + CalculateCheckpointSegments + CheckPointGuts), checkpointer.c (CheckpointerMain + RequestCheckpoint + frequency warning), xlog.h flag bits, guc_tables.c, bgwriter.h, the transam README. The find_related from xlog.c:3060 was the standout (0.916 pulling the rest of XLogBackgroundFlush).
- **Hallucination: clean, one tiny hedge.** All specifics trace to returned chunks — the 200ms/1MB/300s/5min defaults, the `flushblocks` decision branches, `CheckPointSegments = max_wal_size/(1+completion_target)`, the `CHECKPOINT_CAUSE_XLOG` raise at `XLogWrite`, the `RequestCheckpoint` shmem-flag protocol, the "checkpoints too frequently" warning, `CheckPointGuts`' subsystem order. The one minor: the "sync commits flush via `XLogFlush()`" sentence isn't strictly grounded — `XLogFlush` itself wasn't retrieved — but the README async-commit prose makes the implication clear and the language is hedged.
- **Tool use:** 8 searches + 1 find_related, well-targeted refinement WAL-writer → flushblocks → checkpoint triggers → XLogCheckpointNeeded → RequestCheckpoint. Two transient agent attempts to "view" the local file path didn't work; agent correctly pivoted to find_related. (Not a ken issue.)
- **CHUNK BOUNDARIES (A/B variable):** still ~50-line line-windows opening mid-construct (xlog.c:3016-3065 opens at "WriteRqst.Write = XLogCtl->asyncXactLSN;"; xlog.c:2971-3020 opens with comment fragment "* We can guarantee..."). Same as Q1 regex arm — consistent regex-on-C behavior. ⏳ Compare against the treesitter arm (postgres-treesitter-q2) once #12 is fixed.
- **Residual noise:** `*.po` gettext translation files showed up at low ranks (pl.po, tr.po, ko.po — translated versions of the same `checkpoints are occurring too frequently` message). **New candidate exclude:** `*.po`. Low value but low-cost; could add to round-3 if you want maximum tidiness. (I'd still leave it — diminishing returns, and the agent ignored them cleanly.)

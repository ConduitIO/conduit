-- One sample of WAL pressure on the source database, emitted as CSV rows with
-- a leading kind column (psql -tA -F,). Read once a second by sample-wal.sh.
--
-- Timestamps come from the Postgres clock — the SAME clock bench.yml's
-- TS_BEFORE_MS / TS_AFTER_MS markers use — so scripts/summarize.py can
-- restrict these samples to exactly the measured window without comparing two
-- machines' clocks.
--
-- kind=wal:  the current WAL insert position as a byte offset. Consecutive
--            samples give WAL bytes/s, which is the pressure the loader puts
--            on the source database. If both engines of a pair sit at the same
--            WAL rate, the SOURCE's WAL generation (or its logical decoding),
--            not either engine, set the throughput, and no "N% faster" claim
--            is possible in that regime.
-- kind=slot: per replication slot, the walsender pid and how far behind the
--            current WAL position that slot's confirmed flush is. A slot whose
--            lag grows is not keeping up with the WAL.
SELECT 'wal',
       (extract(epoch from clock_timestamp()) * 1000)::bigint,
       (pg_current_wal_lsn() - '0/0'::pg_lsn)::bigint,
       '',
       0;

SELECT 'slot',
       (extract(epoch from clock_timestamp()) * 1000)::bigint,
       COALESCE((pg_current_wal_lsn() - confirmed_flush_lsn)::bigint, 0),
       slot_name,
       COALESCE(active_pid, 0)
FROM pg_replication_slots
ORDER BY slot_name;

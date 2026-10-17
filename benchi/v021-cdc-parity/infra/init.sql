-- Source table: the single table every CDC arm captures. The loader writes a
-- fixed-rate mix of INSERT/UPDATE/DELETE into it. Row shape is fixed
-- (id bigint PRIMARY KEY, payload text) and identical across all arms.
--
-- REPLICA IDENTITY stays DEFAULT (the primary key), so pgoutput emits the key
-- for deletes and all three systems can reconstruct the changed row.
CREATE TABLE bench_source (
    id      bigint PRIMARY KEY,
    payload text   NOT NULL
);

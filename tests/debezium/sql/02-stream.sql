-- Golden workload, part 2: changes made after the snapshot completed.
--
-- Each statement is its own transaction unless wrapped in BEGIN/COMMIT, so
-- the order of events per table is the order of the statements here.

-- Plain insert, update, delete (delete + tombstone).
INSERT INTO golden.customers VALUES
    (10, 'Edsger', 'ew@example.com', 99999999.99, true, '2000-01-01 00:00:00.000001', '2000-01-01 00:00:00.000001+00');
UPDATE golden.customers SET score = 1.10, email = NULL WHERE id = 10;
DELETE FROM golden.customers WHERE id = 3;

-- Primary-key change: delete (+ tombstone) for the old key, create for the new.
UPDATE golden.customers SET id = 11 WHERE id = 10;

-- Multi-statement transaction across tables, with a multi-row insert.
BEGIN;
INSERT INTO golden.customers VALUES
    (20, 'Barbara', 'bl@example.com', 0.00, true, '2024-01-01 00:00:00', '2024-01-01 00:00:00+00'),
    (21, 'Ken',     NULL,             NULL, true, NULL,                  NULL);
UPDATE golden.full_ident SET val = 'uno', qty = 10 WHERE id = 1;
DELETE FROM golden.full_ident WHERE id = 2;
COMMIT;

-- Types: insert edge values, update a few columns, then delete.
INSERT INTO golden.types VALUES
    (4, 1, 2, 'NaN', 'Infinity', 0.0001, 123456789012345678901234567890.123456789, -0.01,
     '1970-01-01', '23:59:59.999999', '00:00:01', '12:00:00.5', '23:59:59.999999-12',
     '2262-04-11 23:47:16.854775', '1677-09-21 00:12:43.146', '2262-04-11 23:47:16.854775+00',
     '-178000000 years', 'ffffffff-ffff-ffff-ffff-ffffffffffff',
     '{"nested": {"deep": [true, false, null]}, "unicode": "é中"}', '{"z": 1, "a": 2}', '\x', '{-1,NULL,2147483647}', '{"","Unicode é中",NULL}',
     '{NULL,-1.000}', 'ok', '"a"=>"b"', '10.0.0.0/8', 'x', 'trailing  ', '<r><c a="1"/></r>', NULL, 1);
-- Number formatting boundaries: JSON float text (Java switches to E notation
-- below 1e-3 and at 1e7), negative-scale results of stripping trailing zeros
-- from unconstrained numerics, and a constrained numeric below its scale.
INSERT INTO golden.types (id, c_float4, c_float8, c_num, c_num_unc, c_num_arr, c_int8) VALUES
    (5, 10000000, 0.001, 5, 100, '{100,0.50,0.00001}', -9223372036854775808),
    (6, 0.0001, 9999999.999, -12345678.9, 1000.50, '{-0.0}', 0),
    (7, 3.4028235e38, 1e7, 0, -0.000, NULL, NULL),
    (8, 1.17549435e-38, 4.9e-324, 0.5, 1e20, NULL, NULL);
UPDATE golden.types SET c_num = 99999999.9999, c_num_unc = 1.000, c_tstz = '2024-06-30 23:59:60+00', c_jsonb = '{"changed": true}' WHERE id = 1;
UPDATE golden.types SET c_text_arr = NULL, c_int_arr = '{}' WHERE id = 2;
DELETE FROM golden.types WHERE id = 4;

-- Unchanged TOAST: only counter changes, every big column is unchanged.
UPDATE golden.toasty SET counter = counter + 1 WHERE id = 1;
-- And one update that rewrites a TOASTed column.
UPDATE golden.toasty SET big_text = 'small now', counter = counter + 1 WHERE id = 1;

-- Composite key: non-key update, key-changing update, delete.
UPDATE golden.composite SET note = 'first-updated' WHERE region = 'eu' AND seq = 1;
UPDATE golden.composite SET seq = 2 WHERE region = 'us' AND seq = 1;
DELETE FROM golden.composite WHERE region = 'eu' AND seq = 1;

-- Primary-key changes on numeric and uuid keys.
UPDATE golden.numkey SET id = -0.25 WHERE id = 1.50;
UPDATE golden.uuidkey SET id = 'ffffffff-ffff-ffff-ffff-ffffffffffff' WHERE val = 'u';

-- REPLICA IDENTITY DEFAULT deletes: before carries the key only, while the
-- schema declares the other columns NOT NULL (D15).
UPDATE golden.default_ident SET note = 'n1b' WHERE id = 1;
DELETE FROM golden.default_ident WHERE id = 2;

-- String escaping: control characters, quotes, backslash, a slash, U+2028 and
-- characters outside the BMP, as the JSON converter writes them.
INSERT INTO golden.customers (id, name, email) VALUES
    (30, E'tab\there\nnl\x01\x1f "q" \\ / <&>   \U0001F600 \x7f', E'\b\f\r');

-- Explicit NULLs in columns that have defaults, and a row that uses them.
INSERT INTO golden.defaults (id, d_text, d_int, d_num, d_bool, d_now) VALUES (1, NULL, NULL, NULL, NULL, NULL);
INSERT INTO golden.defaults (id, d_now) VALUES (2, '2024-01-01 00:00:00+00');

-- Keyless table insert: null record key.
INSERT INTO golden.nopk VALUES ('stream', 2), (NULL, NULL);

-- Logical decoding messages, transactional and not.
SELECT pg_logical_emit_message(true, 'golden', 'transactional payload');
SELECT pg_logical_emit_message(false, 'golden', convert_to('non-transactional', 'UTF8'));

-- Truncate is skipped by default (skipped.operations=t); present so the
-- goldens show it produces nothing.
TRUNCATE golden.nopk;

INSERT INTO golden.sentinel VALUES (1);

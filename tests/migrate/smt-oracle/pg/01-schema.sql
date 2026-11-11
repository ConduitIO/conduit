-- SMT oracle fixture database, part 1: schema and the rows the snapshot reads.
-- Every value is a literal so two captures differ only in the fields
-- java/Consume.java normalizes.


-- The main fixture: a nullable column WITH a default (note, qty), a nullable
-- column without one, a TOASTed text column, jsonb and array columns, a column
-- whose name contains a dot, and a column named with the Kelvin sign (U+212A).
CREATE TABLE items (
    id        integer PRIMARY KEY,
    name      text NOT NULL,
    note      text DEFAULT 'none',
    qty       integer DEFAULT 5,
    price     numeric(10, 2),
    big       text,
    attrs     jsonb,
    a         text,
    b         text,
    created   timestamptz
);
-- Out-of-line storage without compression (the values are 2.5 kB, over the 2 kB
-- TOAST threshold), so the value stays TOASTed and an
-- update that does not touch it carries Debezium's unavailable-value marker.
ALTER TABLE items ALTER COLUMN big SET STORAGE EXTERNAL;
ALTER TABLE items REPLICA IDENTITY FULL;

-- Columns Conduit's Postgres source (v0.20) cannot build an Avro schema for
-- live in their own table, which the oracle reads with the connector's schema
-- attachment off: arrays (cannot resolve field type "_text"), a dotted column
-- name ("invalid name a.b"), and a column named with the Kelvin sign (U+212A).
CREATE TABLE noschema (id integer PRIMARY KEY, tags text[], nums integer[], "a.b" text, "temp_K" text);
INSERT INTO noschema VALUES
  (1, ARRAY['x','y'], ARRAY[1,2,3], 'dot', 'kelvin'),
  (2, ARRAY[]::text[], ARRAY[]::integer[], '', ''),
  (3, NULL, NULL, NULL, NULL);

-- Tables named for the alternation and lazy-quantifier routing vectors.
CREATE TABLE a  (id integer PRIMARY KEY, v text);
CREATE TABLE ab (id integer PRIMARY KEY, v text);

-- Default replica identity, so an update that does not touch the TOASTed
-- column carries the unavailable-value marker instead of the old value.
CREATE TABLE toast (id integer PRIMARY KEY, label text, big text);
ALTER TABLE toast ALTER COLUMN big SET STORAGE EXTERNAL;

-- No nullable columns: the only table Conduit's Postgres source can read with
-- its default Avro schema attachment (a NULL in a nullable column halts it, see
-- the README), so it is the one table whose records carry registry schemas.
CREATE TABLE strict (
    id      integer PRIMARY KEY,
    name    text NOT NULL,
    qty     integer NOT NULL DEFAULT 5,
    price   numeric(10, 2) NOT NULL,
    created timestamptz NOT NULL,
    attrs   jsonb NOT NULL,
    big     text NOT NULL,
    flag    boolean NOT NULL
);
ALTER TABLE strict ALTER COLUMN big SET STORAGE EXTERNAL;

CREATE PUBLICATION dbz_smt FOR ALL TABLES;

INSERT INTO items (id, name, note, qty, price, big, attrs, a, b, created) VALUES
  (1, 'widget', 'n1', 7, 12.50, repeat(md5('toast-1'), 80), '{"k": [1, 2, {"z": null}], "s": "x"}', 'A1', 'B1', '2023-11-14 22:13:20+00'),
  (2, 'gadget', NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL),
  (3, 'gizmo', DEFAULT, DEFAULT, 0.10, repeat(md5('toast-3'), 80), '[]', 'A3', 'B3', '2023-11-14 22:13:20.123456+00');
INSERT INTO strict VALUES
  (1, 'one', 3, 9.99, '2023-11-14 22:13:20+00', '{"a": [1, {"b": null}]}', repeat(md5('strict-1'), 80), true),
  (2, 'two', DEFAULT, 0.01, '2023-11-14 22:13:20.123456+00', '[]', 'small', false);
INSERT INTO toast VALUES (1, 'one', repeat(md5('toast-t1'), 80));
INSERT INTO a  VALUES (1, 'one');
INSERT INTO ab VALUES (1, 'one');

-- Golden workload, part 1: schema and the rows the initial snapshot reads.
--
-- Every value is a literal, so two captures of the same Debezium version
-- differ only in the volatile fields the design lists (D1, D2). Literals are
-- written in ISO form because the server runs with DateStyle 'SQL, DMY'.

CREATE EXTENSION IF NOT EXISTS hstore;

CREATE SCHEMA golden;

CREATE TYPE golden.mood AS ENUM ('sad', 'ok', 'happy');

-- Single-column primary key, the common case.
CREATE TABLE golden.customers (
    id         integer PRIMARY KEY,
    name       text NOT NULL,
    email      varchar(100),
    score      numeric(10, 2),
    active     boolean NOT NULL DEFAULT true,
    created_at timestamp,
    updated_at timestamptz
);

-- One column per type in the design's type mapping that the stock postgres
-- image supports.
CREATE TABLE golden.types (
    id           integer PRIMARY KEY,
    c_int2       smallint,
    c_int8       bigint,
    c_float4     real,
    c_float8     double precision,
    c_num        numeric(12, 4),
    c_num_unc    numeric,
    c_money      money,
    c_date       date,
    c_time       time,
    c_time0      time(0),
    c_time3      time(3),
    c_timetz     timetz,
    c_ts         timestamp,
    c_ts3        timestamp(3),
    c_tstz       timestamptz,
    c_interval   interval,
    c_uuid       uuid,
    c_json       json,
    c_jsonb      jsonb,
    c_bytea      bytea,
    c_int_arr    integer[],
    c_text_arr   text[],
    c_num_arr    numeric[],
    c_enum       golden.mood,
    c_hstore     hstore,
    c_inet       inet,
    c_bpchar     char(5),
    c_varchar    varchar(20),
    c_xml        xml,
    c_bool       boolean,
    c_oid        oid
);

-- Large values stored out of line and uncompressed, so an update that does not
-- touch them sends Debezium the "unchanged TOAST" marker for each.
CREATE TABLE golden.toasty (
    id       integer PRIMARY KEY,
    big_text text,
    big_json jsonb,
    big_arr  text[],
    big_blob bytea,
    counter  integer NOT NULL
);
ALTER TABLE golden.toasty ALTER COLUMN big_text SET STORAGE EXTERNAL;
ALTER TABLE golden.toasty ALTER COLUMN big_json SET STORAGE EXTERNAL;
ALTER TABLE golden.toasty ALTER COLUMN big_arr  SET STORAGE EXTERNAL;
ALTER TABLE golden.toasty ALTER COLUMN big_blob SET STORAGE EXTERNAL;

-- Composite primary key, declared in an order different from the column order,
-- and a key-changing update in the stream.
CREATE TABLE golden.composite (
    region text    NOT NULL,
    seq    integer NOT NULL,
    note   text,
    PRIMARY KEY (seq, region)
);

-- REPLICA IDENTITY FULL: updates and deletes carry the whole old row.
CREATE TABLE golden.full_ident (
    id  integer PRIMARY KEY,
    val text,
    qty integer NOT NULL
);
ALTER TABLE golden.full_ident REPLICA IDENTITY FULL;

-- Deletes under REPLICA IDENTITY DEFAULT with NOT NULL non-key columns (D15).
CREATE TABLE golden.default_ident (
    id    integer PRIMARY KEY,
    label text    NOT NULL,
    qty   integer NOT NULL DEFAULT 7,
    note  text
);

-- No primary key: null record key. Insert only (DEFAULT identity without a key
-- rejects updates and deletes on a published table).
CREATE TABLE golden.nopk (
    val text,
    n   integer
);

-- Primary keys of numeric, uuid and timestamp type, so the primary-key change
-- headers (__debezium.newkey/oldkey) show how the header converter writes
-- Decimal, string-backed logical and int64-backed logical key fields.
CREATE TABLE golden.numkey (
    id  numeric(10, 2) PRIMARY KEY,
    val text
);
CREATE TABLE golden.uuidkey (
    id  uuid PRIMARY KEY,
    at  timestamp NOT NULL,
    val text,
    UNIQUE (at)
);

-- Nullable columns with literal defaults: the JsonConverter's
-- replace.null.with.default decides whether a NULL is written as null or as
-- the schema default.
CREATE TABLE golden.defaults (
    id     integer PRIMARY KEY,
    d_text text    DEFAULT 'dflt',
    d_int  integer DEFAULT 42,
    d_num  numeric(5, 2) DEFAULT 1.50,
    d_bool boolean DEFAULT false,
    d_now  timestamptz DEFAULT now()
);

-- Written last by the stream script; the capture stops after seeing it.
CREATE TABLE golden.sentinel (
    id integer PRIMARY KEY
);

INSERT INTO golden.customers VALUES
    (1, 'Ada',   'ada@example.com',   10.50, true,  '2024-03-15 10:11:12.345678', '2024-03-15 10:11:12.5+02'),
    (2, 'Grace', NULL,                -0.01, false, '1999-12-31 23:59:59',        '1970-01-01 00:00:00+00'),
    (3, 'Linus', 'linus@example.com', NULL,  true,  NULL,                         NULL);

INSERT INTO golden.types VALUES
    (1, 32767, 9223372036854775807, 1.5, 3.141592653589793, 12345678.1234, 1.10, 1234.56,
     '2024-02-29', '13:14:15.123456', '13:14:15', '13:14:15.123', '13:14:15.5+05:30',
     '2024-02-29 13:14:15.123456', '2024-02-29 13:14:15.123', '2024-02-29 13:14:15.123456-08',
     '1 year 2 mons 3 days 04:05:06.789', 'a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11',
     '{"b": 1, "a": [1, 2]}', '{"b": 1, "a": [1, 2]}', '\x00ff10', '{1,2,3}', '{"a","b c",NULL}',
     '{1.5,2.25}', 'happy', '"k1"=>"v1", "k2"=>NULL', '192.168.0.1/24', 'ab', 'vc', '<a>1</a>', true, 4294967295),
    (2, -32768, -1, -0.0, 1e-300, -0.0001, 0, -5.00,
     '0001-01-01', '00:00:00', '23:59:59', '00:00:00.001', '00:00:00+00',
     '1970-01-01 00:00:00', '1970-01-01 00:00:00.999', '1970-01-01 00:00:00+00',
     '-1 days -00:00:01', '00000000-0000-0000-0000-000000000000',
     '[]', '{}', '', '{}', '{}',
     '{}', 'sad', '', '::1', 'abcde', '', '<x/>', false, 0),
    (3, NULL, NULL, NULL, NULL, NULL, NULL, NULL,
     NULL, NULL, NULL, NULL, NULL,
     NULL, NULL, NULL,
     NULL, NULL,
     NULL, NULL, NULL, NULL, NULL,
     NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL);

INSERT INTO golden.toasty
SELECT 1,
       (SELECT string_agg(md5(i::text), '') FROM generate_series(1, 200) i),
       (SELECT jsonb_build_object('blob', string_agg(md5(i::text), '')) FROM generate_series(201, 400) i),
       (SELECT array_agg(md5(i::text)) FROM generate_series(401, 600) i),
       (SELECT decode(string_agg(md5(i::text), ''), 'hex') FROM generate_series(601, 900) i),
       0;

INSERT INTO golden.composite VALUES ('eu', 1, 'first'), ('us', 1, 'second');

INSERT INTO golden.full_ident VALUES (1, 'one', 1), (2, 'two', 2);

INSERT INTO golden.default_ident VALUES (1, 'keep', 1, 'n1'), (2, 'drop-me', 2, NULL);

INSERT INTO golden.nopk VALUES ('snap', 1);

INSERT INTO golden.numkey VALUES (1.50, 'n');

INSERT INTO golden.uuidkey VALUES ('a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11', '2024-01-01 00:00:00.5', 'u');

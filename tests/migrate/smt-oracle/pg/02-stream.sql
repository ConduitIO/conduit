-- Part 2: changes made while the connector streams.
INSERT INTO items (id, name, a, b) VALUES (4, 'inserted-defaults', 'A4', 'B4');          -- defaults apply
INSERT INTO items (id, name, note, qty, a, b) VALUES (5, 'inserted-explicit-null', NULL, NULL, 'A5', 'B5');
UPDATE items SET name = 'widget-2' WHERE id = 1;                                        -- big stays TOASTed
UPDATE items SET big = repeat(md5('toast-1b'), 80) WHERE id = 3;                       -- big rewritten
UPDATE items SET note = NULL, qty = NULL WHERE id = 1;                                  -- defaulted column set to null
UPDATE items SET attrs = '{"k": []}'  WHERE id = 3;
UPDATE noschema SET "a.b" = 'dot2', tags = ARRAY['z'], nums = ARRAY[9] WHERE id = 1;
DELETE FROM items WHERE id = 2;                                                         -- delete event, then tombstone
DELETE FROM items WHERE id = 5;
INSERT INTO a  VALUES (2, 'two');
INSERT INTO ab VALUES (2, 'two');
DELETE FROM ab WHERE id = 1;
UPDATE toast SET label = 'one-b' WHERE id = 1;                                     -- big is TOASTed and unchanged
INSERT INTO strict VALUES (3, 'three', 1, 1.50, '2024-01-01 00:00:00+00', '{}', 'x', true);
-- big is rewritten: with the Avro schema attached, Conduit fails closed on an update that
-- leaves a TOASTed column unchanged (postgres.avro.unchanged_toast; README, findings).
UPDATE strict SET name = 'one-b', big = repeat(md5('strict-1b'), 80) WHERE id = 1;
DELETE FROM strict WHERE id = 2;

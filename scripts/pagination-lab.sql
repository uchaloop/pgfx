\set ON_ERROR_STOP on
CREATE ROLE pagination_lab LOGIN PASSWORD 'pagination_lab';
CREATE DATABASE pagination_lab OWNER pagination_lab;
\connect pagination_lab
SET ROLE pagination_lab;
CREATE TABLE bench_rows (id bigint PRIMARY KEY, score bigint NOT NULL, label text NOT NULL, meta jsonb NOT NULL);
INSERT INTO bench_rows
SELECT i, i % 100000, 'row-' || i, jsonb_build_object('group', i % 100, 'payload', repeat('x', 64))
FROM generate_series(1, 2000000) AS i;
VACUUM (ANALYZE) bench_rows;
SELECT count(*), pg_size_pretty(pg_total_relation_size('bench_rows')) FROM bench_rows;

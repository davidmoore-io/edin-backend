#!/usr/bin/env bash
set -euo pipefail

docker exec -i edin-timescaledb psql \
  -v ON_ERROR_STOP=1 \
  -U edin_admin \
  -d edin <<'SQL'
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'edin_cmd_writer') THEN
        CREATE ROLE edin_cmd_writer LOGIN NOSUPERUSER NOBYPASSRLS;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'edin_cmd_reader') THEN
        CREATE ROLE edin_cmd_reader LOGIN NOSUPERUSER NOBYPASSRLS;
    END IF;
END
$$;

ALTER ROLE edin_cmd_writer
    WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS
    PASSWORD 'edin-local-cmd-writer';
ALTER ROLE edin_cmd_reader
    WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS
    PASSWORD 'edin-local-cmd-reader';

GRANT CONNECT ON DATABASE edin TO edin_cmd_writer, edin_cmd_reader;
SQL


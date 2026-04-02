-- =============================================================
-- PostgreSQL initialization script.
-- Runs automatically when the Docker container is first created
-- (mapped via docker-entrypoint-initdb.d in docker-compose.dev.yml).
--
-- DO NOT include schema changes here — use numbered migration files.
-- This file is for extensions and database-level settings only.
-- =============================================================

-- pg_stat_statements: tracks execution statistics for all SQL statements.
-- Required for query performance analysis in pgAdmin and Grafana dashboards.
-- The shared_preload_libraries=pg_stat_statements is set in docker-compose.
CREATE EXTENSION IF NOT EXISTS pg_stat_statements;

-- pg_trgm: enables trigram-based similarity search and GIN indexes.
-- Used in Phase 3 for analytics text search on original_url.
CREATE EXTENSION IF NOT EXISTS pg_trgm;
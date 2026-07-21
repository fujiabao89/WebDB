-- +goose Up
-- 为已有 executions 表添加 statement_hash 非空白 CHECK 约束
-- NOT VALID 避免锁表扫描已有行
ALTER TABLE executions ADD CONSTRAINT chk_executions_statement_hash_nonblank
    CHECK (btrim(statement_hash) <> '') NOT VALID;

-- +goose Down
ALTER TABLE executions DROP CONSTRAINT IF EXISTS chk_executions_statement_hash_nonblank;

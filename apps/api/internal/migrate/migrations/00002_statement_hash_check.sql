-- +goose Up
-- 修正已有空白 statement_hash 后再添加 CHECK 约束
UPDATE executions SET statement_hash = 'legacy-migration'
    WHERE btrim(statement_hash) = '';
ALTER TABLE executions ADD CONSTRAINT chk_executions_statement_hash_nonblank
    CHECK (btrim(statement_hash) <> '');

-- +goose Down
ALTER TABLE executions DROP CONSTRAINT IF EXISTS chk_executions_statement_hash_nonblank;

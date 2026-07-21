-- +goose Up
-- 修正已有空白 statement_hash（含 tab/newline）后再添加 CHECK 约束
UPDATE executions SET statement_hash = 'legacy-migration'
    WHERE statement_hash !~ '\S';
ALTER TABLE executions ADD CONSTRAINT chk_executions_statement_hash_nonblank
    CHECK (statement_hash !~ '^\s*$');

-- +goose Down
ALTER TABLE executions DROP CONSTRAINT IF EXISTS chk_executions_statement_hash_nonblank;

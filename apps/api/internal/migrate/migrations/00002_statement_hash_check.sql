-- +goose Up
ALTER TABLE executions ADD CONSTRAINT chk_executions_statement_hash_nonblank
    CHECK (btrim(statement_hash) <> '');

-- +goose Down
ALTER TABLE executions DROP CONSTRAINT IF EXISTS chk_executions_statement_hash_nonblank;

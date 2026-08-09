-- +goose Up
-- 支持 connections 按 workspace_id 过滤 + created_at DESC 排序的查询路径
-- （ListConnections / ListConnectionsAllowed：WHERE workspace_id=? ORDER BY created_at DESC）。
-- uq_connections_ws_id (workspace_id, id) 已覆盖 workspace_id 过滤，但不覆盖
-- created_at 排序；该复合索引使过滤与排序可走同一条索引，避免大工作区下的额外排序。
CREATE INDEX IF NOT EXISTS idx_connections_ws_created_at
    ON connections (workspace_id, created_at DESC);

-- +goose Down
DROP INDEX IF EXISTS idx_connections_ws_created_at;

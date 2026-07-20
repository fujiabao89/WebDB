-- +goose Up

-- ============================================================
-- WebDB P0 元数据 Schema
-- ADR-013: P0 元数据库迁移与 Schema 基线
-- 8 张表：users, workspaces, workspace_members, credential_envelopes,
--          connections, connection_policies, executions, audit_events
-- ============================================================

-- 0. 扩展
CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- ============================================================
-- 1. users — 本地用户
-- ADR-013: email 非空、无首尾空白、大小写不敏感唯一；password_hash 非空非空白
-- ============================================================
CREATE TABLE users (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email           TEXT NOT NULL
                    CHECK (email !~ '^\s' AND email !~ '\s$' AND email <> ''),
    password_hash   TEXT NOT NULL
                    CHECK (btrim(password_hash) <> ''),
    status          TEXT NOT NULL DEFAULT 'active'
                    CHECK (status IN ('active', 'disabled')),
    identity_provider TEXT,
    external_subject  TEXT,
    external_tenant   TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX uq_users_lower_email ON users (lower(email));

-- ============================================================
-- 2. workspaces — 团队租户
-- ============================================================
CREATE TABLE workspaces (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            TEXT NOT NULL,
    settings        JSONB NOT NULL DEFAULT '{}'::jsonb
                    CHECK (jsonb_typeof(settings) = 'object'),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ============================================================
-- 3. workspace_members — 成员关联
-- ADR-013: 非空 workspace_id + user_id，复合主键，直接引用真实 workspace/user
--          分别外键引用且 ON DELETE RESTRICT，角色限制 4 种
-- ============================================================
CREATE TABLE workspace_members (
    workspace_id    UUID NOT NULL
                    REFERENCES workspaces(id) ON DELETE RESTRICT,
    user_id         UUID NOT NULL
                    REFERENCES users(id) ON DELETE RESTRICT,
    role            TEXT NOT NULL
                    CHECK (role IN ('owner', 'admin', 'editor', 'viewer')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, user_id)
);

-- ============================================================
-- 4. credential_envelopes — 加密凭证信封
-- ADR-013: 直接引用真实 workspace；三元组唯一引用边界
--          所有二进制 payload 非空且非零长度；suite 非空白；版本正整数
-- ============================================================
CREATE TABLE credential_envelopes (
    workspace_id    UUID NOT NULL
                    REFERENCES workspaces(id) ON DELETE RESTRICT,
    secret_ref      UUID NOT NULL,
    version         INTEGER NOT NULL CHECK (version > 0),
    ciphertext      BYTEA NOT NULL
                    CHECK (octet_length(ciphertext) > 0),
    data_nonce      BYTEA NOT NULL
                    CHECK (octet_length(data_nonce) > 0),
    wrapped_dek     BYTEA NOT NULL
                    CHECK (octet_length(wrapped_dek) > 0),
    wrap_nonce      BYTEA NOT NULL
                    CHECK (octet_length(wrap_nonce) > 0),
    envelope_suite  TEXT NOT NULL
                    CHECK (btrim(envelope_suite) <> ''),
    kek_version     INTEGER NOT NULL CHECK (kek_version > 0),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    retired_at      TIMESTAMPTZ,
    CONSTRAINT uq_credential_envelopes_ref
        UNIQUE (workspace_id, secret_ref, version)
);

-- ============================================================
-- 5. connections — 目标数据库连接
-- ADR-013: UNIQUE(workspace_id, id) 作为复合外键目标
--          环境由调用方显式提供；只保存 secret_ref+version 不保存明文
--          created_by 必须是对应工作区成员
--          端口限制 1-65535
-- ============================================================
CREATE TABLE connections (
    id              UUID DEFAULT gen_random_uuid(),
    workspace_id    UUID NOT NULL
                    REFERENCES workspaces(id) ON DELETE RESTRICT,
    name            TEXT NOT NULL,
    engine          TEXT NOT NULL
                    CHECK (engine IN ('postgresql', 'mysql')),
    host            TEXT NOT NULL,
    port            INTEGER NOT NULL
                    CHECK (port > 0 AND port <= 65535),
    database        TEXT NOT NULL,
    environment     TEXT NOT NULL
                    CHECK (environment IN ('development', 'staging', 'production')),
    secret_ref      UUID NOT NULL,
    secret_version  INTEGER NOT NULL CHECK (secret_version > 0),
    created_by      UUID NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id),
    CONSTRAINT uq_connections_ws_id UNIQUE (workspace_id, id),
    CONSTRAINT uq_connections_ws_name UNIQUE (workspace_id, name),
    CONSTRAINT fk_connections_envelope
        FOREIGN KEY (workspace_id, secret_ref, secret_version)
        REFERENCES credential_envelopes (workspace_id, secret_ref, version)
        ON DELETE RESTRICT,
    CONSTRAINT fk_connections_created_by
        FOREIGN KEY (workspace_id, created_by)
        REFERENCES workspace_members (workspace_id, user_id)
        ON DELETE RESTRICT
);

-- ============================================================
-- 6. connection_policies — 连接策略（每连接至多一条）
-- ADR-013: 复合主键确保唯一；缺失策略时访问层默认拒绝全部操作
--          statement_timeout_ms 和 max_rows 为正整数
-- ============================================================
CREATE TABLE connection_policies (
    workspace_id        UUID NOT NULL,
    connection_id       UUID NOT NULL,
    allow_read          BOOLEAN NOT NULL DEFAULT true,
    allow_write         BOOLEAN NOT NULL DEFAULT false,
    allow_export        BOOLEAN NOT NULL DEFAULT false,
    statement_timeout_ms INTEGER NOT NULL CHECK (statement_timeout_ms > 0),
    max_rows            INTEGER NOT NULL CHECK (max_rows > 0),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, connection_id),
    CONSTRAINT fk_policies_connection
        FOREIGN KEY (workspace_id, connection_id)
        REFERENCES connections (workspace_id, id)
        ON DELETE RESTRICT
);

-- ============================================================
-- 7. executions — SQL 执行记录
-- ADR-013: 非空 workspace/connection/actor；actor 必须是工作区成员
--          非空 result_ref 必须有过期时间；trace_id 非空非空白
-- ============================================================
CREATE TABLE executions (
    id                  UUID DEFAULT gen_random_uuid(),
    workspace_id        UUID NOT NULL,
    connection_id       UUID NOT NULL,
    actor_id            UUID NOT NULL,
    document_id         UUID,
    query_version_id    UUID,
    statement_hash      TEXT NOT NULL,
    status              TEXT NOT NULL DEFAULT 'pending'
                        CHECK (status IN ('pending', 'running', 'completed', 'failed', 'cancelled')),
    trace_id            TEXT NOT NULL
                        CHECK (btrim(trace_id) <> ''),
    started_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at         TIMESTAMPTZ,
    duration_ms         INTEGER,
    row_count           INTEGER,
    result_ref          TEXT,
    result_expires_at   TIMESTAMPTZ,
    error_code          TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id),
    CONSTRAINT uq_executions_ws_conn_id UNIQUE (workspace_id, connection_id, id),
    CONSTRAINT chk_executions_result_expiry
        CHECK (result_ref IS NULL OR result_expires_at IS NOT NULL),
    CONSTRAINT fk_executions_connection
        FOREIGN KEY (workspace_id, connection_id)
        REFERENCES connections (workspace_id, id)
        ON DELETE RESTRICT,
    CONSTRAINT fk_executions_actor
        FOREIGN KEY (workspace_id, actor_id)
        REFERENCES workspace_members (workspace_id, user_id)
        ON DELETE RESTRICT
);

-- ============================================================
-- 8. audit_events — 追加式审计（不可修改/删除）
-- ADR-013: 直接引用真实 workspace
--          actor_type 判别 user/system 空值语义一致
--          action/resource_type/resource_id/trace_id 非空非空白
--          occurred_at 无默认值必须显式提供
--          metadata 只能是 JSON object
--          execution_id 可空但非空时 connection_id 必须非空
-- ============================================================
CREATE TABLE audit_events (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    UUID NOT NULL
                    REFERENCES workspaces(id) ON DELETE RESTRICT,
    actor_type      TEXT NOT NULL
                    CHECK (actor_type IN ('user', 'system')),
    actor_id        UUID,
    connection_id   UUID,
    action          TEXT NOT NULL
                    CHECK (btrim(action) <> ''),
    resource_type   TEXT NOT NULL
                    CHECK (btrim(resource_type) <> ''),
    resource_id     TEXT NOT NULL
                    CHECK (btrim(resource_id) <> ''),
    outcome         TEXT NOT NULL
                    CHECK (outcome IN ('succeeded', 'failed', 'denied', 'cancelled')),
    metadata        JSONB NOT NULL DEFAULT '{}'::jsonb
                    CHECK (jsonb_typeof(metadata) = 'object'),
    trace_id        TEXT NOT NULL
                    CHECK (btrim(trace_id) <> ''),
    execution_id    UUID,
    occurred_at     TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- actor 判别器：user 必须有 actor_id，system 必须无 actor_id
    CONSTRAINT chk_audit_actor
        CHECK ((actor_type = 'user' AND actor_id IS NOT NULL)
            OR (actor_type = 'system' AND actor_id IS NULL)),
    -- execution 非空时 connection 必须非空
    CONSTRAINT chk_audit_exec_conn
        CHECK (execution_id IS NULL OR connection_id IS NOT NULL),
    -- connection 复合外键（MATCH SIMPLE 允许空值）
    CONSTRAINT fk_audit_connection
        FOREIGN KEY (workspace_id, connection_id)
        REFERENCES connections (workspace_id, id)
        ON DELETE RESTRICT,
    -- actor 必须是工作区成员（仅 user 类型时生效）
    CONSTRAINT fk_audit_actor
        FOREIGN KEY (workspace_id, actor_id)
        REFERENCES workspace_members (workspace_id, user_id)
        ON DELETE RESTRICT,
    -- execution + connection 三列复合外键保证同一连接
    CONSTRAINT fk_audit_execution
        FOREIGN KEY (workspace_id, connection_id, execution_id)
        REFERENCES executions (workspace_id, connection_id, id)
        ON DELETE RESTRICT
);

-- 审计检索索引
CREATE INDEX idx_audit_ws_conn_time
    ON audit_events (workspace_id, connection_id, occurred_at);
CREATE INDEX idx_audit_ws_time
    ON audit_events (workspace_id, occurred_at);
CREATE INDEX idx_audit_resource
    ON audit_events (workspace_id, resource_type, resource_id);
CREATE INDEX idx_audit_trace
    ON audit_events (trace_id);

-- 执行 trace 查询索引
CREATE INDEX idx_executions_ws_trace
    ON executions (workspace_id, trace_id);

-- ============================================================
-- 审计不可变触发器 — 拒绝 UPDATE、DELETE、TRUNCATE
-- ADR-013: 数据库层安装拒绝触发器
-- ============================================================
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION deny_audit_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        RAISE EXCEPTION 'audit_events 不可修改 (UPDATE 被拒绝)';
    ELSIF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'audit_events 不可删除 (DELETE 被拒绝)';
    ELSIF TG_OP = 'TRUNCATE' THEN
        RAISE EXCEPTION 'audit_events 不可清空 (TRUNCATE 被拒绝)';
    END IF;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER trg_audit_no_update
    BEFORE UPDATE ON audit_events
    FOR EACH STATEMENT
    EXECUTE FUNCTION deny_audit_mutation();

CREATE TRIGGER trg_audit_no_delete
    BEFORE DELETE ON audit_events
    FOR EACH STATEMENT
    EXECUTE FUNCTION deny_audit_mutation();

CREATE TRIGGER trg_audit_no_truncate
    BEFORE TRUNCATE ON audit_events
    FOR EACH STATEMENT
    EXECUTE FUNCTION deny_audit_mutation();

-- +goose Down

-- 按依赖逆序删除
DROP TRIGGER IF EXISTS trg_audit_no_truncate ON audit_events;
DROP TRIGGER IF EXISTS trg_audit_no_delete ON audit_events;
DROP TRIGGER IF EXISTS trg_audit_no_update ON audit_events;
DROP FUNCTION IF EXISTS deny_audit_mutation();

DROP TABLE IF EXISTS audit_events;
DROP TABLE IF EXISTS executions;
DROP TABLE IF EXISTS connection_policies;
DROP TABLE IF EXISTS connections;
DROP TABLE IF EXISTS credential_envelopes;
DROP TABLE IF EXISTS workspace_members;
DROP TABLE IF EXISTS workspaces;
DROP TABLE IF EXISTS users;

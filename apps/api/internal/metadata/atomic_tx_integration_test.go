//go:build integration
// +build integration

package metadata

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// 本文件覆盖 WEB-25 设计 §4 验收矩阵的原子事务闸门：mutation + AuditEvent
// 在同一事务原子提交；缺失/不匹配审计事件时 Commit 拒绝并回滚。

func newCredOp(t *testing.T, wsID, actorID, secretRef uuid.UUID, traceID string) *OperationContext {
	t.Helper()
	op, err := NewOperationContext(
		uuid.NewString(), wsID, "credential", secretRef.String(),
		ActionCredentialCreate, nil, actorID, "user", string(OutcomeSucceeded), traceID,
	)
	if err != nil {
		t.Fatalf("NewOperationContext: %v", err)
	}
	return op
}

func newCredEvent(t *testing.T, wsID, actorID, secretRef uuid.UUID, version int, traceID string) *AuditEvent {
	t.Helper()
	raw, err := (AuditMetadata{
		SecretRef:     strPtr(secretRef.String()),
		SecretVersion: intPtr(version),
		EnvelopeSuite: strPtr("AES256GCM-v1"),
		KEKVersion:    intPtr(1),
	}).Marshal()
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	return &AuditEvent{
		WorkspaceID:  wsID,
		ActorType:    ActorTypeUser,
		ActorID:      &actorID,
		Action:       ActionCredentialCreate,
		ResourceType: "credential",
		ResourceID:   secretRef.String(),
		Outcome:      OutcomeSucceeded,
		Metadata:     raw,
		TraceID:      traceID,
		OccurredAt:   time.Now().UTC(),
	}
}

func syntheticEnvelope(wsID, secretRef uuid.UUID, version int) *CredentialEnvelope {
	return &CredentialEnvelope{
		WorkspaceID:   wsID,
		SecretRef:     secretRef,
		Version:       version,
		Ciphertext:    []byte("cipher"),
		DataNonce:     []byte("data-nonce-12b"),
		WrappedDEK:    []byte("wrapped-dek"),
		WrapNonce:     []byte("wrap-nonce-12b"),
		EnvelopeSuite: "AES256GCM-v1",
		KEKVersion:    1,
	}
}

// TestAtomicTxCommitWithoutAuditRollsBack 验证 Commit 审计闸门：未追加匹配
// AuditEvent 时拒绝提交并回滚，envelope 不残留。
func TestAtomicTxCommitWithoutAuditRollsBack(t *testing.T) {
	db, store, _, ws, _, _, _, cleanup := setupFull(t)
	defer db.Close()
	defer cleanup()

	ctx := context.Background()
	secretRef := uuid.New()
	op := newCredOp(t, ws.ID, uuid.New(), secretRef, uuid.NewString())

	atx, err := store.BeginCredential(ctx, op)
	if err != nil {
		t.Fatalf("BeginCredential: %v", err)
	}
	if err := atx.InsertEnvelope(ctx, syntheticEnvelope(ws.ID, secretRef, 1)); err != nil {
		t.Fatalf("InsertEnvelope: %v", err)
	}
	if err := atx.Commit(); err == nil {
		t.Fatal("Commit without audit must be rejected by gate")
	}

	// 验证回滚：envelope 不残留
	if _, err := store.EnvelopeByRef(ctx, ws.ID, secretRef, 1); err == nil {
		t.Fatal("envelope should not exist after gate rollback")
	}
}

// TestAtomicTxCommitWithMatchingAudit 验证成功路径：mutation + 匹配审计事件
// 原子提交，envelope 与 audit_events 均持久化。
func TestAtomicTxCommitWithMatchingAudit(t *testing.T) {
	db, store, u, ws, _, _, _, cleanup := setupFull(t)
	defer db.Close()
	defer cleanup()

	ctx := context.Background()
	secretRef := uuid.New()
	// 使用 setupFull 创建的真实用户 ID：audit_events.actor_id 受 fk_audit_actor
	// 外键约束，随机 uuid 不在 users 表会导致 INSERT 失败（CI API checks）。
	actorID := u.ID
	traceID := uuid.NewString()
	op := newCredOp(t, ws.ID, actorID, secretRef, traceID)

	atx, err := store.BeginCredential(ctx, op)
	if err != nil {
		t.Fatalf("BeginCredential: %v", err)
	}
	if err := atx.InsertEnvelope(ctx, syntheticEnvelope(ws.ID, secretRef, 1)); err != nil {
		t.Fatalf("InsertEnvelope: %v", err)
	}
	if err := atx.AppendAudit(ctx, op, newCredEvent(t, ws.ID, actorID, secretRef, 1, traceID)); err != nil {
		t.Fatalf("AppendAudit: %v", err)
	}
	if err := atx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	env, err := store.EnvelopeByRef(ctx, ws.ID, secretRef, 1)
	if err != nil {
		t.Fatalf("envelope should exist after atomic commit: %v", err)
	}
	if env == nil {
		t.Fatal("envelope nil after atomic commit")
	}
}

// TestAtomicTxAppendAuditWrongContextRejected 验证 AppendAudit 携带不匹配 op 被拒绝。
func TestAtomicTxAppendAuditWrongContextRejected(t *testing.T) {
	db, store, _, ws, _, _, _, cleanup := setupFull(t)
	defer db.Close()
	defer cleanup()

	ctx := context.Background()
	secretRef := uuid.New()
	op := newCredOp(t, ws.ID, uuid.New(), secretRef, uuid.NewString())

	atx, err := store.BeginCredential(ctx, op)
	if err != nil {
		t.Fatalf("BeginCredential: %v", err)
	}
	// AppendAudit 失败后必须终止事务并归还连接（CodeRabbit-6）。
	defer atx.Rollback()
	wrongOp, err := NewOperationContext(
		uuid.NewString(), ws.ID, "credential", secretRef.String(),
		ActionCredentialCreate, nil, uuid.New(), "user", string(OutcomeSucceeded), uuid.NewString(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := atx.AppendAudit(ctx, wrongOp, newCredEvent(t, ws.ID, uuid.New(), secretRef, 1, uuid.NewString())); err == nil {
		t.Fatal("AppendAudit with wrong op must be rejected")
	}
}

// TestConnectionAtomicTxCommitGate 验证连接原子事务闸门：无审计则拒绝提交。
func TestConnectionAtomicTxCommitGate(t *testing.T) {
	db, store, u, ws, _, _, env, cleanup := setupFull(t)
	defer db.Close()
	defer cleanup()

	ctx := context.Background()
	connID := uuid.New()
	op, err := NewOperationContext(
		uuid.NewString(), ws.ID, "connection", connID.String(),
		ActionConnectionCreate, &connID, u.ID, "user", string(OutcomeSucceeded), uuid.NewString(),
	)
	if err != nil {
		t.Fatal(err)
	}
	atx, err := store.BeginConnection(ctx, op)
	if err != nil {
		t.Fatalf("BeginConnection: %v", err)
	}
	conn := &Connection{
		ID:            connID,
		WorkspaceID:   ws.ID,
		Name:          "atomic-conn",
		Engine:        EnginePostgreSQL,
		Host:          "localhost",
		Port:          5432,
		Database:      "testdb",
		Environment:   EnvDevelopment,
		SecretRef:     env.SecretRef,
		SecretVersion: env.SecretVersion,
		// 使用 setupFull 创建的真实用户 ID：connections.created_by 受
		// fk_connections_created_by 外键约束，随机 uuid 不在 users 表会导致失败。
		CreatedBy: u.ID,
	}
	if err := atx.CreateConnection(ctx, conn); err != nil {
		t.Fatalf("CreateConnection: %v", err)
	}
	if err := atx.Commit(); err == nil {
		t.Fatal("Commit without audit must be rejected by gate")
	}
	if _, err := store.ConnectionByID(ctx, ws.ID, connID); err == nil {
		t.Fatal("connection should not exist after gate rollback")
	}
}

package execution

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/fujiabao89/webdb/internal/adapter"
	"github.com/fujiabao89/webdb/internal/credentials"
	"github.com/fujiabao89/webdb/internal/metadata"
	"github.com/google/uuid"
)

// TestAuditedExecute_MetadataNoCanary 验证审计 metadata 不含 SQL 正文、密码或结果。
func TestAuditedExecute_MetadataNoCanary(t *testing.T) {
	canarySQL := "SELECT 'sup3rsecret-pw-42' FROM users WHERE password = 'hunter2'"
	canaryPassword := "canary-db-password-!@#$%"

	principal, conn, policy, resolver, client, txStore, auditStore, alarm := auditedPipelineInputs()
	resolver.payload = credentials.CredentialPayload{User: "synthetic_user", Password: canaryPassword}
	client.handle.result = &adapter.QueryResult{TotalReturned: 1}

	pipeline := auditedPipeline(
		&fakeConnectionReader{connections: []*metadata.Connection{conn}},
		&fakePolicyReader{policy: policy},
		auditedMember(principal),
		resolver, client, txStore, auditStore, alarm, realClock(),
	)

	_, err := pipeline.Execute(context.Background(), ExecuteRequest{
		Principal:    principal,
		ConnectionID: conn.ID,
		SQL:          canarySQL,
		Engine:       EnginePostgreSQL,
	})
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}

	if len(auditStore.events) != 1 {
		t.Fatalf("audit events = %d, want 1", len(auditStore.events))
	}
	md := string(auditStore.events[0].Metadata)
	if strings.Contains(md, canarySQL) {
		t.Fatalf("audit metadata contains SQL body: %s", md)
	}
	if strings.Contains(md, canaryPassword) {
		t.Fatalf("audit metadata contains plaintext password: %s", md)
	}
	if strings.Contains(md, "hunter2") {
		t.Fatalf("audit metadata contains canary password fragment: %s", md)
	}
	if strings.Contains(md, "SELECT") {
		t.Fatalf("audit metadata must not contain SQL keywords: %s", md)
	}
}

// TestAuditedExecute_ErrorNoCanary 验证错误路径不含 SQL 正文或密码。
func TestAuditedExecute_ErrorNoCanary(t *testing.T) {
	canaryPassword := "canary-db-password-!@#$%"

	principal, conn, policy, resolver, client, txStore, auditStore, alarm := auditedPipelineInputs()
	resolver.payload = credentials.CredentialPayload{User: "synthetic_user", Password: canaryPassword}
	client.handle.err = &adapter.AdapterError{Code: adapter.ErrDatabaseError}

	pipeline := auditedPipeline(
		&fakeConnectionReader{connections: []*metadata.Connection{conn}},
		&fakePolicyReader{policy: policy},
		auditedMember(principal),
		resolver, client, txStore, auditStore, alarm, realClock(),
	)

	result, err := pipeline.Execute(context.Background(), ExecuteRequest{
		Principal:    principal,
		ConnectionID: conn.ID,
		SQL:          "SELECT 1",
		Engine:       EnginePostgreSQL,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), canaryPassword) {
		t.Fatalf("error message contains password: %v", err)
	}
	if strings.Contains(err.Error(), "SELECT 1") {
		t.Fatalf("error message contains SQL: %v", err)
	}
	if strings.Contains(string(result.ErrorCode), canaryPassword) {
		t.Fatalf("stable error code contains password: %v", result.ErrorCode)
	}
}

// TestStderrAlarmOutput 验证 $SECURITY_ALERT 输出不含敏感输入且为结构化字段。
func TestStderrAlarmOutput(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	// 恢复原 writer，而非 SetOutput(nil)，避免后续 NewStderrAlarm 的 log.Print panic（Qodo #5 / CodeRabbit #20）。
	t.Cleanup(func() { log.SetOutput(old) })

	alarm := NewStderrAlarm()
	alarm.Alarm(context.Background(), metadata.SecurityAlertEvent{
		TraceID:     "trace-1",
		WorkspaceID: uuidV4(),
		Code:        "audit_failed",
		OccurredAt:  time.Unix(1_700_000_000, 0).UTC(),
	})

	out := buf.String()
	if !strings.Contains(out, "$SECURITY_ALERT") {
		t.Fatalf("stderr alarm missing marker: %q", out)
	}
	// 结构化 key=value 字段便于监控聚合（CodeRabbit #28）。
	for _, field := range []string{"trace=trace-1", "workspace=", "code=audit_failed", "occurred_at=2023-11-14T22:13:20Z"} {
		if !strings.Contains(out, field) {
			t.Fatalf("stderr alarm missing structured field %q: %q", field, out)
		}
	}
	for _, sensitive := range []string{"sup3rsecret", "hunter2", "password=", "kek="} {
		if strings.Contains(out, sensitive) {
			t.Fatalf("stderr alarm leaked sensitive data %q: %q", sensitive, out)
		}
	}
}

func uuidV4() uuid.UUID {
	return uuid.MustParse("123e4567-e89b-42d3-a456-426614174000")
}

package metadata

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func testOp(t *testing.T, resource, resourceID, action, outcome string, conn *uuid.UUID) *OperationContext {
	t.Helper()
	op, err := NewOperationContext(
		"mutation-1", uuid.New(), resource, resourceID, action, conn,
		uuid.New(), "user", outcome, "trace-1",
	)
	if err != nil {
		t.Fatalf("NewOperationContext(%s/%s) error = %v", resource, action, err)
	}
	return op
}

func TestOperationContextValidate(t *testing.T) {
	ws := uuid.New()
	actor := uuid.New()
	conn := uuid.New()

	cases := []struct {
		name    string
		build   func() (*OperationContext, error)
		wantErr string // 期望的错误片段；空表示成功
	}{
		{"credential valid", func() (*OperationContext, error) {
			return NewOperationContext("m", ws, "credential", "ref", "credential.create", nil, actor, "user", "succeeded", "t")
		}, ""},
		{"connection valid", func() (*OperationContext, error) {
			return NewOperationContext("m", ws, "connection", "c1", "connection.create", &conn, actor, "user", "succeeded", "t")
		}, ""},
		{"nil mutation id", func() (*OperationContext, error) {
			return NewOperationContext("", ws, "credential", "ref", "credential.create", nil, actor, "user", "succeeded", "t")
		}, "empty mutation id"},
		{"zero workspace", func() (*OperationContext, error) {
			return NewOperationContext("m", uuid.Nil, "credential", "ref", "credential.create", nil, actor, "user", "succeeded", "t")
		}, "zero workspace"},
		{"invalid resource", func() (*OperationContext, error) {
			return NewOperationContext("m", ws, "execution", "ref", "x", nil, actor, "user", "succeeded", "t")
		}, "invalid resource"},
		{"empty resource id", func() (*OperationContext, error) {
			return NewOperationContext("m", ws, "credential", "", "credential.create", nil, actor, "user", "succeeded", "t")
		}, "empty resource id"},
		{"empty action", func() (*OperationContext, error) {
			return NewOperationContext("m", ws, "credential", "ref", "", nil, actor, "user", "succeeded", "t")
		}, "empty action"},
		{"empty actor type", func() (*OperationContext, error) {
			return NewOperationContext("m", ws, "credential", "ref", "credential.create", nil, actor, "", "succeeded", "t")
		}, "empty actor type"},
		{"empty outcome", func() (*OperationContext, error) {
			return NewOperationContext("m", ws, "credential", "ref", "credential.create", nil, actor, "user", "", "t")
		}, "empty outcome"},
		{"connection missing conn id", func() (*OperationContext, error) {
			return NewOperationContext("m", ws, "connection", "c1", "connection.create", nil, actor, "user", "succeeded", "t")
		}, "connection-scoped but missing or zero connection id"},
		{"connection zero conn id", func() (*OperationContext, error) {
			z := uuid.Nil
			return NewOperationContext("m", ws, "connection", "c1", "connection.create", &z, actor, "user", "succeeded", "t")
		}, "connection-scoped but missing or zero connection id"},
		{"zero actor", func() (*OperationContext, error) {
			return NewOperationContext("m", ws, "credential", "ref", "credential.create", nil, uuid.Nil, "user", "succeeded", "t")
		}, "missing actor or trace"},
		{"empty trace", func() (*OperationContext, error) {
			return NewOperationContext("m", ws, "credential", "ref", "credential.create", nil, actor, "user", "succeeded", "")
		}, "missing actor or trace"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 直接断言构造器返回的错误（CodeRabbit-7）：校验失败时构造器返回 (nil, err)，
			// 仅对成功构造的上下文再调用 Validate 保持直接覆盖。
			op, err := tc.build()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("NewOperationContext() error = %v, want nil", err)
				}
				if op == nil {
					t.Fatal("NewOperationContext() op = nil on success")
				}
				if verr := op.Validate(); verr != nil {
					t.Fatalf("Validate() error = %v, want nil", verr)
				}
				return
			}
			if err == nil {
				t.Fatalf("NewOperationContext() = nil error, want containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("NewOperationContext() error = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestEventAllowed(t *testing.T) {
	allowed := [][3]string{
		{"connection", "connection.create", "succeeded"},
		{"connection", "connection.update", "succeeded"},
		{"credential", "credential.create", "succeeded"},
		{"credential", "credential.rotate", "succeeded"},
		{"credential", "credential.rotate", "failed"},
		{"credential", "credential.retire", "succeeded"},
	}
	for _, c := range allowed {
		if !eventAllowed(c[0], c[1], c[2]) {
			t.Errorf("eventAllowed(%s/%s/%s) = false, want true", c[0], c[1], c[2])
		}
	}
	denied := [][3]string{
		{"connection", "connection.test", "succeeded"}, // E7 外部副作用例外，不允许原子化
		{"credential", "credential.create", "failed"},
		{"credential", "credential.retire", "failed"},
		{"execution", "sql.execute", "succeeded"},
	}
	for _, c := range denied {
		if eventAllowed(c[0], c[1], c[2]) {
			t.Errorf("eventAllowed(%s/%s/%s) = true, want false", c[0], c[1], c[2])
		}
	}
}

func TestValidateOpForCredentialCrossDomainRejected(t *testing.T) {
	conn := uuid.New()
	op := testOp(t, "connection", "c1", "connection.create", "succeeded", &conn)
	if err := validateOpForCredential(op); err == nil {
		t.Fatal("validateOpForCredential(connection op) = nil, want error (cross-domain)")
	}
	if err := validateOpForConnection(testOp(t, "credential", "ref", "credential.create", "succeeded", nil)); err == nil {
		t.Fatal("validateOpForConnection(credential op) = nil, want error (cross-domain)")
	}
}

func TestValidateOpForCredentialInvalidEvent(t *testing.T) {
	if err := validateOpForCredential(testOp(t, "credential", "ref", "credential.retire", "failed", nil)); err == nil {
		t.Fatal("invalid credential.retire/failed accepted, want error")
	}
	conn := uuid.New()
	if err := validateOpForConnection(testOp(t, "connection", "c1", "connection.test", "succeeded", &conn)); err == nil {
		t.Fatal("connection.test/succeeded accepted, want error (E7 exception)")
	}
}

func TestValidateOpForCredentialNil(t *testing.T) {
	if err := validateOpForCredential(nil); err == nil || !strings.Contains(err.Error(), "nil") {
		t.Fatalf("validateOpForCredential(nil) = %v, want nil-context error", err)
	}
	if err := validateOpForConnection(nil); err == nil || !strings.Contains(err.Error(), "nil") {
		t.Fatalf("validateOpForConnection(nil) = %v, want nil-context error", err)
	}
}

func TestAuditMatchFieldsReturnsDefensiveCopy(t *testing.T) {
	first := AuditMatchFields()
	second := AuditMatchFields()
	if len(first) != len(auditMatchFields) {
		t.Fatalf("AuditMatchFields() len = %d, want %d", len(first), len(auditMatchFields))
	}
	first[0] = "mutated"
	if second[0] == "mutated" {
		t.Fatal("AuditMatchFields() not a defensive copy")
	}
}

func TestMatchOperationContext(t *testing.T) {
	op := testOp(t, "credential", "ref", "credential.create", "succeeded", nil)
	if !matchOperationContext(op, op) {
		t.Fatal("matchOperationContext(same) = false")
	}
	// 构造仅 mutationID 不同的第二个上下文（CodeRabbit-8）：此前 testOp 每次生成新的
	// workspaceID/actorID，断言失败消息虽称 "different mutationID"，实际差异在身份字段，
	// 对 mutationID 比较无回归保护。
	other, err := NewOperationContext(
		"mutation-2", op.WorkspaceID(), op.Resource(), op.ResourceID(), op.Action(),
		nil, op.ActorID(), op.ActorType(), op.Outcome(), op.TraceID(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if matchOperationContext(op, other) {
		t.Fatal("matchOperationContext(different mutationID) = true")
	}
	if matchOperationContext(nil, op) || matchOperationContext(op, nil) {
		t.Fatal("matchOperationContext(nil) = true")
	}
}

func TestMatchAuditEventOp(t *testing.T) {
	ws := uuid.New()
	actor := uuid.New()
	conn := uuid.New()
	op, err := NewOperationContext("m", ws, "connection", "c1", "connection.create", &conn, actor, "user", "succeeded", "trace-x")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := (AuditMetadata{Engine: strPtr("postgresql"), Environment: strPtr("development")}).Marshal()
	base := &AuditEvent{
		WorkspaceID:  ws,
		ActorType:    ActorTypeUser,
		ActorID:      &actor,
		ConnectionID: &conn,
		Action:       ActionConnectionCreate,
		ResourceType: "connection",
		ResourceID:   "c1",
		Outcome:      OutcomeSucceeded,
		Metadata:     raw,
		TraceID:      "trace-x",
		OccurredAt:   time.Now().UTC(),
	}

	t.Run("match", func(t *testing.T) {
		if err := matchAuditEventOp(op, base); err != nil {
			t.Fatalf("matchAuditEventOp(match) = %v", err)
		}
	})
	t.Run("workspace mismatch", func(t *testing.T) {
		e := *base
		e.WorkspaceID = uuid.New()
		if err := matchAuditEventOp(op, &e); err == nil {
			t.Fatal("workspace mismatch not detected")
		}
	})
	t.Run("action mismatch", func(t *testing.T) {
		e := *base
		e.Action = ActionConnectionUpdate
		if err := matchAuditEventOp(op, &e); err == nil {
			t.Fatal("action mismatch not detected")
		}
	})
	t.Run("resource mismatch", func(t *testing.T) {
		e := *base
		e.ResourceType = "credential"
		if err := matchAuditEventOp(op, &e); err == nil {
			t.Fatal("resource mismatch not detected")
		}
	})
	t.Run("connection mismatch", func(t *testing.T) {
		e := *base
		e.ConnectionID = &uuid.UUID{}
		if err := matchAuditEventOp(op, &e); err == nil {
			t.Fatal("connection mismatch not detected")
		}
	})
	t.Run("outcome mismatch", func(t *testing.T) {
		e := *base
		e.Outcome = OutcomeFailed
		if err := matchAuditEventOp(op, &e); err == nil {
			t.Fatal("outcome mismatch not detected")
		}
	})
	t.Run("trace mismatch", func(t *testing.T) {
		e := *base
		e.TraceID = "other"
		if err := matchAuditEventOp(op, &e); err == nil {
			t.Fatal("trace mismatch not detected")
		}
	})

	// credential-only event must not carry connection
	credOp, _ := NewOperationContext("m2", ws, "credential", "ref", "credential.create", nil, actor, "user", "succeeded", "trace-y")
	credEvent := *base
	credEvent.Action = ActionCredentialCreate
	credEvent.ResourceType = "credential"
	credEvent.ResourceID = "ref"
	credEvent.TraceID = "trace-y"
	if err := matchAuditEventOp(credOp, &credEvent); err == nil {
		t.Fatal("credential event with connection should be rejected")
	}
	credEvent.ConnectionID = nil
	if err := matchAuditEventOp(credOp, &credEvent); err != nil {
		t.Fatalf("credential event (nil conn) rejected: %v", err)
	}
}

// TestAtomicTxDomainIsolation 验证原子 wrapper 类型不跨域实现对方 mutation 接口
// （设计 §3 不跨域约束）。
func TestAtomicTxDomainIsolation(t *testing.T) {
	var credTx interface{} = (*pgCredentialAtomicTx)(nil)
	if _, ok := credTx.(ConnectionMutationTx); ok {
		t.Fatal("pgCredentialAtomicTx unexpectedly implements ConnectionMutationTx")
	}
	var connTx interface{} = (*pgConnectionAtomicTx)(nil)
	if _, ok := connTx.(CredentialMutationTx); ok {
		t.Fatal("pgConnectionAtomicTx unexpectedly implements CredentialMutationTx")
	}
	var metaTx interface{} = (*pgMetadataTx)(nil)
	if _, ok := metaTx.(CredentialAtomicTx); ok {
		t.Fatal("pgMetadataTx unexpectedly implements CredentialAtomicTx")
	}
	if _, ok := metaTx.(ConnectionAtomicTx); ok {
		t.Fatal("pgMetadataTx unexpectedly implements ConnectionAtomicTx")
	}
}

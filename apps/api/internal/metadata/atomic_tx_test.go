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
		op      func() *OperationContext
		wantErr bool
	}{
		{"credential valid", func() *OperationContext {
			op, _ := NewOperationContext("m", ws, "credential", "ref", "credential.create", nil, actor, "user", "succeeded", "t")
			return op
		}, false},
		{"connection valid", func() *OperationContext {
			op, _ := NewOperationContext("m", ws, "connection", "c1", "connection.create", &conn, actor, "user", "succeeded", "t")
			return op
		}, false},
		{"nil mutation id", func() *OperationContext {
			op, _ := NewOperationContext("", ws, "credential", "ref", "credential.create", nil, actor, "user", "succeeded", "t")
			return op
		}, true},
		{"zero workspace", func() *OperationContext {
			op, _ := NewOperationContext("m", uuid.Nil, "credential", "ref", "credential.create", nil, actor, "user", "succeeded", "t")
			return op
		}, true},
		{"invalid resource", func() *OperationContext {
			op, _ := NewOperationContext("m", ws, "execution", "ref", "x", nil, actor, "user", "succeeded", "t")
			return op
		}, true},
		{"empty resource id", func() *OperationContext {
			op, _ := NewOperationContext("m", ws, "credential", "", "credential.create", nil, actor, "user", "succeeded", "t")
			return op
		}, true},
		{"empty action", func() *OperationContext {
			op, _ := NewOperationContext("m", ws, "credential", "ref", "", nil, actor, "user", "succeeded", "t")
			return op
		}, true},
		{"empty actor type", func() *OperationContext {
			op, _ := NewOperationContext("m", ws, "credential", "ref", "credential.create", nil, actor, "", "succeeded", "t")
			return op
		}, true},
		{"empty outcome", func() *OperationContext {
			op, _ := NewOperationContext("m", ws, "credential", "ref", "credential.create", nil, actor, "user", "", "t")
			return op
		}, true},
		{"connection missing conn id", func() *OperationContext {
			op, _ := NewOperationContext("m", ws, "connection", "c1", "connection.create", nil, actor, "user", "succeeded", "t")
			return op
		}, true},
		{"connection zero conn id", func() *OperationContext {
			z := uuid.Nil
			op, _ := NewOperationContext("m", ws, "connection", "c1", "connection.create", &z, actor, "user", "succeeded", "t")
			return op
		}, true},
		{"zero actor", func() *OperationContext {
			op, _ := NewOperationContext("m", ws, "credential", "ref", "credential.create", nil, uuid.Nil, "user", "succeeded", "t")
			return op
		}, true},
		{"empty trace", func() *OperationContext {
			op, _ := NewOperationContext("m", ws, "credential", "ref", "credential.create", nil, actor, "user", "succeeded", "")
			return op
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			op := tc.op()
			err := op.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tc.wantErr)
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
	op2 := testOp(t, "credential", "ref", "credential.create", "succeeded", nil)
	if matchOperationContext(op, op2) {
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

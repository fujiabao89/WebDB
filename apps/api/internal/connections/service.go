// Package connections 提供连接生命周期的最小 orchestration seam。
// 仅接入追加式审计（E1/E2/E7/E8），不实现公开 HTTP API。
package connections

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fujiabao89/webdb/internal/adapter"
	"github.com/fujiabao89/webdb/internal/credentials"
	"github.com/fujiabao89/webdb/internal/metadata"
	"github.com/google/uuid"
)

// StableErrorCode 连接服务稳定错误码。
type StableErrorCode string

func (e StableErrorCode) Error() string { return string(e) }

const (
	ErrConnectionNotFound StableErrorCode = "connection_not_found"
	ErrForbidden          StableErrorCode = "forbidden"
	ErrInternalError      StableErrorCode = "internal_error"
	ErrAuditFailed        StableErrorCode = "audit_failed"
)

// Principal 由可信上游提供的已验证身份。
type Principal struct {
	UserID      uuid.UUID
	WorkspaceID uuid.UUID
}

// ConnectionTester 抽象连接测试能力（生产实现包装 adapter.PoolHandle.Ping）。
type ConnectionTester interface {
	Ping(ctx context.Context, cfg adapter.ConnectConfig) error
}

// AdapterPingTester 将 AdapterManager 收窄为 ConnectionTester。
type AdapterPingTester struct {
	Manager *adapter.AdapterManager
}

// Ping 获取连接并执行 Ping。
func (t AdapterPingTester) Ping(ctx context.Context, cfg adapter.ConnectConfig) error {
	handle, err := t.Manager.Get(ctx, cfg)
	if err != nil {
		return err
	}
	defer handle.Release()
	return handle.Ping(ctx)
}

// Service 连接生命周期服务（orchestration seam）。
type Service struct {
	conns    metadata.ConnectionStore
	members  metadata.WorkspaceMemberStore
	audit    metadata.AuditEventStore
	alarm    metadata.SecurityAlarm
	resolver credentials.CredentialResolver
	tester   ConnectionTester
	clock    func() time.Time
	newTrace func() string
}

// NewService 创建连接服务。
func NewService(
	conns metadata.ConnectionStore,
	members metadata.WorkspaceMemberStore,
	audit metadata.AuditEventStore,
	alarm metadata.SecurityAlarm,
	resolver credentials.CredentialResolver,
	tester ConnectionTester,
) *Service {
	return &Service{
		conns:    conns,
		members:  members,
		audit:    audit,
		alarm:    alarm,
		resolver: resolver,
		tester:   tester,
		clock:    func() time.Time { return time.Now().UTC() },
		newTrace: func() string { return uuid.NewString() },
	}
}

// Create 创建连接并写入 E1 connection.create。
func (s *Service) Create(ctx context.Context, p Principal, conn *metadata.Connection) (*metadata.Connection, error) {
	if err := s.requireManager(ctx, p); err != nil {
		return nil, err
	}
	conn.WorkspaceID = p.WorkspaceID
	conn.CreatedBy = p.UserID
	if err := s.conns.CreateConnection(ctx, conn); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInternalError, err)
	}

	event, err := newConnectionAuditEvent(
		p, conn.ID, metadata.ActionConnectionCreate, "connection", conn.ID.String(),
		metadata.OutcomeSucceeded,
		metadata.AuditMetadata{
			Engine:      strPtr(string(conn.Engine)),
			Environment: strPtr(string(conn.Environment)),
		},
		s.newTrace(), s.clock(),
	)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAuditFailed, err)
	}
	if err := s.writeAudit(ctx, event, conn.WorkspaceID); err != nil {
		return nil, err
	}
	return conn, nil
}

// Update 更新连接并写入 E2 connection.update。
func (s *Service) Update(ctx context.Context, p Principal, conn *metadata.Connection) error {
	if err := s.requireManager(ctx, p); err != nil {
		return err
	}
	conn.WorkspaceID = p.WorkspaceID
	if err := s.conns.UpdateConnection(ctx, p.WorkspaceID, conn); err != nil {
		return fmt.Errorf("%w: %v", ErrInternalError, err)
	}

	event, err := newConnectionAuditEvent(
		p, conn.ID, metadata.ActionConnectionUpdate, "connection", conn.ID.String(),
		metadata.OutcomeSucceeded,
		metadata.AuditMetadata{Environment: strPtr(string(conn.Environment))},
		s.newTrace(), s.clock(),
	)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrAuditFailed, err)
	}
	if err := s.writeAudit(ctx, event, conn.WorkspaceID); err != nil {
		return err
	}
	return nil
}

// Test 测试连接连通性。成功写入 E7，失败写入 E8。
func (s *Service) Test(ctx context.Context, p Principal, connID uuid.UUID) error {
	conn, err := s.conns.ConnectionByID(ctx, p.WorkspaceID, connID)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrConnectionNotFound, err)
	}

	payload, err := s.resolver.ResolveCredential(ctx, conn.WorkspaceID, conn.SecretRef, conn.SecretVersion)
	if err != nil {
		code := mapCredentialError(err)
		s.alarm.Alarm(ctx, metadata.SecurityAlertEvent{
			TraceID:     s.newTrace(),
			WorkspaceID: conn.WorkspaceID,
			Code:        code,
			OccurredAt:  s.clock(),
		})
		event, buildErr := newConnectionAuditEvent(
			p, conn.ID, metadata.ActionConnectionTest, "connection", conn.ID.String(),
			metadata.OutcomeFailed,
			metadata.AuditMetadata{
				Engine:      strPtr(string(conn.Engine)),
				Environment: strPtr(string(conn.Environment)),
				ErrorCode:   strPtr(code),
			},
			s.newTrace(), s.clock(),
		)
		if buildErr != nil {
			return buildErr
		}
		if writeErr := s.writeAudit(ctx, event, conn.WorkspaceID); writeErr != nil {
			return writeErr
		}
		return fmt.Errorf("%w: %v", ErrInternalError, err)
	}

	cfg := adapter.ConnectConfig{
		ConnectionID:  conn.ID.String(),
		SecretVersion: conn.SecretVersion,
		Engine:        adapter.Engine(conn.Engine),
		Host:          conn.Host,
		Port:          conn.Port,
		User:          payload.User,
		Password:      payload.Password,
		Database:      conn.Database,
		TLS:           adapter.TLSRequire,
	}

	start := s.clock()
	testErr := s.tester.Ping(ctx, cfg)
	durationMs := int(s.clock().Sub(start).Milliseconds())
	if durationMs < 0 {
		durationMs = 0
	}

	outcome := metadata.OutcomeSucceeded
	md := metadata.AuditMetadata{
		Engine:      strPtr(string(conn.Engine)),
		Environment: strPtr(string(conn.Environment)),
		DurationMs:  intPtr(durationMs),
	}
	if testErr != nil {
		outcome = metadata.OutcomeFailed
		md.ErrorCode = strPtr(mapConnectionTestError(testErr))
	}

	event, err := newConnectionAuditEvent(
		p, conn.ID, metadata.ActionConnectionTest, "connection", conn.ID.String(),
		outcome, md, s.newTrace(), s.clock(),
	)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrAuditFailed, err)
	}
	if err := s.writeAudit(ctx, event, conn.WorkspaceID); err != nil {
		return err
	}
	if testErr != nil {
		return fmt.Errorf("%w: %v", ErrInternalError, testErr)
	}
	return nil
}

// writeAudit 写审计；失败触发安全告警并返回 audit_failed。
func (s *Service) writeAudit(ctx context.Context, event *metadata.AuditEvent, wsID uuid.UUID) error {
	if err := s.audit.AppendAudit(ctx, event); err != nil {
		s.alarm.Alarm(ctx, metadata.SecurityAlertEvent{
			TraceID:     event.TraceID,
			WorkspaceID: wsID,
			Code:        string(ErrAuditFailed),
			OccurredAt:  event.OccurredAt,
		})
		return fmt.Errorf("%w: %v", ErrAuditFailed, err)
	}
	return nil
}

// requireManager 校验调用者为工作区 owner/admin 成员。
func (s *Service) requireManager(ctx context.Context, p Principal) error {
	if s.members == nil {
		return fmt.Errorf("%w: members store not configured", ErrInternalError)
	}
	member, err := s.members.MemberByWorkspaceAndUser(ctx, p.WorkspaceID, p.UserID)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrForbidden, err)
	}
	if member == nil {
		return fmt.Errorf("%w", ErrForbidden)
	}
	switch member.Role {
	case metadata.RoleOwner, metadata.RoleAdmin:
		return nil
	default:
		return fmt.Errorf("%w", ErrForbidden)
	}
}

// newConnectionAuditEvent 构建连接审计事件（user actor）。
func newConnectionAuditEvent(
	p Principal,
	connID uuid.UUID,
	action, resourceType, resourceID string,
	outcome metadata.AuditOutcome,
	md metadata.AuditMetadata,
	traceID string,
	occurredAt time.Time,
) (*metadata.AuditEvent, error) {
	raw, err := md.Marshal()
	if err != nil {
		return nil, err
	}
	actorID := p.UserID
	connRef := connID
	return &metadata.AuditEvent{
		WorkspaceID:  p.WorkspaceID,
		ActorType:    metadata.ActorTypeUser,
		ActorID:      &actorID,
		ConnectionID: &connRef,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		Outcome:      outcome,
		Metadata:     raw,
		TraceID:      traceID,
		OccurredAt:   occurredAt,
	}, nil
}

func mapCredentialError(err error) string {
	if credentials.IsErrorCode(err, credentials.ErrCredentialNotFound) {
		return string(credentials.ErrCredentialNotFound)
	}
	if credentials.IsErrorCode(err, credentials.ErrCredentialRetired) {
		return string(credentials.ErrCredentialRetired)
	}
	if credentials.IsErrorCode(err, credentials.ErrDecryptionFailed) {
		return string(credentials.ErrDecryptionFailed)
	}
	if credentials.IsErrorCode(err, credentials.ErrUnknownKEKVersion) {
		return string(credentials.ErrUnknownKEKVersion)
	}
	return string(credentials.ErrInternalError)
}

func mapConnectionTestError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "execution_timeout"
	}
	var adapterErr *adapter.AdapterError
	if errors.As(err, &adapterErr) {
		return string(adapterErr.Code)
	}
	return "connection_failed"
}

func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }

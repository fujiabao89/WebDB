package metadata

import (
	"context"
	"log"
	"time"

	"github.com/google/uuid"
)

// SecurityAlertEvent 安全告警事件。只包含安全稳定字段（trace/workspace/稳定错误码/时间），
// 不含任何原始错误、密钥、密码、SQL 正文或结果数据。
type SecurityAlertEvent struct {
	TraceID     string
	WorkspaceID uuid.UUID
	Code        string // 稳定错误码（ADR-017 §6）
	OccurredAt  time.Time
}

// SecurityAlarm 独立安全告警通道（E17）。
// 凭证解密失败、未知 KEK 版本、审计写入失败必须产生告警（proposal §9.2）。
// 告警自身失败不得递归写回审计系统，不得泄漏原始错误或敏感信息。
type SecurityAlarm interface {
	Alarm(ctx context.Context, e SecurityAlertEvent)
}

// EmitAlarm 发送安全告警并捕获告警通道实现自身的 panic（T28）。
// 安全告警是 E17 的最后防线：即使告警实现崩溃，也不能 panic、递归审计
// 或改变调用方的 fail-closed 返回；失败仅记录到 stderr 由基础设施处理。
func EmitAlarm(alarm SecurityAlarm, ctx context.Context, e SecurityAlertEvent) {
	if alarm == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("$SECURITY_ALERT_CHANNEL_FAILED code=%s occurred_at=%s",
				e.Code, e.OccurredAt.Format(time.RFC3339))
		}
	}()
	alarm.Alarm(ctx, e)
}

// StderrSecurityAlarm 将安全告警写入 stderr（基础设施级最终 fallback，T28）。
// 输出结构化 key=value 字段（trace/workspace/code/occurred_at），便于监控系统聚合。
type StderrSecurityAlarm struct{}

// NewStderrSecurityAlarm 创建 stderr 安全告警通道。
func NewStderrSecurityAlarm() SecurityAlarm { return StderrSecurityAlarm{} }

func (StderrSecurityAlarm) Alarm(ctx context.Context, e SecurityAlertEvent) {
	log.Printf("$SECURITY_ALERT trace=%s workspace=%s code=%s occurred_at=%s",
		e.TraceID, e.WorkspaceID.String(), e.Code, e.OccurredAt.Format(time.RFC3339))
}

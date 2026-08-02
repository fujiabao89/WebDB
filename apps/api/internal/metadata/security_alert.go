package metadata

import (
	"context"
	"log"
	"regexp"
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

// sensitiveLogPatterns 匹配结构化日志中可能出现的敏感内容
// （密码、KEK/DEK、密钥、token、连接串用户密码、私钥）。
var sensitiveLogPatterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`(?i)(password|passwd|pwd)\s*[=:]\s*[^\s,;]+`), "$1=[redacted]"},
	{regexp.MustCompile(`(?i)(kek|wrap_dek|data_nonce|wrap_nonce|dek)\s*[=:]\s*[^\s,;]+`), "$1=[redacted]"},
	{regexp.MustCompile(`(?i)(secret|token|api[_-]?key)\s*[=:]\s*[^\s,;]+`), "$1=[redacted]"},
	{regexp.MustCompile(`(?i)(postgres|postgresql|mysql|redis)://[^@\s]+@`), "$1://[redacted]@"},
	{regexp.MustCompile(`-----BEGIN [A-Z ]+PRIVATE KEY-----.*?(-----END [A-Z ]+PRIVATE KEY-----|$)`), "[redacted: private key]"},
}

// RedactSensitive 对日志/错误文本做统一脱敏，禁止原始敏感内容进入结构化日志
// （credentials/connections 的 logStorageFailure 共用，vti-OS/vti-OZ）。
func RedactSensitive(s string) string {
	for _, p := range sensitiveLogPatterns {
		s = p.re.ReplaceAllString(s, p.repl)
	}
	return s
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

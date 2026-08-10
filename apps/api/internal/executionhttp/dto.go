package executionhttp

// ExecuteRequestDto 第一页执行请求（P0-06A §8.1）。
// order_by 只是客户端排序意图，不是唯一性证明；唯一性只由服务端 VerifiedSortPlan 证明。
type ExecuteRequestDto struct {
	ConnectionID string       `json:"connection_id"`
	SQL          string       `json:"sql"`
	PageSize     int          `json:"page_size"`
	OrderBy      []OrderByDto `json:"order_by"`
}

// OrderByDto 单条排序意图。
type OrderByDto struct {
	Column    string `json:"column"`
	Order     string `json:"order"`
	NullsLast bool   `json:"nulls_last"`
}

// NextPageRequestDto 单向续页请求（P0-06A §9.2）：客户端只提交 opaque token，
// 不得重新提交 SQL/args/connection/order_by/unique/policy。
type NextPageRequestDto struct {
	NextPageToken string `json:"next_page_token"`
}

// AuditReceiptDto 审计 receipt（P0-06A §11.2 D14）。
type AuditReceiptDto struct {
	State        string `json:"state"`
	AuditEventID string `json:"audit_event_id"`
	ExecutionID  string `json:"execution_id"`
	TraceID      string `json:"trace_id"`
	Outcome      string `json:"outcome"`
}

// QueryPageDto 分页 meta。next_page_token 仅在确有后续页时返回（P0-06A §8.2/§9.3）。
type QueryPageDto struct {
	PageSize      int     `json:"page_size"`
	HasMore       bool    `json:"has_more"`
	NextPageToken *string `json:"next_page_token,omitempty"`
}

// QueryMetaDto 成功响应 meta。
type QueryMetaDto struct {
	Page  QueryPageDto    `json:"page"`
	Audit AuditReceiptDto `json:"audit"`
}

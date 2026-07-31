package metadata

import (
	"encoding/json"
	"testing"
)

func TestSanitizeAuditMetadata_NumericTypes(t *testing.T) {
	// [CR #19] 表驱动测试：duration_ms / row_count / rows_affected 类型边界
	tests := []struct {
		name    string
		input   map[string]any
		wantKey string
		wantOk  bool // key 应存在于过滤结果中
	}{
		// 正常整数值
		{name: "duration_ms valid", input: map[string]any{"duration_ms": 42.0}, wantKey: "duration_ms", wantOk: true},
		{name: "row_count valid", input: map[string]any{"row_count": 100.0}, wantKey: "row_count", wantOk: true},
		{name: "rows_affected valid", input: map[string]any{"rows_affected": 0.0}, wantKey: "rows_affected", wantOk: true},

		// 负数 → 丢弃
		{name: "duration_ms negative", input: map[string]any{"duration_ms": -1.0}, wantKey: "duration_ms", wantOk: false},
		{name: "row_count negative", input: map[string]any{"row_count": -5.0}, wantKey: "row_count", wantOk: false},
		{name: "rows_affected negative", input: map[string]any{"rows_affected": -3.0}, wantKey: "rows_affected", wantOk: false},

		// 小数 → 丢弃（val != float64(int64(val))）
		{name: "duration_ms decimal", input: map[string]any{"duration_ms": 3.14}, wantKey: "duration_ms", wantOk: false},
		{name: "row_count decimal", input: map[string]any{"row_count": 0.5}, wantKey: "row_count", wantOk: false},

		// 超过 int32 上限 → 丢弃
		{name: "duration_ms overflow", input: map[string]any{"duration_ms": float64(1 << 32)}, wantKey: "duration_ms", wantOk: false},
		{name: "row_count overflow", input: map[string]any{"row_count": float64(1 << 33)}, wantKey: "row_count", wantOk: false},

		// 错误字段类型：float64 用于非数字 key → 丢弃
		{name: "engine as float64", input: map[string]any{"engine": 1.0}, wantKey: "engine", wantOk: false},
		{name: "environment as float64", input: map[string]any{"environment": 0.0}, wantKey: "environment", wantOk: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, _ := json.Marshal(tt.input)
			filtered := sanitizeAuditMetadata(raw)
			var m map[string]any
			if err := json.Unmarshal(filtered, &m); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			_, exists := m[tt.wantKey]
			if tt.wantOk && !exists {
				t.Errorf("key %q should exist but was filtered out", tt.wantKey)
			}
			if !tt.wantOk && exists {
				t.Errorf("key %q should be filtered out but exists with value %v", tt.wantKey, m[tt.wantKey])
			}
		})
	}
}

func TestSanitizeAuditMetadata_BoolTypes(t *testing.T) {
	// [CR #19] cached 接受 bool，其他 key 拒绝 bool
	tests := []struct {
		name    string
		input   map[string]any
		wantKey string
		wantOk  bool
	}{
		{name: "cached true", input: map[string]any{"cached": true}, wantKey: "cached", wantOk: true},
		{name: "cached false", input: map[string]any{"cached": false}, wantKey: "cached", wantOk: true},
		{name: "engine as bool", input: map[string]any{"engine": true}, wantKey: "engine", wantOk: false},
		{name: "environment as bool", input: map[string]any{"environment": false}, wantKey: "environment", wantOk: false},
		{name: "error_code as bool", input: map[string]any{"error_code": true}, wantKey: "error_code", wantOk: false},
		{name: "duration_ms as bool", input: map[string]any{"duration_ms": true}, wantKey: "duration_ms", wantOk: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, _ := json.Marshal(tt.input)
			filtered := sanitizeAuditMetadata(raw)
			var m map[string]any
			if err := json.Unmarshal(filtered, &m); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			_, exists := m[tt.wantKey]
			if tt.wantOk && !exists {
				t.Errorf("key %q should exist but was filtered out", tt.wantKey)
			}
			if !tt.wantOk && exists {
				t.Errorf("key %q should be filtered out but exists with value %v", tt.wantKey, m[tt.wantKey])
			}
		})
	}
}

func TestSanitizeAuditMetadata_WrongTypeForKey(t *testing.T) {
	// [CR #19] 类型不匹配的 key 被丢弃
	tests := []struct {
		name  string
		input map[string]any
	}{
		{name: "engine as number", input: map[string]any{"engine": 1.0}},
		{name: "environment as number", input: map[string]any{"environment": 2.0}},
		{name: "error_code as number", input: map[string]any{"error_code": 0.0}},
		{name: "reason_code as number", input: map[string]any{"reason_code": 3.14}},
		{name: "cached as number", input: map[string]any{"cached": 1.0}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, _ := json.Marshal(tt.input)
			filtered := sanitizeAuditMetadata(raw)
			var m map[string]any
			if err := json.Unmarshal(filtered, &m); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if len(m) != 0 {
				t.Errorf("expected empty result, got %v", m)
			}
		})
	}
}

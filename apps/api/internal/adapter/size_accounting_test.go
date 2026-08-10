package adapter

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// TestCopyAndMeasureByteAccounting 验证文本 string 与二进制 []byte 的字节计数一致：
// MySQL 文本列规范化为 string 后，MaxCellBytes/MaxPageBytes 仍按真实字节数计算，不被绕过。
func TestCopyAndMeasureByteAccounting(t *testing.T) {
	t.Parallel()

	payload := []byte("hello world")
	d1, n1, err := copyAndMeasure([]any{payload}, 1<<20)
	if err != nil {
		t.Fatalf("copyAndMeasure([]byte) error = %v", err)
	}
	d2, n2, err := copyAndMeasure([]any{string(payload)}, 1<<20)
	if err != nil {
		t.Fatalf("copyAndMeasure(string) error = %v", err)
	}
	if n1 != n2 || n1 != len(payload) {
		t.Fatalf("byte accounting mismatch: []byte=%d string=%d, want %d", n1, n2, len(payload))
	}
	if !bytes.Equal(d1[0].([]byte), payload) {
		t.Fatalf("[]byte value not preserved: %x", d1[0].([]byte))
	}
	if d2[0].(string) != string(payload) {
		t.Fatalf("string value not preserved: %q", d2[0].(string))
	}
}

// TestCopyAndMeasureCellLimitString 验证超 MaxCellBytes 的文本（string）同样被拒绝：
// 规范化转换不会绕过单元格大小上限。
func TestCopyAndMeasureCellLimitString(t *testing.T) {
	t.Parallel()

	big := strings.Repeat("x", 300)
	_, _, err := copyAndMeasure([]any{big}, 256)
	var adapterErr *AdapterError
	if !errors.As(err, &adapterErr) || adapterErr.Code != ErrResultTooLarge {
		t.Fatalf("copyAndMeasure() error = %v, want %s", err, ErrResultTooLarge)
	}
}

// TestCopyAndMeasureDefensiveCopyBinary 验证二进制列返回独立 []byte 副本：
// 修改驱动原始缓冲区不影响结果（防御性复制）。
func TestCopyAndMeasureDefensiveCopyBinary(t *testing.T) {
	t.Parallel()

	src := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	d, _, err := copyAndMeasure([]any{src}, 1<<20)
	if err != nil {
		t.Fatalf("copyAndMeasure() error = %v", err)
	}
	out := d[0].([]byte)
	src[0] = 0x00 // 模拟驱动复用缓冲区被后续行覆盖
	if out[0] != 0xDE {
		t.Fatalf("defensive copy violated: out[0]=%#x, want 0xDE", out[0])
	}
}

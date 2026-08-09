package adapter

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// TestStepRowReadAheadSkipsTextCopy 验证页边界预读哨兵行的控制流：
// rc >= effPage 时，超大文本单元格不得执行 []byte→string 规范化/复制，
// 只递增行计数（finalizeResult 会丢弃该行）。若预读判定晚于规范化，
// 此测试失败：vals[0] 会被替换成 string。
func TestStepRowReadAheadSkipsTextCopy(t *testing.T) {
	t.Parallel()

	big := []byte(strings.Repeat("x", 1<<20)) // 1 MiB 文本单元格
	vals := []any{big}
	textCols := []bool{true}
	step, err := stepRow(vals, textCols, 1, 1, 256) // rc=1 >= effPage=1 → 预读
	if err != nil {
		t.Fatalf("readAhead stepRow error = %v", err)
	}
	if !step.readAhead {
		t.Fatal("expected readAhead at page boundary, got data row")
	}
	if step.nextRC != 2 {
		t.Fatalf("nextRC = %d, want 2", step.nextRC)
	}
	if _, ok := vals[0].([]byte); !ok {
		t.Fatal("readAhead row must not be text-normalized to string")
	}
}

// TestStepRowDataRowNormalizesAndAccounts 验证数据行：文本列规范化为 string、
// 二进制列防御性复制为 []byte、字节计数与行计数正确。
func TestStepRowDataRowNormalizesAndAccounts(t *testing.T) {
	t.Parallel()

	vals := []any{[]byte("hello"), []byte{0xDE, 0xAD}, nil}
	textCols := []bool{true, false, true}
	step, err := stepRow(vals, textCols, 0, 1, 1<<20)
	if err != nil {
		t.Fatalf("stepRow error = %v", err)
	}
	if step.readAhead {
		t.Fatal("data row must not be readAhead")
	}
	if s, ok := step.dataRow[0].(string); !ok || s != "hello" {
		t.Fatalf("data row text col: got %T %v, want string 'hello'", step.dataRow[0], step.dataRow[0])
	}
	if b, ok := step.dataRow[1].([]byte); !ok || !bytes.Equal(b, []byte{0xDE, 0xAD}) {
		t.Fatalf("data row binary col: got %T %v, want []byte preserved", step.dataRow[1], step.dataRow[1])
	}
	if step.dataRow[2] != nil {
		t.Fatalf("data row nil col: got %T %v, want nil", step.dataRow[2], step.dataRow[2])
	}
	if step.added != 7 { // text(5) + binary(2)
		t.Fatalf("added = %d, want 7", step.added)
	}
	if step.nextRC != 1 {
		t.Fatalf("nextRC = %d, want 1", step.nextRC)
	}
}

// TestStepRowOversizedCellRejected 验证数据行超限单元格在规范化转换前即被拒绝
// （ErrResultTooLarge，MaxCellBytes 语义不变）；预读行的超限单元格不报错，
// 只计数，由 finalizeResult 丢弃。
func TestStepRowOversizedCellRejected(t *testing.T) {
	t.Parallel()

	// 数据行（rc < effPage）：超限 → ErrResultTooLarge
	big := []byte(strings.Repeat("x", 300))
	_, err := stepRow([]any{big}, []bool{true}, 0, 1, 256)
	var adapterErr *AdapterError
	if !errors.As(err, &adapterErr) || adapterErr.Code != ErrResultTooLarge {
		t.Fatalf("data row oversized: error = %v, want %s", err, ErrResultTooLarge)
	}

	// 预读行（rc >= effPage）：超限单元格不报错，只计数
	step, err := stepRow([]any{big}, []bool{true}, 1, 1, 256)
	if err != nil {
		t.Fatalf("readAhead oversized must not error: %v", err)
	}
	if !step.readAhead {
		t.Fatal("expected readAhead for oversized lookahead row")
	}
}

// TestNormalizeTextCols 验证文本列规范化：文本 []byte→string、二进制/未知列保持
// []byte、NULL 保持 nil；超限单元格转换前即拒绝且值保持 []byte；maxCell=0 不设限。
func TestNormalizeTextCols(t *testing.T) {
	t.Parallel()

	vals := []any{[]byte("hello"), []byte{0xDE, 0xAD}, nil}
	textCols := []bool{true, false, true}
	if err := normalizeTextCols(vals, textCols, 1<<20); err != nil {
		t.Fatalf("normalizeTextCols error = %v", err)
	}
	if s, ok := vals[0].(string); !ok || s != "hello" {
		t.Fatalf("text col: got %T %v, want string 'hello'", vals[0], vals[0])
	}
	if b, ok := vals[1].([]byte); !ok || !bytes.Equal(b, []byte{0xDE, 0xAD}) {
		t.Fatalf("binary col: got %T %v, want []byte preserved", vals[1], vals[1])
	}
	if vals[2] != nil {
		t.Fatalf("nil col: got %T %v, want nil", vals[2], vals[2])
	}
}

func TestNormalizeTextColsCellLimit(t *testing.T) {
	t.Parallel()

	big := []byte(strings.Repeat("x", 300))
	vals := []any{big}
	err := normalizeTextCols(vals, []bool{true}, 256)
	var adapterErr *AdapterError
	if !errors.As(err, &adapterErr) || adapterErr.Code != ErrResultTooLarge {
		t.Fatalf("normalizeTextCols() error = %v, want %s", err, ErrResultTooLarge)
	}
	if _, ok := vals[0].([]byte); !ok {
		t.Fatal("oversized cell must not be converted to string before rejection")
	}
}

func TestNormalizeTextColsNoLimit(t *testing.T) {
	t.Parallel()

	big := []byte(strings.Repeat("x", 300))
	vals := []any{big}
	if err := normalizeTextCols(vals, []bool{true}, 0); err != nil {
		t.Fatalf("maxCell=0 must not reject: %v", err)
	}
	if s, ok := vals[0].(string); !ok || len(s) != 300 {
		t.Fatalf("maxCell=0: got %T len %d, want string", vals[0], len(s))
	}
}

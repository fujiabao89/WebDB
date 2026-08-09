package adapter

import (
	"errors"
	"testing"
)

// typeMatrixPreflightOutcome 描述类型矩阵预检结果的处理方式。
type typeMatrixPreflightOutcome int

const (
	preflightProceed        typeMatrixPreflightOutcome = iota // 表存在，继续
	preflightSkipEnvMissing                                   // 表不存在，环境未预置
	preflightFail                                             // 预检查询失败：权限/超时/连接/元数据错误
)

// preflightOutcome 由预检查询结果决定处理方式：查询成功且表存在 → proceed；
// 查询成功但表不存在 → skip（环境未预置，唯一允许的 Skip）；查询失败 → fail
// （连接、权限、超时或元数据错误都是真实失败，不得隐藏为绿色 CI）。
func preflightOutcome(err error, n int64) typeMatrixPreflightOutcome {
	if err != nil {
		return preflightFail
	}
	if n == 0 {
		return preflightSkipEnvMissing
	}
	return preflightProceed
}

// TestTypeMatrixPreflightOutcome 是预检错误处理的聚焦回归证据：
// 预检查询失败必须判为 fail（t.Fatalf 路径），仅表不存在判为 skip。
func TestTypeMatrixPreflightOutcome(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		n    int64
		want typeMatrixPreflightOutcome
	}{
		{"表存在", nil, 2, preflightProceed},
		{"表缺失-环境未预置", nil, 0, preflightSkipEnvMissing},
		{"预检查询失败", errors.New("permission denied"), 0, preflightFail},
		{"预检查询失败但计数非零", errors.New("timeout"), 2, preflightFail},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := preflightOutcome(c.err, c.n); got != c.want {
				t.Fatalf("preflightOutcome(%v, %d) = %d, want %d", c.err, c.n, got, c.want)
			}
		})
	}
}

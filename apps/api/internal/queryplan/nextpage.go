package queryplan

import (
	"fmt"
	"reflect"
)

// nextPagePlanVersion 是 VerifiedNextPagePlan 内部版本号。
const nextPagePlanVersion = 1

// VerifiedNextPagePlan 是 Adapter 续页输入的 sealed interface（ADR-015）。
// 字段不导出、无 raw constructor；只能由成功 claim + 重新授权后的服务层通过
// NewVerifiedNextPagePlan 生成。零值/伪造实现无效。
type VerifiedNextPagePlan interface {
	isVerifiedNextPagePlan()
	Valid() bool
	Version() int
	// SortSpecs 返回排序规格深拷贝。
	SortSpecs() []SortSpec
	// LastSortValues 返回 last sort values 深拷贝。
	LastSortValues() []any
	// SQL 返回原始 SQL（服务端从 ContinuationState 恢复，不来自客户端 token）。
	SQL() string
	// Args 返回参数深拷贝。
	Args() []any
	PageSize() int
	MaxRows() int
	CumulativeCount() int
}

// verifiedNextPagePlan 具体实现不导出。
type verifiedNextPagePlan struct {
	version         int
	valid           bool
	specs           []SortSpec
	lastSortValues  []any
	sql             string
	args            []any
	pageSize        int
	maxRows         int
	cumulativeCount int
}

func (p *verifiedNextPagePlan) isVerifiedNextPagePlan() {}

func (p *verifiedNextPagePlan) Valid() bool {
	return p != nil && p.valid && p.version == nextPagePlanVersion
}

func (p *verifiedNextPagePlan) Version() int {
	if p == nil {
		return 0
	}
	return p.version
}

func (p *verifiedNextPagePlan) SortSpecs() []SortSpec {
	if p == nil {
		return nil
	}
	out := make([]SortSpec, len(p.specs))
	copy(out, p.specs)
	return out
}

func (p *verifiedNextPagePlan) LastSortValues() []any {
	if p == nil {
		return nil
	}
	return deepCopySlice(p.lastSortValues)
}

func (p *verifiedNextPagePlan) SQL() string {
	if p == nil {
		return ""
	}
	return p.sql
}

func (p *verifiedNextPagePlan) Args() []any {
	if p == nil {
		return nil
	}
	return deepCopySlice(p.args)
}

func (p *verifiedNextPagePlan) PageSize() int {
	if p == nil {
		return 0
	}
	return p.pageSize
}

func (p *verifiedNextPagePlan) MaxRows() int {
	if p == nil {
		return 0
	}
	return p.maxRows
}

func (p *verifiedNextPagePlan) CumulativeCount() int {
	if p == nil {
		return 0
	}
	return p.cumulativeCount
}

// IsValidVerifiedNextPagePlan 对 nil / typed-nil / 无效 version 做 fail-closed 判定。
func IsValidVerifiedNextPagePlan(p VerifiedNextPagePlan) bool {
	if p == nil {
		return false
	}
	rv := reflect.ValueOf(p)
	if rv.Kind() == reflect.Pointer && rv.IsNil() {
		return false
	}
	return p.Valid()
}

// NewVerifiedNextPagePlan 是 VerifiedNextPagePlan 的唯一合法创建入口。
// 由成功 claim 和重新授权后的 Service 内部流程调用。
//
// 校验：原始 VerifiedSortPlan 有效、last sort values 数量与排序计划匹配、
// PageSize/MaxRows/CumulativeCount 边界有效；任一不满足则拒绝（旧 token 保持失效）。
func NewVerifiedNextPagePlan(
	sortPlan VerifiedSortPlan,
	lastSortValues []any,
	sql string,
	args []any,
	pageSize, maxRows, cumulativeCount int,
) (VerifiedNextPagePlan, error) {
	if !IsValidVerifiedPlan(sortPlan) {
		return nil, fmt.Errorf("verified next page plan: invalid sort plan")
	}
	specs := sortPlan.SortSpecs()
	if len(specs) == 0 {
		return nil, fmt.Errorf("verified next page plan: empty sort specs")
	}
	if sql == "" {
		return nil, fmt.Errorf("verified next page plan: empty sql")
	}
	if pageSize <= 0 {
		return nil, fmt.Errorf("verified next page plan: invalid page size %d", pageSize)
	}
	if maxRows <= 0 {
		return nil, fmt.Errorf("verified next page plan: invalid max rows %d", maxRows)
	}
	if cumulativeCount < 0 || cumulativeCount >= maxRows {
		return nil, fmt.Errorf("verified next page plan: invalid cumulative count %d (must be in [0, maxRows))", cumulativeCount)
	}
	if len(lastSortValues) != len(specs)*2 {
		return nil, fmt.Errorf("verified next page plan: last sort values count %d does not match %d sort columns",
			len(lastSortValues), len(specs)*2)
	}
	return &verifiedNextPagePlan{
		version:         nextPagePlanVersion,
		valid:           true,
		specs:           specs,
		lastSortValues:  deepCopySlice(lastSortValues),
		sql:             sql,
		args:            deepCopySlice(args),
		pageSize:        pageSize,
		maxRows:         maxRows,
		cumulativeCount: cumulativeCount,
	}, nil
}

// deepCopySlice 递归深拷贝 []any。
func deepCopySlice(in []any) []any {
	if in == nil {
		return nil
	}
	out := make([]any, len(in))
	for i, v := range in {
		out[i] = deepCopyValue(v)
	}
	return out
}

// deepCopyValue 递归深拷贝任意 slice/map（含非 string 键 map：键与值均深拷贝）。
func deepCopyValue(v any) any {
	if v == nil {
		return nil
	}
	return deepCopyReflectValue(reflect.ValueOf(v)).Interface()
}

// deepCopyReflectValue 递归深拷贝 reflect.Value（slice/map/interface 全类型）。
func deepCopyReflectValue(rv reflect.Value) reflect.Value {
	if !rv.IsValid() {
		return rv
	}
	switch rv.Kind() {
	case reflect.Interface:
		if rv.IsNil() {
			return rv
		}
		return deepCopyReflectValue(rv.Elem())
	case reflect.Slice:
		cp := reflect.MakeSlice(rv.Type(), rv.Len(), rv.Len())
		for j := 0; j < rv.Len(); j++ {
			cp.Index(j).Set(deepCopyReflectValue(rv.Index(j)))
		}
		return cp
	case reflect.Map:
		m := reflect.MakeMapWithSize(rv.Type(), rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			m.SetMapIndex(deepCopyReflectValue(iter.Key()), deepCopyReflectValue(iter.Value()))
		}
		return m
	default:
		return rv // 标量/时间等不可变类型
	}
}

// Package pagination 提供 Service-owned 的 continuation registry（ADR-015）。
//
// 该包是 token/registry 的唯一 Owner：Adapter 不生成、不解析、不保存客户端 token。
// Registry 只保存 opaque handle 的 SHA-256 digest；客户端仅持有 32 字节以上
// CSPRNG handle。token 不进入日志、审计正文、trace attribute 或 metric label。
package pagination

import (
	"reflect"
	"time"

	"github.com/fujiabao89/webdb/internal/queryplan"
)

// ContinuationState 是 Registry 保存的完整续页状态（ADR-015 §4）。
// 除 ExpiresAt 由 Registry 在 Create/Rotate 时按 TTL 设定外，其余字段由服务层填充。
type ContinuationState struct {
	UserID           string
	WorkspaceID      string
	ConnectionID     string
	PoolGeneration   int64
	SchemaGeneration string
	TableSchema      string // 表 lineage（schema generation 重新校验所需）
	TableName        string
	PolicyVersion    int64
	StatementHash    string
	SortPlan         queryplan.VerifiedSortPlan
	SQL              string
	Args             []any // 深拷贝后存储
	LastSortValues   []any // 深拷贝后存储
	CumulativeCount  int
	PageSize         int
	MaxRows          int
	TimeoutMs        int
	ExpiresAt        time.Time // 绝对过期时间，由 Registry 设定（TTL 自创建起算，非 idle）
}

// deepCopy 递归深拷贝 ContinuationState，Args/LastSortValues 使用递归深拷贝。
func (s *ContinuationState) deepCopy() *ContinuationState {
	if s == nil {
		return nil
	}
	cp := *s
	cp.Args = deepCopyAny(s.Args).([]any)
	cp.LastSortValues = deepCopyAny(s.LastSortValues).([]any)
	return &cp
}

// deepCopyAny 递归深拷贝任意 slice/map（含非 string 键 map：键与值均深拷贝）。
// 标量/时间等不可变类型直接复用。
func deepCopyAny(v any) any {
	if v == nil {
		return nil
	}
	return deepCopyReflect(reflect.ValueOf(v)).Interface()
}

// deepCopyReflect 递归深拷贝 reflect.Value（slice/map/interface 全类型）。
func deepCopyReflect(v reflect.Value) reflect.Value {
	if !v.IsValid() {
		return v
	}
	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			return v
		}
		return deepCopyReflect(v.Elem())
	case reflect.Slice:
		cp := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for j := 0; j < v.Len(); j++ {
			cp.Index(j).Set(deepCopyReflect(v.Index(j)))
		}
		return cp
	case reflect.Map:
		m := reflect.MakeMapWithSize(v.Type(), v.Len())
		iter := v.MapRange()
		for iter.Next() {
			// 键与值均深拷贝（含 map[int]any 等非 string 键）
			m.SetMapIndex(deepCopyReflect(iter.Key()), deepCopyReflect(iter.Value()))
		}
		return m
	default:
		return v // 标量/时间等不可变类型
	}
}

// retainedBytes 递归测量深拷贝后实际保留的字节数（用于字节配额计费）。
func retainedBytes(v any) int64 {
	switch t := v.(type) {
	case nil:
		return 0
	case bool:
		return 1
	case string:
		return int64(len(t))
	case []byte:
		return int64(len(t))
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return 8
	case time.Time:
		return 32
	default:
		rv := reflect.ValueOf(v)
		switch rv.Kind() {
		case reflect.Slice:
			var n int64 = 16
			for j := 0; j < rv.Len(); j++ {
				n += retainedBytes(rv.Index(j).Interface())
			}
			return n
		case reflect.Array:
			// 数组按元素递归计费（如 [4096]byte），避免仅按反射显示字符串记账造成配额绕过
			// （Codex 审查：非 string 键 map 曾以 64 个 [4096]byte 键只计 2490 字节）。
			var n int64 = 16
			for j := 0; j < rv.Len(); j++ {
				n += retainedBytes(rv.Index(j).Interface())
			}
			return n
		case reflect.Map:
			var n int64 = 32
			iter := rv.MapRange()
			for iter.Next() {
				// 键与值均按具体类型递归计费（非反射显示字符串），数组/标量走保守估计。
				n += 8 + retainedBytes(iter.Key().Interface()) + retainedBytes(iter.Value().Interface())
			}
			return n
		default:
			// 未知类型按 64 字节保守下限计费（fail-closed 低估计；可安全覆盖的数组/映射已递归）。
			return 64
		}
	}
}

// stateBytes 计算 ContinuationState 深拷贝后的 retained bytes。
// SortPlan 的 retained SortSpecs 计入字节配额（Codex P2）：每个 continuation 保留
// 自己的 VerifiedSortPlan，调用方可提交大量排序列绕过 per-state 字节配额；
// SQL/Args/LastSortValues 计入。
func stateBytes(s *ContinuationState) int64 {
	if s == nil {
		return 0
	}
	n := int64(len(s.SQL))
	n += int64(len(s.UserID)) + int64(len(s.WorkspaceID)) + int64(len(s.ConnectionID)) +
		int64(len(s.SchemaGeneration)) + int64(len(s.TableSchema)) + int64(len(s.TableName)) +
		int64(len(s.StatementHash))
	for _, a := range s.Args {
		n += retainedBytes(a)
	}
	for _, l := range s.LastSortValues {
		n += retainedBytes(l)
	}
	if s.SortPlan != nil {
		for _, spec := range s.SortPlan.SortSpecs() {
			n += int64(len(spec.Column)) + int64(len(spec.BaseColumn)) + 2 // 2 个 bool 字段
		}
	}
	return n
}

package queryplan

import (
	"fmt"
	"reflect"
)

// sortPlanVersion 是 VerifiedSortPlan 内部版本号。Adapter/服务层在执行前必须
// 校验该版本，防止未来兼容性变化被静默接受（ADR-014 invariant 校验）。
const sortPlanVersion = 1

// SortSpec 排序规格（暴露名用于 SQL/结果列定位；基础列用于唯一性证明）。
type SortSpec struct {
	Column     string // 结果列暴露名（Adapter 排序/取 last values 使用）
	BaseColumn string // 基础表列名（唯一性证明使用）
	Asc        bool
	NullsLast  bool
}

// SnapshotBinding 计划绑定的可信 SchemaSnapshot 上下文。
type SnapshotBinding struct {
	ConnectionID     string
	Dialect          Dialect
	PoolGeneration   int64
	SchemaGeneration string
}

// VerifiedSortPlan 是 exported sealed interface（ADR-014）。
// 包外无法实现该接口；唯一合法创建入口是 VerifySortPlan。
type VerifiedSortPlan interface {
	isVerifiedSortPlan()
	Valid() bool
	Version() int
	// SortSpecs 返回排序规格深拷贝；调用方修改不影响内部状态。
	SortSpecs() []SortSpec
	// SnapshotBinding 返回计划绑定的快照上下文。
	SnapshotBinding() SnapshotBinding
}

// verifiedSortPlan 具体实现不导出，字段全部私有。
type verifiedSortPlan struct {
	version int
	valid   bool
	specs   []SortSpec
	binding SnapshotBinding
}

func (p *verifiedSortPlan) isVerifiedSortPlan() {}

func (p *verifiedSortPlan) Valid() bool {
	return p != nil && p.valid && p.version == sortPlanVersion
}

func (p *verifiedSortPlan) Version() int {
	if p == nil {
		return 0
	}
	return p.version
}

func (p *verifiedSortPlan) SortSpecs() []SortSpec {
	if p == nil {
		return nil
	}
	out := make([]SortSpec, len(p.specs))
	copy(out, p.specs)
	return out
}

func (p *verifiedSortPlan) SnapshotBinding() SnapshotBinding {
	if p == nil {
		return SnapshotBinding{}
	}
	return p.binding
}

// IsValidVerifiedPlan 对 nil / typed-nil / 无效 version 做 fail-closed 判定，
// 供 Adapter 在执行前使用（ADR-014：interface 非 nil、typed-nil 检查、Valid、
// 内部 version/seal/invariants）。
func IsValidVerifiedPlan(p VerifiedSortPlan) bool {
	if p == nil {
		return false
	}
	rv := reflect.ValueOf(p)
	if rv.Kind() == reflect.Ptr && rv.IsNil() {
		return false
	}
	return p.Valid()
}

// VerifySortPlan 是 VerifiedSortPlan 的唯一合法创建入口（ADR-014）。
//
// 输入：
//   - snapshot：可信 SchemaSnapshot（绑定 connection/dialect/pool/schema generation）
//   - shape：保守提取的查询形状（单一基础表、可追溯列）
//   - sortKeys：客户端排序意图（不含唯一性声明）
//
// 校验：nil/typed-nil/无效 snapshot 或 shape、形状与快照表不一致、排序列不可追溯
// 到基础列、重复列、无效方向、唯一性无法证明（部分复合唯一列 / nullable unique /
// 无唯一键）均 fail-closed 拒绝。客户端提交的任何"唯一性标记"在此被忽略。
func VerifySortPlan(snapshot *SchemaSnapshot, shape *QueryShape, sortKeys []SortKey) (VerifiedSortPlan, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("verified sort plan: nil schema snapshot")
	}
	if err := snapshot.Validate(); err != nil {
		return nil, fmt.Errorf("verified sort plan: %w", err)
	}
	if shape == nil {
		return nil, fmt.Errorf("verified sort plan: nil query shape")
	}
	if err := shape.Validate(); err != nil {
		return nil, fmt.Errorf("verified sort plan: %w", err)
	}
	if shape.BaseSchema != snapshot.SchemaName() || shape.BaseTable != snapshot.TableName() {
		return nil, fmt.Errorf("verified sort plan: shape table %s.%s does not match snapshot %s.%s",
			shape.BaseSchema, shape.BaseTable, snapshot.SchemaName(), snapshot.TableName())
	}
	if len(sortKeys) == 0 {
		return nil, fmt.Errorf("verified sort plan: empty sort keys")
	}

	specs := make([]SortSpec, 0, len(sortKeys))
	baseCols := make([]string, 0, len(sortKeys))
	seen := make(map[string]bool, len(sortKeys))
	for _, k := range sortKeys {
		if err := validateIdent(k.Column); err != nil {
			return nil, fmt.Errorf("verified sort plan: sort column %q: %w", k.Column, err)
		}
		if k.Direction != SortAsc && k.Direction != SortDesc {
			return nil, fmt.Errorf("verified sort plan: invalid sort direction %q", k.Direction)
		}
		if seen[k.Column] {
			return nil, fmt.Errorf("verified sort plan: duplicate sort column %q", k.Column)
		}
		seen[k.Column] = true

		base := k.Column
		if !shape.SelectStar {
			var ok bool
			base, ok = shape.Columns[k.Column]
			if !ok {
				return nil, fmt.Errorf("verified sort plan: sort column %q not exposed in result", k.Column)
			}
		}
		if base == "" {
			return nil, fmt.Errorf("verified sort plan: sort column %q is a computed expression, not a base column", k.Column)
		}
		if !columnExists(snapshot, base) {
			return nil, fmt.Errorf("verified sort plan: sort column %q not a base column of table", k.Column)
		}
		specs = append(specs, SortSpec{Column: k.Column, BaseColumn: base, Asc: k.Direction != SortDesc, NullsLast: k.NullsLast})
		baseCols = append(baseCols, base)
	}

	proof := findUniqueProof(snapshot.TableMetadataCopy())
	if proof == nil {
		return nil, fmt.Errorf("verified sort plan: no unique key (complete primary key or all-NOT-NULL unique constraint) available")
	}
	for _, pc := range proof.columns {
		found := false
		for _, bc := range baseCols {
			if bc == pc {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("verified sort plan: sort columns do not fully cover unique key (%s)", stringify(proof.columns))
		}
	}

	return &verifiedSortPlan{
		version: sortPlanVersion,
		valid:   true,
		specs:   specs,
		binding: SnapshotBinding{
			ConnectionID:     snapshot.ConnectionID,
			Dialect:          snapshot.Dialect,
			PoolGeneration:   snapshot.PoolGeneration,
			SchemaGeneration: snapshot.SchemaGeneration,
		},
	}, nil
}

func columnExists(snapshot *SchemaSnapshot, name string) bool {
	for _, c := range snapshot.Columns() {
		if c.Name == name {
			return true
		}
	}
	return false
}

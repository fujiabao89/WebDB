package queryplan

import "fmt"

// QueryShape 保守提取的可分页查询形状（ADR-014 单一基础表、可追溯列）。
//
// 由服务层从可信 AST 提取（sqlpolicy.AnalyzeShape），queryplan 不解析 SQL。
// 该结构只描述可安全分页的查询；join/聚合/分组/DISTINCT/CTE/集合操作/
// 子查询 FROM/内层 LIMIT 等形状不产生 QueryShape（AnalyzeShape 返回错误）。
type QueryShape struct {
	BaseSchema string
	BaseTable  string
	// SelectStar 表示目标列表为 *（所有基础列均暴露）。
	SelectStar bool
	// Columns 暴露名 → 基础列名。表达式/计算列不收录（值缺省或为空串）。
	Columns map[string]string
}

// Validate 对 QueryShape 做 fail-closed 校验。
// BaseSchema 允许为空：SQL 中未限定表名时由服务层解析为连接默认 schema/database。
func (s *QueryShape) Validate() error {
	if s == nil {
		return fmt.Errorf("query shape is nil")
	}
	if s.BaseSchema != "" {
		if err := validateIdent(s.BaseSchema); err != nil {
			return fmt.Errorf("query shape: base schema: %w", err)
		}
	}
	if err := validateIdent(s.BaseTable); err != nil {
		return fmt.Errorf("query shape: base table: %w", err)
	}
	if s.SelectStar {
		return nil
	}
	if len(s.Columns) == 0 {
		return fmt.Errorf("query shape: no exposed columns")
	}
	for exposed, base := range s.Columns {
		if err := validateIdent(exposed); err != nil {
			return fmt.Errorf("query shape: exposed column %q: %w", exposed, err)
		}
		if base != "" {
			if err := validateIdent(base); err != nil {
				return fmt.Errorf("query shape: base column %q: %w", base, err)
			}
		}
	}
	return nil
}

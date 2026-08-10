package sqlpolicy

import (
	"fmt"
	"unicode"
)

// HasUnboundPlaceholder 检测 SQL 中未绑定的原生位置占位符（P0-06A §8.1）。
//
// token 级判定（非裸子串扫描）：
//   - PostgreSQL：`$` + 数字（`$N`）为位置占位符 → 命中；JSONB 操作符 `?`/`?|`/`?&`、
//     `::`、`@>`/`<@` 以及字符串/注释/美元引号内的符号一律放行。
//   - MySQL：可执行位置的 `?` 为位置占位符 → 命中；`#` 行注释、反斜杠转义、字符串/
//     注释/反引号标识符内放行。
//
// fail-closed（CodeRabbit #21）：未知方言或任何词法不确定状态（未闭合字符串/注释/
// 美元引号/标识符）返回 error，调用方必须拒绝，不得静默按"无占位符"放行。
// 返回 true 表示应拒绝（statement_not_allowed，422）。
func HasUnboundPlaceholder(dialect Dialect, sql string) (bool, error) {
	switch dialect {
	case DialectPostgreSQL:
		return hasPGPlaceholder(sql)
	case DialectMySQL:
		return hasMySQLPlaceholder(sql)
	default:
		return false, fmt.Errorf("unknown dialect %q: cannot classify placeholders", dialect)
	}
}

func hasPGPlaceholder(sql string) (bool, error) {
	return scanPlaceholder(sql, false, false, func(sql string, i int) bool {
		if i+1 >= len(sql) {
			return false
		}
		n := sql[i+1]
		return n >= '0' && n <= '9'
	})
}

func hasMySQLPlaceholder(sql string) (bool, error) {
	return scanPlaceholder(sql, true, true, func(sql string, i int) bool {
		return sql[i] == '?'
	})
}

// scanPlaceholder 词法扫描 SQL，跳过字符串/注释/引号标识符/美元引号，
// 在可执行位置调用 isPlaceholder 判定。hashComment 仅 MySQL 启用（`#` 行注释；
// PG 中 `#` 是 JSONB 操作符字符 `#>`/`#>>`，不得按注释跳过，否则会吞掉其后的
// 占位符）；backslashEscapes 仅 MySQL 启用（MySQL 默认 `\` 是字符串转义；
// PG 普通字符串用 `'` 转义，E 字符串才转义——PG 保守按无转义处理，见注释）。
// 任何未闭合的词法结构（字符串/块注释/引号标识符/美元引号）返回 error
// （fail-closed，CodeRabbit #21），不得静默按"无占位符"放行。
func scanPlaceholder(sql string, hashComment, backslashEscapes bool, isPlaceholder func(sql string, i int) bool) (bool, error) {
	for i := 0; i < len(sql); {
		c := sql[i]
		switch {
		case c == '\'':
			next, closed := skipSingleQuoted(sql, i, backslashEscapes)
			if !closed {
				return false, fmt.Errorf("unclosed single-quoted string")
			}
			i = next
		case c == '"':
			next, closed := skipDoubleQuoted(sql, i)
			if !closed {
				return false, fmt.Errorf("unclosed double-quoted identifier")
			}
			i = next
		case c == '`':
			next, closed := skipBacktick(sql, i)
			if !closed {
				return false, fmt.Errorf("unclosed backtick identifier")
			}
			i = next
		case c == '-' && i+1 < len(sql) && sql[i+1] == '-':
			i = skipLineComment(sql, i)
		case c == '#' && hashComment:
			i = skipLineComment(sql, i)
		case c == '/' && i+1 < len(sql) && sql[i+1] == '*':
			next, closed := skipBlockComment(sql, i)
			if !closed {
				return false, fmt.Errorf("unclosed block comment")
			}
			i = next
		case c == '$':
			// 先判定占位符（$N），再判定美元引号（$tag$/$$）。
			if isPlaceholder(sql, i) {
				return true, nil
			}
			if isDollarQuoteStart(sql, i) {
				next, closed := skipDollarQuoted(sql, i)
				if !closed {
					return false, fmt.Errorf("unclosed dollar-quoted string")
				}
				i = next
				continue
			}
			i++
		case c == '?':
			if isPlaceholder(sql, i) {
				return true, nil
			}
			i++
		default:
			i++
		}
	}
	return false, nil
}

// isDollarQuoteStart 判断 $ 是否为美元引号开始（$tag$ 或 $$）。
func isDollarQuoteStart(sql string, i int) bool {
	if i+1 >= len(sql) {
		return false
	}
	if sql[i+1] == '$' {
		return true
	}
	r := rune(sql[i+1])
	return unicode.IsLetter(r) || r == '_'
}

// skipDollarQuoted 跳过 $tag$...$tag$（tag 可为空，即 $$...$$）。
// 返回 (结束下标, 是否闭合)；未找到闭合 $tag$ 时返回 (len(sql), false)（fail-closed）。
func skipDollarQuoted(sql string, i int) (int, bool) {
	j := i + 1
	for j < len(sql) && sql[j] != '$' {
		j++
	}
	if j >= len(sql) {
		return len(sql), false // 无闭合 $ → 未闭合
	}
	tag := sql[i+1 : j]
	search := j + 1
	for {
		k := search
		for k < len(sql) && sql[k] != '$' {
			k++
		}
		if k >= len(sql) {
			return len(sql), false // 未找到闭合 $tag$
		}
		// 检查 k 处是否构成闭合 $[tag]$：sql[k]=='$'，后接 tag，再接 '$'。
		if k+1+len(tag) < len(sql) &&
			sql[k+1:k+1+len(tag)] == tag &&
			sql[k+1+len(tag)] == '$' {
			return k + len(tag) + 2, true
		}
		// 不是闭合标记（可能是 tag 内容或其他 $），继续向后搜索。
		search = k + 1
	}
}

// skipSingleQuoted 跳过 '...' 字符串（含 ” 转义；backslashEscapes 时含 \x 转义，
// MySQL 默认 \' 转义使字符串不提前结束，避免误判字符串内 ? 为占位符，P3-2 修复）。
// 返回 (结束下标, 是否闭合)；未闭合时返回 (len(sql), false)（fail-closed）。
func skipSingleQuoted(sql string, i int, backslashEscapes bool) (int, bool) {
	i++
	for i < len(sql) {
		if sql[i] == '\\' && backslashEscapes && i+1 < len(sql) {
			i += 2
			continue
		}
		if sql[i] == '\'' {
			if i+1 < len(sql) && sql[i+1] == '\'' {
				i += 2
				continue
			}
			return i + 1, true
		}
		i++
	}
	return i, false
}

// skipDoubleQuoted 跳过 "..."（PG 引号标识符，含 "" 转义）。
// 返回 (结束下标, 是否闭合)；未闭合时返回 (len(sql), false)。
func skipDoubleQuoted(sql string, i int) (int, bool) {
	i++
	for i < len(sql) {
		if sql[i] == '"' {
			if i+1 < len(sql) && sql[i+1] == '"' {
				i += 2
				continue
			}
			return i + 1, true
		}
		i++
	}
	return i, false
}

// skipBacktick 跳过 `...`（MySQL 引号标识符，含 “ 转义）。
// 返回 (结束下标, 是否闭合)；未闭合时返回 (len(sql), false)。
func skipBacktick(sql string, i int) (int, bool) {
	i++
	for i < len(sql) {
		if sql[i] == '`' {
			if i+1 < len(sql) && sql[i+1] == '`' {
				i += 2
				continue
			}
			return i + 1, true
		}
		i++
	}
	return i, false
}

// skipLineComment 跳过 -- 或 # 单行注释到行尾。行注释在行尾/EOF 自然闭合，恒为 closed。
func skipLineComment(sql string, i int) int {
	for i < len(sql) && sql[i] != '\n' {
		i++
	}
	return i
}

// skipBlockComment 跳过 /* ... */ 块注释。
// 返回 (结束下标, 是否闭合)；未闭合时返回 (len(sql), false)（fail-closed）。
func skipBlockComment(sql string, i int) (int, bool) {
	i += 2
	for i+1 < len(sql) {
		if sql[i] == '*' && sql[i+1] == '/' {
			return i + 2, true
		}
		i++
	}
	return len(sql), false
}

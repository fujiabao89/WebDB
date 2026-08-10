package sqlpolicy

import "unicode"

// HasUnboundPlaceholder 检测 SQL 中未绑定的原生位置占位符（P0-06A §8.1）。
//
// token 级判定（非裸子串扫描）：
//   - PostgreSQL：`$` + 数字（`$N`）为位置占位符 → 命中；JSONB 操作符 `?`/`?|`/`?&`、
//     `::`、`@>`/`<@` 以及字符串/注释/美元引号内的符号一律放行。
//   - MySQL：可执行位置的 `?` 为位置占位符 → 命中；字符串/注释/反引号标识符内放行。
//
// 返回 true 表示应拒绝（statement_not_allowed，422）。
func HasUnboundPlaceholder(dialect Dialect, sql string) bool {
	switch dialect {
	case DialectPostgreSQL:
		return hasPGPlaceholder(sql)
	case DialectMySQL:
		return hasMySQLPlaceholder(sql)
	default:
		return false
	}
}

func hasPGPlaceholder(sql string) bool {
	return scanPlaceholder(sql, false, func(sql string, i int) bool {
		if i+1 >= len(sql) {
			return false
		}
		n := sql[i+1]
		return n >= '0' && n <= '9'
	})
}

func hasMySQLPlaceholder(sql string) bool {
	return scanPlaceholder(sql, true, func(sql string, i int) bool {
		return sql[i] == '?'
	})
}

// scanPlaceholder 词法扫描 SQL，跳过字符串/注释/引号标识符/美元引号，
// 在可执行位置调用 isPlaceholder 判定。backslashEscapes 仅 MySQL 启用
// （MySQL 默认 \ 是字符串转义；PG 普通字符串用 ”，E” 才转义——PG 保守按无转义处理）。
func scanPlaceholder(sql string, backslashEscapes bool, isPlaceholder func(sql string, i int) bool) bool {
	for i := 0; i < len(sql); {
		c := sql[i]
		switch {
		case c == '\'':
			i = skipSingleQuoted(sql, i, backslashEscapes)
		case c == '"':
			i = skipDoubleQuoted(sql, i)
		case c == '`':
			i = skipBacktick(sql, i)
		case c == '-' && i+1 < len(sql) && sql[i+1] == '-':
			i = skipLineComment(sql, i)
		case c == '#':
			i = skipLineComment(sql, i)
		case c == '/' && i+1 < len(sql) && sql[i+1] == '*':
			i = skipBlockComment(sql, i)
		case c == '$':
			// 先判定占位符（$N），再判定美元引号（$tag$/$$）。
			if isPlaceholder(sql, i) {
				return true
			}
			if isDollarQuoteStart(sql, i) {
				i = skipDollarQuoted(sql, i)
				continue
			}
			i++
		case c == '?':
			if isPlaceholder(sql, i) {
				return true
			}
			i++
		default:
			i++
		}
	}
	return false
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

// skipDollarQuoted 跳过 $tag$...$tag$（tag 可为空）。
func skipDollarQuoted(sql string, i int) int {
	j := i + 1
	for j < len(sql) && sql[j] != '$' {
		j++
	}
	if j >= len(sql) {
		return len(sql)
	}
	tag := sql[i+1 : j]
	end := j + 1
	for {
		k := end
		for k < len(sql) && sql[k] != '$' {
			k++
		}
		if k >= len(sql) {
			return len(sql)
		}
		if sql[i+1:k] == tag {
			return k + 1
		}
		end = k + 1
	}
}

// skipSingleQuoted 跳过 '...' 字符串（含 ” 转义；backslashEscapes 时含 \x 转义，
// MySQL 默认 \' 转义使字符串不提前结束，避免误判字符串内 ? 为占位符，P3-2 修复）。
func skipSingleQuoted(sql string, i int, backslashEscapes bool) int {
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
			return i + 1
		}
		i++
	}
	return i
}

// skipDoubleQuoted 跳过 "..."（PG 引号标识符，含 "" 转义）。
func skipDoubleQuoted(sql string, i int) int {
	i++
	for i < len(sql) {
		if sql[i] == '"' {
			if i+1 < len(sql) && sql[i+1] == '"' {
				i += 2
				continue
			}
			return i + 1
		}
		i++
	}
	return i
}

// skipBacktick 跳过 `...`（MySQL 引号标识符，含 “ 转义）。
func skipBacktick(sql string, i int) int {
	i++
	for i < len(sql) {
		if sql[i] == '`' {
			if i+1 < len(sql) && sql[i+1] == '`' {
				i += 2
				continue
			}
			return i + 1
		}
		i++
	}
	return i
}

// skipLineComment 跳过 -- 或 # 单行注释到行尾。
func skipLineComment(sql string, i int) int {
	for i < len(sql) && sql[i] != '\n' {
		i++
	}
	return i
}

// skipBlockComment 跳过 /* ... */ 块注释。
func skipBlockComment(sql string, i int) int {
	i += 2
	for i+1 < len(sql) {
		if sql[i] == '*' && sql[i+1] == '/' {
			return i + 2
		}
		i++
	}
	return len(sql)
}

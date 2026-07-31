package sqlpolicy

import "fmt"

// ecmLexer WebDB 自有 MySQL ECM（可执行注释）词法分析器。
//
// 使用确定性单趟词法状态机识别 /*!...*/ 可执行注释。
// 时间复杂度 O(n)，空间复杂度 O(1)，发现首个 ECM 后立即停止。
//
// ADR-007: MySQL 使用两层安全边界 —— WebDB ECM lexer 前置识别，之后才调用 Omni AST。
type ecmLexer struct {
	input []byte
	pos   int
	mode  MySQLLexerMode
}

type lexerState int

const (
	stateNormal       lexerState = iota // 正常 SQL
	stateSingleQuote                    // '...'
	stateDoubleQuote                    // "..."（仅 ANSI_QUOTES 模式下视为标识符）
	stateBacktick                       // `...`
	stateBlockComment                   // /* ... */
	stateLineComment                    // -- ... 或 # ...
	stateSlash                          // 刚看到 /
	stateMaybeBlock                     // 刚看到 /* — 还在同一字符，需要确认下一个字符
)

func newECMLexer(input string, mode MySQLLexerMode) *ecmLexer {
	return &ecmLexer{input: []byte(input), pos: 0, mode: mode}
}

// scan 扫描输入，返回是否检测到可执行注释。
// hasECM=true 或 err!=nil 时均必须拒绝 SQL。
func (l *ecmLexer) scan() (bool, error) {
	state := stateNormal
	n := len(l.input)

	for l.pos < n {
		ch := l.input[l.pos]

		switch state {
		case stateNormal:
			switch {
			case ch == '\'':
				state = stateSingleQuote
			case ch == '"':
				if l.mode.ANSIQuotes {
					state = stateDoubleQuote
				}
				// 非 ANSI_QUOTES 模式下 " 在 SQL 中是字符串分隔符，但在 MySQL 默认模式下也是标识符引用符
				// 保守处理：在非 ANSI_QUOTES 模式下将其视为字符串分隔符
				state = stateDoubleQuote
			case ch == '`':
				state = stateBacktick
			case ch == '#':
				state = stateLineComment
			case ch == '-':
				// 检查是否 -- 行注释（需要后面紧跟空白/控制字符或 MySQL 行注释字符）
				if l.pos+1 < n && l.input[l.pos+1] == '-' {
					// -- 后面需要是空格、tab、换行等 MySQL 认可的空白/控制字符
					if l.pos+2 >= n || isMySQLCommentWhitespace(l.input[l.pos+2]) {
						state = stateLineComment
						l.pos++ // 跳过第二个 '-'
					}
				}
			case ch == '/':
				if l.pos+1 < n && l.input[l.pos+1] == '*' {
					// 检查是否 /*! 或 /*+
					if l.pos+2 < n {
						next := l.input[l.pos+2]
						if next == '!' {
							// 可执行注释！无条件检测
							return true, nil
						} else if next == '+' {
							// optimizer hint — 安全的块注释变体
							l.pos += 2 // 跳过 /*
							state = stateBlockComment
							continue
						}
					}
					// 普通 /* ... */ 块注释
					l.pos++ // 跳过 *
					state = stateBlockComment
				}
			}

		case stateSingleQuote:
			if ch == '\'' {
				// 检查是否转义引号 ''
				if l.pos+1 < n && l.input[l.pos+1] == '\'' {
					l.pos++ // 跳过转义引号
				} else {
					state = stateNormal
				}
			} else if ch == '\\' && !l.mode.NoBackslashEscapes {
				// 反斜杠转义：跳过下一个字符（不判断其内容，仅作为转义消费）
				if l.pos+1 < n {
					l.pos++
				}
			}

		case stateDoubleQuote:
			if ch == '"' {
				if l.pos+1 < n && l.input[l.pos+1] == '"' {
					l.pos++ // 跳过转义引号 ""
				} else {
					state = stateNormal
				}
			} else if ch == '\\' && !l.mode.NoBackslashEscapes {
				if l.pos+1 < n {
					l.pos++
				}
			}

		case stateBacktick:
			if ch == '`' {
				if l.pos+1 < n && l.input[l.pos+1] == '`' {
					l.pos++ // 跳过转义反引号 ``
				} else {
					state = stateNormal
				}
			}

		case stateBlockComment:
			// 嵌套块注释？MySQL 不支持嵌套块注释
			// 寻找 */
			if ch == '*' && l.pos+1 < n && l.input[l.pos+1] == '*' {
				// 连续的 ** — 可能是结束 */
				// 继续
			}
			if ch == '*' && l.pos+1 < n && l.input[l.pos+1] == '/' {
				l.pos++ // 跳过 /
				state = stateNormal
			}

		case stateLineComment:
			if ch == '\n' {
				state = stateNormal
			}

		default:
			return false, fmt.Errorf("ecm lexer: unknown state %d at position %d", state, l.pos)
		}

		l.pos++
	}

	// 输入耗尽时检查未闭合状态
	switch state {
	case stateSingleQuote, stateDoubleQuote, stateBacktick:
		// 未闭合的引号/标识符 —— MySQL 会报 parse error
		// 对 ECM 检测来说没有影响（/*! 不会出现在这些未闭合结构之后）
		// 不视为 lexer error
		return false, nil
	case stateBlockComment:
		// 未闭合块注释 —— 保守：标记为错误
		return false, fmt.Errorf("ecm lexer: unclosed block comment")
	}

	return false, nil
}

// isMySQLCommentWhitespace 判断字符是否是 MySQL 中 -- 行注释后的合法空白。
// MySQL 将 -- 后紧跟的任何空白/控制字符视为行注释开始，
// 包括：空格 (0x20)、tab (0x09)、换行 (0x0A)、回车 (0x0D)、
// form-feed (0x0C)、vertical-tab (0x0B) 以及大多数控制字符。
func isMySQLCommentWhitespace(c byte) bool {
	return c <= ' ' // 空格及所有控制字符
}

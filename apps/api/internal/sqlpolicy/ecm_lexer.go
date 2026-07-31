package sqlpolicy

import "fmt"

// ecmLexer WebDB 自有 MySQL ECM 词法分析器。
// 确定性单趟状态机，O(n)/O(1)。ADR-007: MySQL 在 AST 前识别并拒绝 ECM。

type lexerState int

const (
	stateNormal           lexerState = iota
	stateSingleQuote                 // '...'
	stateDoubleQuote                 // "..." 字符串（非 ANSI_QUOTES）
	stateDoubleQuoteIdent            // "..." 标识符（ANSI_QUOTES）[CR #7]
	stateBacktick                    // `...`
	stateBlockComment                // /* ... */
	stateLineComment                 // -- ... 或 # ...
)

type ecmLexer struct {
	input []byte
	pos   int
	mode  MySQLLexerMode
}

func newECMLexer(input string, mode MySQLLexerMode) *ecmLexer {
	return &ecmLexer{input: []byte(input), pos: 0, mode: mode}
}

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
				// [CR #7] ANSI_QUOTES: 双引号为标识符；否则为字符串
				if l.mode.ANSIQuotes {
					state = stateDoubleQuoteIdent
				} else {
					state = stateDoubleQuote
				}
			case ch == '`':
				state = stateBacktick
			case ch == '#':
				state = stateLineComment
			case ch == '-':
				if l.pos+1 < n && l.input[l.pos+1] == '-' {
					if l.pos+2 >= n || isMySQLCommentWhitespace(l.input[l.pos+2]) {
						state = stateLineComment
						l.pos++
					}
				}
			case ch == '/':
				if l.pos+1 < n && l.input[l.pos+1] == '*' {
					if l.pos+2 < n {
						if l.input[l.pos+2] == '!' {
							return true, nil
						}
						if l.input[l.pos+2] == '+' {
							l.pos += 2
							state = stateBlockComment
							continue
						}
					}
					l.pos++
					state = stateBlockComment
				}
			}

		case stateSingleQuote:
			if ch == '\'' {
				if l.pos+1 < n && l.input[l.pos+1] == '\'' {
					l.pos++
				} else {
					state = stateNormal
				}
			} else if ch == '\\' && !l.mode.NoBackslashEscapes {
				if l.pos+1 < n {
					l.pos++
				}
			}

		case stateDoubleQuote:
			// [CR #8] 字符串模式：反斜杠转义取决于 NoBackslashEscapes
			if ch == '"' {
				if l.pos+1 < n && l.input[l.pos+1] == '"' {
					l.pos++
				} else {
					state = stateNormal
				}
			} else if ch == '\\' && !l.mode.NoBackslashEscapes {
				if l.pos+1 < n {
					l.pos++
				}
			}

		case stateDoubleQuoteIdent:
			// [CR #7] ANSI_QUOTES 标识符：仅 "" 转义，反斜杠不转义
			if ch == '"' {
				if l.pos+1 < n && l.input[l.pos+1] == '"' {
					l.pos++
				} else {
					state = stateNormal
				}
			}

		case stateBacktick:
			if ch == '`' {
				if l.pos+1 < n && l.input[l.pos+1] == '`' {
					l.pos++
				} else {
					state = stateNormal
				}
			}

		case stateBlockComment:
			if ch == '*' && l.pos+1 < n && l.input[l.pos+1] == '/' {
				l.pos++
				state = stateNormal
			}

		case stateLineComment:
			if ch == '\n' {
				state = stateNormal
			}

		// [CR #6] default 不可达（所有状态均有对应 case），保留为 fail-safe
		default:
			return false, fmt.Errorf("ecm lexer: unknown state %d at pos %d", state, l.pos)
		}

		l.pos++
	}

	if state == stateBlockComment {
		return false, fmt.Errorf("ecm lexer: unclosed block comment")
	}
	return false, nil
}

func isMySQLCommentWhitespace(c byte) bool {
	return c <= ' '
}

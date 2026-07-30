// WebDB ECM Lexer v2: deterministic state machine, O(n) time, O(1) space.
// Fixes: stDash1 bypass, unknown mode fail-closed, EOF backslash states.
package main

import "fmt"

type ECMMode int

const (
	ModeDefault           ECMMode = iota
	ModeNoBackslashEscapes
)

type ECMLexerResult struct {
	HasExecComment bool
	Error          error
}

const (
	stNormal = iota
	stSingleQuote
	stDoubleQuote
	stBacktick
	stBlockComment
	stLineComment
	stHashComment
	stBackslashSingle
	stBackslashDouble
)

func isMySQLDashWS(ch byte) bool {
	switch ch {
	case ' ', '\t', '\n', '\r', '\f', '\v':
		return true
	}
	return false
}

func ScanECM(sql string, mode ECMMode) ECMLexerResult {
	switch mode {
	case ModeDefault, ModeNoBackslashEscapes:
	default:
		return ECMLexerResult{Error: fmt.Errorf("unknown ECM mode: %d", mode)}
	}

	bs := mode == ModeDefault
	n := len(sql)
	state := stNormal

	for i := 0; i < n; i++ {
		ch := sql[i]
		switch state {
		case stNormal:
			switch ch {
			case '\'':
				state = stSingleQuote
			case '"':
				state = stDoubleQuote
			case '`':
				state = stBacktick
			case '/':
				if i+1 < n && sql[i+1] == '*' {
					if i+2 < n && sql[i+2] == '!' {
						return ECMLexerResult{HasExecComment: true}
					}
					state = stBlockComment
					i++
				}
			case '#':
				state = stHashComment
			case '-':
				if i+1 < n && sql[i+1] == '-' && i+2 < n && isMySQLDashWS(sql[i+2]) {
					state = stLineComment
					i += 1 // skip second '-', let stLineComment process the whitespace/newline
				}
			}
		case stSingleQuote:
			if bs && ch == '\\' {
				state = stBackslashSingle
			} else if ch == '\'' {
				state = stNormal
			}
		case stDoubleQuote:
			if bs && ch == '\\' {
				state = stBackslashDouble
			} else if ch == '"' {
				state = stNormal
			}
		case stBacktick:
			if ch == '`' {
				state = stNormal
			}
		case stBlockComment:
			if ch == '*' && i+1 < n && sql[i+1] == '/' {
				state = stNormal
				i++
			}
		case stLineComment:
			if ch == '\n' {
				state = stNormal
			}
		case stHashComment:
			if ch == '\n' {
				state = stNormal
			}
		case stBackslashSingle:
			state = stSingleQuote
		case stBackslashDouble:
			state = stDoubleQuote
		}
	}

	switch state {
	case stSingleQuote, stDoubleQuote, stBacktick:
		return ECMLexerResult{Error: fmt.Errorf("unterminated quote/identifier at EOF")}
	case stBlockComment:
		return ECMLexerResult{Error: fmt.Errorf("unterminated block comment at EOF")}
	case stBackslashSingle, stBackslashDouble:
		return ECMLexerResult{Error: fmt.Errorf("unterminated escape at EOF")}
	}
	return ECMLexerResult{HasExecComment: false}
}

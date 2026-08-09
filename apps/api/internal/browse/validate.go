package browse

import (
	"regexp"
	"unicode/utf8"
)

// 标识符边界（P0-06A §7 / CT-16）：≤63 字符、UTF-8、保守字符白名单。
// schema/table 作为 information_schema 查询的值参数绑定，白名单为纵深防御。
// 服务层与 HTTP 层共用同一判定（P2-5），保证所有入口行为一致。
//
// F5 明确化：白名单 ^[A-Za-z0-9_$]+$ 与 MaxIdentLen=63 是安全方向的保守选择，
// 会拒绝含 '-'、'.'、Unicode 的合法标识符，以及 MySQL 64 字符标识符名。
// 这是刻意的 fail-closed 收紧（防注入 + 演示环境标准命名），本任务不扩展字符集；
// 若未来需支持更广标识符，属功能决策，需 Owner 确认并按方言评估（如 quoteIdent
// 或放宽白名单），不在 WEB-36 范围。
const MaxIdentLen = 63

var identRe = regexp.MustCompile(`^[A-Za-z0-9_$]+$`)

// ValidIdent 校验 schema/table 标识符：非空、UTF-8、长度与字符白名单。
func ValidIdent(v string) bool {
	if v == "" || len(v) > MaxIdentLen || !utf8.ValidString(v) {
		return false
	}
	return identRe.MatchString(v)
}

package metadata

import (
	"strings"
	"testing"
)

// assertRedacted 断言脱敏输出不含完整敏感值、其可识别片段，且包含 [redacted] 占位符（vti-LIt）。
// 片段检查从 value 的第二个字段起：首 token 被 [redacted] 替换后，若泄漏会出现在后续字段中；
// 与键名重合的词（如 token= 的键名）不纳入检查。
func assertRedacted(t *testing.T, input, value, out string) {
	t.Helper()
	if strings.Contains(out, value) {
		t.Errorf("value leaked: input=%q out=%q", input, out)
	}
	fields := strings.Fields(value)
	for _, frag := range fields[1:] {
		if strings.Contains(out, frag) {
			t.Errorf("value fragment %q leaked: input=%q out=%q", frag, input, out)
		}
	}
	if !strings.Contains(out, "[redacted]") {
		t.Errorf("value not redacted: input=%q out=%q", input, out)
	}
}

// TestRedactSensitive_QuotedPassword 验证引号包裹的密码值（含空格）被整体脱敏，
// 输出不含完整值与任何可识别片段（vti-EhP/vti-LIt）。
func TestRedactSensitive_QuotedPassword(t *testing.T) {
	cases := []struct {
		input string
		value string
	}{
		{`password="sup3r secret value with spaces"`, `sup3r secret value with spaces`},
		{`password='sup3r secret value'`, `sup3r secret value`},
		{`passwd='another secret'`, `another secret`},
		{`pwd="x y z"`, `x y z`},
	}
	for _, c := range cases {
		assertRedacted(t, c.input, c.value, RedactSensitive(c.input))
	}
}

// TestRedactSensitive_EscapedQuote 验证转义引号内的密码值被整体脱敏（vti-LIx）。
func TestRedactSensitive_EscapedQuote(t *testing.T) {
	cases := []struct {
		input string
		value string
	}{
		{`password="ab\"cd ef"`, `ab"cd ef`},
		{`password='ab\'cd ef'`, `ab'cd ef`},
	}
	for _, c := range cases {
		assertRedacted(t, c.input, c.value, RedactSensitive(c.input))
	}
}

// TestRedactSensitive_JSONValue 验证 JSON 键名冒号形式被整体脱敏（vti-LIx）。
func TestRedactSensitive_JSONValue(t *testing.T) {
	cases := []struct {
		input string
		value string
	}{
		{`{"password":"secret"`, `secret`},
		{`{"kek":"WnVqLWNvbmZpZzEy"}`, `WnVqLWNvbmZpZzEy`},
	}
	for _, c := range cases {
		assertRedacted(t, c.input, c.value, RedactSensitive(c.input))
	}
}

// TestRedactSensitive_UppercaseDSN 验证大写 DSN scheme 被脱敏（vti-LIx）。
func TestRedactSensitive_UppercaseDSN(t *testing.T) {
	cases := []struct {
		input string
		value string
	}{
		{`POSTGRES://dbuser:secretpw@db.invalid:5432/webdb`, `dbuser:secretpw`},
		{`MYSQL://dbuser:hunter2@db.invalid:3306/db`, `dbuser:hunter2`},
	}
	for _, c := range cases {
		out := RedactSensitive(c.input)
		if strings.Contains(out, c.value) {
			t.Errorf("DSN user:pass leaked: input=%q out=%q", c.input, out)
		}
		if !strings.Contains(out, "[redacted]") {
			t.Errorf("DSN not redacted: input=%q out=%q", c.input, out)
		}
	}
}

// TestRedactSensitive_UnquotedPassword 验证未加引号的密码值仍被脱敏（保留既有行为）。
func TestRedactSensitive_UnquotedPassword(t *testing.T) {
	out := RedactSensitive("password=classified123")
	if strings.Contains(out, "classified123") {
		t.Errorf("unquoted password leaked: %q", out)
	}
	if !strings.Contains(out, "password=[redacted]") {
		t.Errorf("unquoted password not redacted: %q", out)
	}
}

// TestRedactSensitive_QuotedKEKToken 验证引号包裹的 KEK/token 值被整体脱敏，
// 输出不含完整值与任何可识别片段（vti-EhP/vti-LIt）。
func TestRedactSensitive_QuotedKEKToken(t *testing.T) {
	cases := []struct {
		input string
		value string
	}{
		{`kek="WnVq LWNvbmZpZzEy"`, `WnVq LWNvbmZpZzEy`},
		{`token='super secret value'`, `super secret value`},
	}
	for _, c := range cases {
		assertRedacted(t, c.input, c.value, RedactSensitive(c.input))
	}
}

// TestRedactSensitive_MultilinePEM 验证多行 PEM 私钥被完整替换（dot-all，vti-EhP）。
func TestRedactSensitive_MultilinePEM(t *testing.T) {
	pem := "-----BEGIN PRIVATE KEY-----\nMIIEvgIBADANBgkqhkiG9w0BAQEFA\nsecretline2\n-----END PRIVATE KEY-----"
	out := RedactSensitive(pem)
	if strings.Contains(out, "MIIEvgIBADAN") || strings.Contains(out, "secretline2") {
		t.Errorf("multiline PEM leaked: %q", out)
	}
	if !strings.Contains(out, "[redacted: private key]") {
		t.Errorf("multiline PEM not redacted: %q", out)
	}
}

// TestRedactSensitive_DSNUserPass 验证连接串中的用户密码被脱敏（vti-EhP 保留项）。
func TestRedactSensitive_DSNUserPass(t *testing.T) {
	out := RedactSensitive("postgres://dbuser:secretpw@db.invalid:5432/webdb")
	if strings.Contains(out, "secretpw") {
		t.Errorf("DSN password leaked: %q", out)
	}
	if !strings.Contains(out, "postgres://[redacted]@") {
		t.Errorf("DSN user:pass not redacted: %q", out)
	}
}

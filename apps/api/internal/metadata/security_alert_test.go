package metadata

import (
	"strings"
	"testing"
)

// TestRedactSensitive_QuotedPassword 验证引号包裹的密码值（含空格）被整体脱敏（vti-EhP）。
func TestRedactSensitive_QuotedPassword(t *testing.T) {
	cases := []string{
		`password="sup3r secret value with spaces"`,
		`password='sup3r secret value'`,
		`passwd='another secret'`,
		`pwd="x y z"`,
	}
	for _, input := range cases {
		out := RedactSensitive(input)
		if strings.Contains(out, "sup3r") || strings.Contains(out, "another secret") {
			t.Errorf("quoted password leaked: %q -> %q", input, out)
		}
		if !strings.Contains(out, "[redacted]") {
			t.Errorf("quoted password not redacted: %q -> %q", input, out)
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

// TestRedactSensitive_QuotedKEKToken 验证引号包裹的 KEK/token 值被整体脱敏（vti-EhP）。
func TestRedactSensitive_QuotedKEKToken(t *testing.T) {
	cases := []string{
		`kek="WnVq LWNvbmZpZzEy"`,
		`token='super secret token value'`,
	}
	for _, input := range cases {
		out := RedactSensitive(input)
		if strings.Contains(out, "WnVq") || strings.Contains(out, "super secret") {
			t.Errorf("quoted kek/token leaked: %q -> %q", input, out)
		}
		if !strings.Contains(out, "[redacted]") {
			t.Errorf("quoted kek/token not redacted: %q -> %q", input, out)
		}
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

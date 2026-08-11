package seeddemo

import (
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// P1（Codex 二轮）：metadata DSN 必须经安全 URL/config 构造，特殊字符不得破坏解析。
// 本测试直接对纯函数 buildMetaDSN 断言，并用 pgx.ParseConfig 还原校验：
// 生成值可被当前 PostgreSQL driver/config parser 正确还原。

func TestBuildMetaDSN_roundTrips(t *testing.T) {
	cases := []struct {
		name     string
		host     string
		port     string
		user     string
		password string
		dbname   string
		sslmode  string
	}{
		{"password-space", "webdb-meta", "5432", "webdb", "pa ss", "webdb_meta", "disable"},
		{"password-quotes", "webdb-meta", "5432", "webdb", `p'a"ss`, "webdb_meta", "disable"},
		{"password-backslash", "webdb-meta", "5432", "webdb", `back\slash`, "webdb_meta", "disable"},
		{"password-special", "webdb-meta", "5432", "webdb", `p@ss:/?#%&`, "webdb_meta", "disable"},
		{"username-encode", "webdb-meta", "5432", "us er", "pw", "webdb_meta", "disable"},
		{"dbname-encode", "webdb-meta", "5432", "webdb", "pw", "my db", "disable"},
		{"dbname-slash", "webdb-meta", "5432", "webdb", "pw", "a/b", "disable"},
		{"dbname-percent", "webdb-meta", "5432", "webdb", "pw", "db%x", "disable"},
		{"ipv4", "127.0.0.1", "5432", "webdb", "pw", "webdb_meta", "disable"},
		{"hostname", "webdb-meta", "5432", "webdb", "pw", "webdb_meta", "disable"},
		{"ipv6", "::1", "5432", "webdb", "pw", "webdb_meta", "disable"},
		{"sslmode-require", "webdb-meta", "5432", "webdb", "pw", "webdb_meta", "require"},
		{"sslmode-verify-full", "webdb-meta", "5432", "webdb", "pw", "webdb_meta", "verify-full"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dsn := buildMetaDSN(tc.host, tc.port, tc.user, tc.password, tc.dbname, tc.sslmode)
			cfg, err := pgx.ParseConfig(dsn)
			if err != nil {
				t.Fatalf("生成 DSN 无法被 pgx 解析: %v", err)
			}
			if cfg.Host != tc.host {
				t.Errorf("host 还原失败: got %q want %q", cfg.Host, tc.host)
			}
			wantPort, _ := strconv.ParseUint(tc.port, 10, 16)
			if cfg.Port != uint16(wantPort) {
				t.Errorf("port 还原失败: got %d want %d", cfg.Port, wantPort)
			}
			if cfg.User != tc.user {
				t.Errorf("user 还原失败: got %q want %q", cfg.User, tc.user)
			}
			if cfg.Password != tc.password {
				// 失败信息不输出 password 值（不泄露 canary）。
				t.Errorf("password 还原失败（case %q）", tc.name)
			}
			if cfg.Database != tc.dbname {
				t.Errorf("dbname 还原失败: got %q want %q", cfg.Database, tc.dbname)
			}
		})
	}
}

// 非法 port 仍 fail-closed：buildMetaDSN 生成值不应被 pgx 接受。
// 注意：空端口是合法 URL 语义（pgx 回退默认 5432），且生产路径 envOr 保证端口非空，
// 因此只验证非数字与越界端口。
func TestBuildMetaDSN_invalidPortFailsClosed(t *testing.T) {
	for _, port := range []string{"notaport", "99999"} {
		dsn := buildMetaDSN("webdb-meta", port, "webdb", "pw", "webdb_meta", "disable")
		if _, err := pgx.ParseConfig(dsn); err == nil {
			t.Errorf("非法端口 %q 应 fail-closed（解析失败）", port)
		}
	}
}

// sslmode 必须作为 query 参数，不能作为裸字符串拼接（Codex 二轮）。
func TestBuildMetaDSN_sslmodeIsQueryParam(t *testing.T) {
	dsn := buildMetaDSN("webdb-meta", "5432", "webdb", "pw", "webdb_meta", "disable")
	if !strings.Contains(dsn, "?sslmode=disable") {
		t.Errorf("sslmode 应作为 query 参数（?sslmode=disable），实际 dsn 不含: %q", dsn)
	}
}

// 生成的 DSN 不得包含明文 password（即使含特殊字符）。
func TestBuildMetaDSN_passwordNotPlain(t *testing.T) {
	passwords := []string{"pa ss", `p'a"ss`, `back\slash`, `p@ss:/?#%&`}
	for _, pw := range passwords {
		dsn := buildMetaDSN("webdb-meta", "5432", "webdb", pw, "webdb_meta", "disable")
		if strings.Contains(dsn, pw) {
			t.Errorf("DSN 不得包含明文 password（case %q）", pw)
		}
	}
}

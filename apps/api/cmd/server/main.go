// WebDB API 与执行服务入口
// P0-02：支持 serve 与 migrate 子命令；serve 启动时不自动迁移
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/fujiabao89/webdb/internal/adapter"
	"github.com/fujiabao89/webdb/internal/browse"
	"github.com/fujiabao89/webdb/internal/browsehttp"
	"github.com/fujiabao89/webdb/internal/credentials"
	"github.com/fujiabao89/webdb/internal/execution"
	"github.com/fujiabao89/webdb/internal/executionhttp"
	"github.com/fujiabao89/webdb/internal/metadata"
	"github.com/fujiabao89/webdb/internal/migrate"
	"github.com/fujiabao89/webdb/internal/sqlpolicy"
)

const version = "0.2.0"

// ---- serve ----------------------------------------------------------------

type healthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
	Time    string `json:"time"`
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	json.NewEncoder(w).Encode(healthResponse{
		Status:  "ok",
		Version: version,
		Time:    time.Now().UTC().Format(time.RFC3339),
	})
}

func runServe() error {
	port := os.Getenv("API_PORT")
	if port == "" {
		port = "8080"
	}

	// 可信演示 Principal（D01b，P0-06A §5.2）：配置缺失/非法 → 拒绝启动（fail-closed），
	// 禁止零值/默认值/客户端身份回退。
	principal, err := executionhttp.PrincipalFromEnv()
	if err != nil {
		return err
	}

	// 元数据库连接。
	dsn, err := metaDSN()
	if err != nil {
		return err
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("连接元数据库失败: %w", err)
	}
	defer db.Close()
	store := metadata.NewPGStore(db)

	// 凭证解析（KEK 从环境加载；缺失 → 启动失败，不写明文密钥到任何输出）。
	kek, err := credentials.NewEnvKEKProvider()
	if err != nil {
		return fmt.Errorf("KEK 初始化失败: %w", err)
	}
	alarm := execution.NewStderrAlarm()
	lm := credentials.NewLifecycleManager(store, store, store, kek, alarm)

	// D01b fail-closed 补强：演示 Principal 必须指向存在的 active 成员，否则拒绝启动。
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := store.MemberByWorkspaceAndUser(ctx, principal.WorkspaceID, principal.UserID); err != nil {
		return fmt.Errorf("演示 Principal 未指向存在的 workspace 成员: %w", err)
	}

	// Adapter + 执行 Pipeline。
	manager := adapter.NewAdapterManager(adapter.ManagerOptions{AllowInsecureLocalDemo: true})
	pipeline := execution.NewPipeline(execution.PipelineConfig{
		Store:       store,
		PolicyStore: store,
		Members:     store,
		Resolver:    lm,
		Adapter:     execution.NewAdapterClient(manager),
		MySQLMode:   sqlpolicy.MySQLLexerMode{},
		Tx:          store,
		Audit:       store,
		Alarm:       alarm,
	})
	defer pipeline.Close()

	// 浏览服务（WEB-36）+ 执行 HTTP（WEB-35）。
	browseSvc := browse.NewService(store, store, store, lm, browse.AdapterBrowser{Manager: manager}, browse.DefaultLimits())
	browseHTTP := browsehttp.NewServer(browseSvc, func(r *http.Request) (browse.Principal, bool) {
		return executionhttp.PrincipalFromContext(r.Context())
	})
	execHTTP := executionhttp.NewServer(principal, pipeline)

	// 统一 /api/v1 传输层：六条已批准路由 + 可信 Principal 注入 + panic recovery。
	composed := executionhttp.ComposeHandler(browseHTTP, execHTTP)
	handler := executionhttp.RecoverMiddleware(nil)(executionhttp.PrincipalMiddleware(principal)(composed))

	mux := http.NewServeMux()
	mux.Handle("/api/v1/", handler)
	mux.HandleFunc("/health", healthHandler)

	server := &http.Server{
		Addr:        ":" + port,
		Handler:     mux,
		ReadTimeout: 5 * time.Second,
		// P2-2（审查）文档化限制：WriteTimeout 是响应写出超时，不取消 request context；
		// 目标库查询超时由 ConnectionPolicy.StatementTimeoutMs（服务端权威）控制。
		// 若策略超时配置大于 WriteTimeout，连接会先被关闭但查询继续执行（连接/permit
		// 占用至策略超时）。演示默认 StatementTimeoutMs 较小；生产部署应将
		// WriteTimeout 对齐策略超时上限，或按文档启用响应写出前的查询取消。
		WriteTimeout: 5 * time.Second,
		IdleTimeout:  30 * time.Second,
	}

	log.Printf("WebDB API %s 启动，端口 %s", version, port)
	return server.ListenAndServe()
}

// ---- migrate --------------------------------------------------------------

// metaDSN 返回元数据库迁移连接串。
// 生产环境：META_DB_USER 为运行时非超级用户（webdb_app_runtime，API 运行时连接），
// 迁移需 DDL 权限，使用独立的 META_MIGRATE_USER/PASSWORD（管理员）。
// 迁移凭据必须成对设置：只设其一即返回错误，禁止静默混用；
// 仅当两者都未设置时才回退到 META_DB_USER/PASSWORD（本地开发，见 PR37 检定/八轮审查）。
func metaDSN() (string, error) {
	migrateUser := os.Getenv("META_MIGRATE_USER")
	migratePassword := os.Getenv("META_MIGRATE_PASSWORD")
	var user, password string
	switch {
	case migrateUser != "" && migratePassword != "":
		user, password = migrateUser, migratePassword
	case migrateUser == "" && migratePassword == "":
		user = envOr("META_DB_USER", "webdb")
		password = envOr("META_DB_PASSWORD", "change_me")
	default:
		return "", fmt.Errorf("META_MIGRATE_USER 与 META_MIGRATE_PASSWORD 必须成对设置（当前仅设置了其一）")
	}
	host := envOr("META_DB_HOST", "webdb-meta")
	port := envOr("META_DB_PORT", "5432")
	dbname := envOr("META_DB_NAME", "webdb_meta")
	sslmode := envOr("META_DB_SSLMODE", "disable")

	u := &url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(user, password),
		Host:     fmt.Sprintf("%s:%s", host, port),
		Path:     dbname,
		RawQuery: fmt.Sprintf("sslmode=%s", sslmode),
	}
	return u.String(), nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func runMigrate(dir string) error {
	dsn, err := metaDSN()
	if err != nil {
		return err
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("连接元数据库失败: %w", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	switch dir {
	case "up", "down":
		return migrate.Run(ctx, db, dir)
	case "reset":
		return migrate.Run(ctx, db, "down-to", "0")
	case "status":
		return migrate.Status(ctx, db)
	case "validate":
		return migrate.Validate()
	default:
		return fmt.Errorf("migrate: 不支持的方向 %q（仅支持 up、down、reset、status、validate）", dir)
	}
}

// ---- main ----------------------------------------------------------------

func main() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	var err error
	switch cmd {
	case "serve":
		err = runServe()
	case "migrate":
		dir := "up"
		if len(os.Args) > 2 {
			dir = os.Args[2]
		}
		err = runMigrate(dir)
	default:
		fmt.Fprintf(os.Stderr, "未知命令: %s\n", os.Args[1])
		os.Exit(1)
	}

	if err != nil {
		log.Fatalf("执行失败: %v", err)
	}
}

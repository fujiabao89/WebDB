// WebDB API 与执行服务入口
// P0-02：支持 serve 与 migrate 子命令；serve 启动时不自动迁移
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
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

	// 元数据库连接（运行时最小权限账号；META_MIGRATE_* 迁移凭据仅 migrate 子命令使用，CodeRabbit #3）。
	dsn, err := runtimeMetaDSN()
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

	// AllowInsecureLocalDemo（CodeRabbit #4）：默认关闭；仅由显式严格解析的环境开关
	// ALLOW_INSECURE_LOCAL_DEMO=true 启用，且只允许本地演示连接（adapter 层校验
	// localhost 主机）。非演示/生产模式不继承 insecure 默认。
	allowInsecure, err := allowInsecureLocalDemo()
	if err != nil {
		return err
	}
	if allowInsecure {
		// 结构化 warning，不含敏感字段。
		log.Printf("WARNING: ALLOW_INSECURE_LOCAL_DEMO=true 已启用：仅允许本地演示连接（TLS disable）；禁止用于生产")
	}

	// Adapter + 执行 Pipeline。
	manager := adapter.NewAdapterManager(adapter.ManagerOptions{AllowInsecureLocalDemo: allowInsecure})
	defer func() {
		// 排空目标库连接池（CodeRabbit #5）：进程退出前主动关闭，不依赖 GC/OS 清理。
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := manager.Close(ctx); err != nil {
			log.Printf("AdapterManager 关闭未能在超时内完成: %v", err)
		}
	}()
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
	execHTTP := executionhttp.NewServer(pipeline)

	// 统一 /api/v1 传输层：六条已批准路由 + 可信 Principal 注入 + panic recovery。
	// PrincipalMiddleware 在 RecoverMiddleware 之外：panic 恢复点能读取已验证 Principal，
	// 记录脱敏 workspace_id 关联字段（CodeRabbit #17）。
	composed := executionhttp.ComposeHandler(browseHTTP, execHTTP)
	handler := executionhttp.PrincipalMiddleware(principal)(executionhttp.RecoverMiddleware(nil)(composed))

	mux := http.NewServeMux()
	mux.Handle("/api/v1/", handler)
	mux.HandleFunc("/health", healthHandler)

	server := &http.Server{
		Addr:        ":" + port,
		Handler:     mux,
		ReadTimeout: 5 * time.Second,
		// P1 审查修复（Greptile P1）：原 WriteTimeout=5s 会先于最长 60s 查询截止并
		// 断开客户端，而查询不被取消（Go 的 WriteTimeout 是写截止，不取消 request
		// context）。现按单一时间预算对齐：WriteTimeout = 最大查询预算
		//（executionhttp.DefaultRequestTimeout）+ 响应写出余量，保证写超时不早于
		// 允许的最长查询；客户端断开经 request context 取消查询（D13 transport abort）。
		WriteTimeout: serverWriteTimeout(),
		IdleTimeout:  30 * time.Second,
	}

	log.Printf("WebDB API %s 启动，端口 %s", version, port)

	// 优雅关闭（CodeRabbit 新 #1）：Docker Compose 停止容器发 SIGTERM，Go 默认直接
	// 退出不执行 defer，manager.Close/pipeline.Close 不会运行。注册 SIGINT/SIGTERM，
	// 用有界 context 调 server.Shutdown（等活跃请求完成后返回 http.ErrServerClosed），
	// 随后函数返回触发 defer 关闭 pipeline 与 AdapterManager 连接池。
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe() }()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("HTTP 服务异常退出: %w", err)
		}
		return nil
	case <-sigCh:
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			return fmt.Errorf("优雅关闭未能在超时内完成: %w", err)
		}
		return nil
	}
}

// responseWriteBudget 响应序列化与写出余量：给服务端在查询完成后写出成功/安全错误
// 响应的时间。固定有界（Greptile P1 修复的单一时间预算组成之一）。
const responseWriteBudget = 5 * time.Second

// serverWriteTimeout 返回 http.Server.WriteTimeout 的单一、可解释、可测试时间预算：
// 最大查询预算 + 响应写出余量。保证 WriteTimeout 不早于允许的最长查询 context，
// WriteTimeout 触发时查询已完成；客户端断开经 request context 取消查询。
func serverWriteTimeout() time.Duration {
	return executionhttp.DefaultRequestTimeout + responseWriteBudget
}

// ---- migrate --------------------------------------------------------------

// runtimeMetaDSN 返回元数据库运行时连接串（最小权限 runtime 账号）。
// API 运行时连接元数据库只使用 META_DB_USER（webdb_app_runtime，非超级用户），
// 不携带 DDL 权限；META_MIGRATE_* 迁移凭据只由 migrate 子命令使用（CodeRabbit #3）。
func runtimeMetaDSN() (string, error) {
	user := envOr("META_DB_USER", "webdb")
	password := envOr("META_DB_PASSWORD", "change_me")
	return buildMetaDSN(user, password), nil
}

// migrateMetaDSN 返回元数据库迁移连接串（DDL 权限）。
// 生产环境：迁移需 DDL 权限，使用独立的 META_MIGRATE_USER/PASSWORD（管理员）。
// 迁移凭据必须成对设置：只设其一即返回错误，禁止静默混用；
// 仅当两者都未设置时才回退到 META_DB_USER/PASSWORD（本地开发，见 PR37 检定/八轮审查）。
func migrateMetaDSN() (string, error) {
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
	return buildMetaDSN(user, password), nil
}

func buildMetaDSN(user, password string) string {
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
	return u.String()
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// allowInsecureLocalDemo 从环境开关 ALLOW_INSECURE_LOCAL_DEMO 严格解析（CodeRabbit #4）：
// 未设置/空 → false（默认关闭）；仅接受显式 "true"（大小写不敏感）→ true；
// 其他非空值一律拒绝启动（fail-closed），禁止宽松解析或静默忽略非法值。
func allowInsecureLocalDemo() (bool, error) {
	v := strings.TrimSpace(os.Getenv("ALLOW_INSECURE_LOCAL_DEMO"))
	switch {
	case v == "":
		return false, nil
	case strings.EqualFold(v, "true"):
		return true, nil
	default:
		return false, fmt.Errorf("ALLOW_INSECURE_LOCAL_DEMO 非法值 %q（仅接受 true；默认关闭）", v)
	}
}

func runMigrate(dir string) error {
	dsn, err := migrateMetaDSN()
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

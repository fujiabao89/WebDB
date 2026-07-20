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
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/fujiabao89/webdb/internal/migrate"
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

	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthHandler)

	server := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
		IdleTimeout:  30 * time.Second,
	}

	log.Printf("WebDB API %s 启动，端口 %s", version, port)
	return server.ListenAndServe()
}

// ---- migrate --------------------------------------------------------------

func metaDSN() string {
	host := envOr("META_DB_HOST", "webdb-meta")
	port := envOr("META_DB_PORT", "5432")
	user := envOr("META_DB_USER", "webdb")
	password := envOr("META_DB_PASSWORD", "change_me")
	dbname := envOr("META_DB_NAME", "webdb_meta")
	sslmode := envOr("META_DB_SSLMODE", "disable")

	return fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		host, port, user, password, dbname, sslmode,
	)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func runMigrate(dir string) error {
	dsn := metaDSN()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("连接元数据库失败: %w", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if dir == "status" {
		return migrate.Status(ctx, db)
	}
	if dir == "validate" {
		return migrate.Validate()
	}
	return migrate.Run(ctx, db, dir)
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

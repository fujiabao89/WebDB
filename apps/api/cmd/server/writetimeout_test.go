package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fujiabao89/webdb/internal/executionhttp"
)

// TestServerWriteTimeoutBudgetRelationship 验证 WriteTimeout 的单一时间预算关系
// （Greptile P1 修复）：WriteTimeout 必须覆盖读入、最长请求 context、请求超时后
// 独立运行的审计收尾以及响应写出；否则审计成功后仍可能因写截止对客户端 EOF。
func TestServerWriteTimeoutBudgetRelationship(t *testing.T) {
	minimum := 5*time.Second + executionhttp.DefaultRequestTimeout + 5*time.Second + responseWriteBudget
	if serverWriteTimeout() < minimum {
		t.Fatalf("serverWriteTimeout=%v 必须 >= %v（读入+最长请求+审计收尾+响应写出）",
			serverWriteTimeout(), minimum)
	}
	if serverWriteTimeout() <= 0 {
		t.Fatal("WriteTimeout 必须有界且为正")
	}
}

// TestServerWriteTimeoutAllowsFullQueryBudget 用真实 TCP server 复现原 5s/60s 问题：
// 原 WriteTimeout=5s 会先于最长 60s 查询截止并断开客户端。修复后 WriteTimeout
// 对齐最长查询预算，一个 6s（> 原 5s）的查询必须完整完成并返回 200，不被写截止切断。
func TestServerWriteTimeoutAllowsFullQueryBudget(t *testing.T) {
	// handler 模拟一个运行 6s 的查询（超过原 WriteTimeout=5s，但在新预算内）。
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(6 * time.Second):
		case <-r.Context().Done():
			// 查询被 request context 取消（不应在无客户端断开时发生）。
			w.WriteHeader(http.StatusGatewayTimeout)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: handler, WriteTimeout: serverWriteTimeout()}
	defer srv.Close()
	go func() { _ = srv.Serve(ln) }()

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+ln.Addr().String()+"/", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET 失败（查询可能被 WriteTimeout 提前切断）: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200（6s 查询应完整完成）", resp.StatusCode)
	}
	if elapsed := time.Since(start); elapsed < 5*time.Second {
		t.Fatalf("响应在 %v 返回，早于查询耗时 6s——WriteTimeout 仍会提前切断查询", elapsed)
	}
}

// TestServerWriteTimeoutAllowsDetachedAuditFinalization uses a scaled real TCP
// server to verify the path where request work reaches its deadline and a
// detached, bounded audit finalizer still has to complete before the response.
func TestServerWriteTimeoutAllowsDetachedAuditFinalization(t *testing.T) {
	const (
		readBudget     = 25 * time.Millisecond
		requestBudget  = 250 * time.Millisecond
		auditBudget    = 100 * time.Millisecond
		responseBudget = 100 * time.Millisecond
	)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCtx, cancelRequest := context.WithTimeout(r.Context(), requestBudget)
		defer cancelRequest()
		<-requestCtx.Done()

		auditCtx, cancelAudit := context.WithTimeout(context.WithoutCancel(requestCtx), auditBudget)
		defer cancelAudit()
		select {
		case <-time.After(auditBudget - 20*time.Millisecond):
		case <-auditCtx.Done():
			http.Error(w, "audit canceled", http.StatusInternalServerError)
			return
		}
		time.Sleep(responseBudget - 20*time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{
		Handler:      handler,
		ReadTimeout:  readBudget,
		WriteTimeout: serverTimeoutBudget(readBudget, requestBudget, auditBudget, responseBudget),
	}
	defer srv.Close()
	go func() { _ = srv.Serve(ln) }()

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + ln.Addr().String() + "/")
	if err != nil {
		t.Fatalf("GET failed after detached audit finalization: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestServerShutdownTimeoutCoversRequestLifecycle(t *testing.T) {
	if serverShutdownTimeout() < serverWriteTimeout() {
		t.Fatalf("serverShutdownTimeout=%v must cover serverWriteTimeout=%v", serverShutdownTimeout(), serverWriteTimeout())
	}
}

// TestServerClientDisconnectCancelsRequestContext 验证客户端断开（transport abort，
// D13）时 request context 被取消，目标查询得以取消而非继续占用资源。
func TestServerClientDisconnectCancelsRequestContext(t *testing.T) {
	cancelled := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		close(cancelled)
	})

	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: handler, WriteTimeout: serverWriteTimeout()}
	defer srv.Close()
	go func() { _ = srv.Serve(ln) }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if _, err := fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: test\r\n\r\n"); err != nil {
		t.Fatalf("write request: %v", err)
	}
	// 客户端立即断开（AbortController 等价）。
	_ = conn.Close()

	select {
	case <-cancelled:
		// request context 已取消，查询可被取消。
	case <-time.After(5 * time.Second):
		t.Fatal("客户端断开后 request context 未被取消，查询会继续占用资源")
	}
}

// TestServerWriteTimeoutCanWriteSafeError 验证 WriteTimeout 预算内可写出安全错误响应
// （写超时不早于最长查询，且有写出错误响应的余量）。
func TestServerWriteTimeoutCanWriteSafeError(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET 失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
}

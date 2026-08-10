import { useEffect, useMemo, useReducer, useRef, useState } from "react";
import { ApiError, createApiClient, type WebDbApi } from "./api/client";
import type { ColumnDto, ConnectionDto, DatabaseEngine, QueryResponseDto, SchemaDto, TableDto, WireCell } from "./api/contracts";
import { initialWorkbenchState, workbenchReducer, type WorkbenchError } from "./state/workbench";
import "./workbench.css";

interface AppProps {
  api?: WebDbApi;
  workspaceId?: string;
}

type LoadStatus = "loading" | "ready" | "empty" | "error";
interface Loadable<T> {
  status: LoadStatus;
  data?: T;
  error?: WorkbenchError;
}

const defaultApi = createApiClient({ baseUrl: import.meta.env.VITE_API_BASE_URL ?? "/api/v1" });

function asWorkbenchError(error: unknown): WorkbenchError {
  if (error instanceof ApiError) return { code: error.code, message: error.message, retryAfterSeconds: error.retryAfterSeconds };
  if (typeof error === "object" && error !== null && "code" in error) {
    const candidate = error as { code?: unknown; message?: unknown; retryAfterSeconds?: unknown };
    return {
      code: typeof candidate.code === "string" ? candidate.code : "internal_error",
      message: typeof candidate.message === "string" ? candidate.message : "internal_error",
      retryAfterSeconds: typeof candidate.retryAfterSeconds === "number" ? candidate.retryAfterSeconds : undefined,
    };
  }
  return { code: "api_unreachable", message: "api_unreachable" };
}

function isAbort(error: unknown): boolean {
  return error instanceof DOMException && error.name === "AbortError";
}

function describeError(error: WorkbenchError): { title: string; description: string; action: string } {
  switch (error.code) {
    case "statement_not_allowed":
    case "read_not_allowed":
      return { title: "查询被只读策略拒绝", description: "当前连接只允许服务端认可的单条只读查询。", action: "返回编辑器修改 SQL" };
    case "sql_parse_error":
    case "unsupported_statement":
      return { title: "无法安全解析查询", description: "请检查当前数据库方言和 SQL 语法。", action: "返回编辑器修改 SQL" };
    case "multiple_statements":
      return { title: "一次只能运行一条语句", description: "请将 SQL 拆分后分别运行。", action: "返回编辑器修改 SQL" };
    case "query_timeout":
      return { title: "查询已超时", description: "查询超过连接策略规定的时间限制。", action: "重试查询" };
    case "query_cancelled":
      return { title: "查询已取消", description: "服务端已确认本次查询取消。", action: "重新运行" };
    case "rate_limited":
    case "connection_busy":
    case "pagination_capacity_exhausted":
      return {
        title: error.code === "connection_busy" ? "目标连接繁忙" : "请求受限",
        description: error.retryAfterSeconds === undefined ? "请稍后手动重试。" : `请在 ${error.retryAfterSeconds} 秒后手动重试。`,
        action: "重试查询",
      };
    case "invalid_page_token":
      return { title: "分页状态已失效", description: "当前结果保留；请从第一页重新运行查询。", action: "从第一页重新运行" };
    case "audit_failed":
      return { title: "无法完成审计", description: "为避免展示未审计结果，本次查询结果不予显示。不会自动重试。", action: "重新运行" };
    case "connection_unavailable":
      return { title: "连接暂不可用", description: "服务端暂时无法安全使用该连接。", action: "重试查询" };
    case "forbidden":
      return { title: "无权访问", description: "当前身份无权使用该资源。", action: "选择其他连接" };
    case "context_unavailable":
      return { title: "工作区上下文未配置", description: "此部署尚未提供可用的服务端工作区上下文。", action: "等待配置" };
    default:
      return { title: "服务暂时不可用", description: "未展示底层数据库或连接细节。", action: "重试查询" };
  }
}

function icon(name: "database" | "schema" | "table" | "column" | "play" | "stop" | "shield" | "chevron"): string {
  const icons = {
    database: "◉",
    schema: "◇",
    table: "▦",
    column: "│",
    play: "▶",
    stop: "■",
    shield: "✓",
    chevron: "›",
  } as const;
  return icons[name];
}

function environmentLabel(environment: ConnectionDto["environment"]): string {
  return environment.toUpperCase();
}

function displayCell(value: WireCell): { text: string; className: string } {
  if (value === null) return { text: "NULL", className: "cell-null" };
  if (value === "") return { text: "''（空字符串）", className: "cell-empty" };
  if (typeof value === "string" && value.trim() === "") return { text: `${"␠".repeat(value.length)}（空白）`, className: "cell-space" };
  return { text: String(value), className: "" };
}

function SqlEditor({ engine, value, onChange, onRun }: { engine?: DatabaseEngine; value: string; onChange: (value: string) => void; onRun: () => void }) {
  const container = useRef<HTMLDivElement>(null);
  const editor = useRef<{
    getValue(): string;
    setValue(nextValue: string): void;
    dispose(): void;
  } | undefined>(undefined);
  const changeRef = useRef(onChange);
  const runRef = useRef(onRun);
  const valueRef = useRef(value);
  changeRef.current = onChange;
  runRef.current = onRun;
  valueRef.current = value;

  useEffect(() => {
    let active = true;
    const languageDefinition = engine === "mysql"
      ? import("monaco-editor/languages/definitions/mysql/register")
      : import("monaco-editor/languages/definitions/pgsql/register");
    void Promise.all([import("monaco-editor/editor/editor.api"), languageDefinition]).then(([monaco]) => {
      if (!active || !container.current) return;
      monaco.editor.defineTheme("webdb-linear", {
        base: "vs-dark",
        inherit: true,
        rules: [],
        colors: {
          "editor.background": "#0f1011",
          "editorGutter.background": "#0f1011",
          "editor.lineHighlightBackground": "#191a1b",
          "editor.selectionBackground": "#343c73",
          "editorCursor.foreground": "#aeb7ff",
        },
      });
      const instance = monaco.editor.create(container.current, {
        value: valueRef.current,
        language: engine === "mysql" ? "mysql" : "pgsql",
        theme: "webdb-linear",
        automaticLayout: true,
        ariaLabel: "SQL 编辑器，按 Ctrl+Enter 或 Cmd+Enter 运行。语法高亮不代表已通过服务端安全裁决。",
        accessibilitySupport: "on",
        minimap: { enabled: false },
        fontFamily: "Berkeley Mono, Cascadia Code, Consolas, monospace",
        fontSize: 13,
        lineHeight: 20,
        lineNumbersMinChars: 3,
        scrollBeyondLastLine: false,
        tabSize: 2,
        padding: { top: 12, bottom: 12 },
      });
      instance.onDidChangeModelContent(() => changeRef.current(instance.getValue()));
      instance.addAction({
        id: "webdb.run-read-only-query",
        label: "Run read-only query",
        keybindings: [monaco.KeyMod.CtrlCmd | monaco.KeyCode.Enter],
        run: () => runRef.current(),
      });
      editor.current = instance;
    });
    return () => {
      active = false;
      editor.current?.dispose();
      editor.current = undefined;
    };
    // Recreate only when the backend-owned dialect changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [engine]);

  useEffect(() => {
    if (editor.current && editor.current.getValue() !== value) editor.current.setValue(value);
  }, [value]);

  return (
    <div className="sql-editor-shell">
      <div className="sql-editor" ref={container} />
      <textarea
        className="sql-editor-accessible-input"
        aria-label="SQL 编辑器，按 Ctrl+Enter 运行。语法高亮不代表已通过服务端安全裁决"
        value={value}
        onChange={(event) => onChange(event.target.value)}
        onKeyDown={(event) => {
          if ((event.ctrlKey || event.metaKey) && event.key === "Enter") {
            event.preventDefault();
            onRun();
          }
        }}
        spellCheck={false}
      />
    </div>
  );
}

export function App({ api = defaultApi, workspaceId = import.meta.env.VITE_WEBDB_WORKSPACE_ID ?? "" }: AppProps) {
  const [state, dispatch] = useReducer(workbenchReducer, initialWorkbenchState);
  const [connections, setConnections] = useState<Loadable<ConnectionDto[]>>({ status: "loading" });
  const [connectionReload, setConnectionReload] = useState(0);
  const [tables, setTables] = useState<Record<string, Loadable<TableDto[]>>>({});
  const [columns, setColumns] = useState<Record<string, Loadable<ColumnDto[]>>>({});
  const [activeOutputTab, setActiveOutputTab] = useState<"results" | "messages">("results");
  const [expandedSchemas, setExpandedSchemas] = useState<Set<string>>(new Set());
  const [expandedTables, setExpandedTables] = useState<Set<string>>(new Set());
  const [sql, setSql] = useState("SELECT\n  id\nFROM public.users\nORDER BY id\nLIMIT 100;");
  const connectionAbort = useRef<AbortController | undefined>(undefined);
  const schemaAbort = useRef<AbortController | undefined>(undefined);
  const runAbort = useRef<AbortController | undefined>(undefined);
  const pageAbort = useRef<AbortController | undefined>(undefined);
  const executionInFlight = useRef(false);
  const pageInFlight = useRef(false);
  const metadataAborts = useRef(new Set<AbortController>());
  const selectedConnection = useRef<string | undefined>(undefined);
  const generation = useRef(0);

  const abortMetadataRequests = () => {
    for (const controller of metadataAborts.current) controller.abort();
    metadataAborts.current.clear();
  };

  useEffect(() => {
    if (!workspaceId) {
      setConnections({ status: "error", error: { code: "context_unavailable", message: "context_unavailable" } });
      return;
    }
    const controller = new AbortController();
    connectionAbort.current?.abort();
    connectionAbort.current = controller;
    setConnections({ status: "loading" });
    void api.connections(workspaceId, controller.signal).then(
      (data) => {
        if (!controller.signal.aborted) setConnections({ status: data.length === 0 ? "empty" : "ready", data });
      },
      (error: unknown) => {
        if (!controller.signal.aborted) setConnections({ status: "error", error: asWorkbenchError(error) });
      },
    );
    return () => controller.abort();
  }, [api, workspaceId, connectionReload]);

  useEffect(
    () => () => {
      connectionAbort.current?.abort();
      abortMetadataRequests();
      runAbort.current?.abort();
      pageAbort.current?.abort();
    },
    [],
  );

  const selected = useMemo(() => connections.data?.find((connection) => connection.id === state.selectedConnectionId), [connections.data, state.selectedConnectionId]);

  const loadSchemas = (connectionId: string, currentGeneration: number) => {
    if (!workspaceId) return;
    schemaAbort.current?.abort();
    const controller = new AbortController();
    schemaAbort.current = controller;
    metadataAborts.current.add(controller);
    dispatch({ type: "schemasLoading", connectionId, generation: currentGeneration });
    void api.schemas(workspaceId, connectionId, controller.signal).then(
      (schemas) => {
        if (!controller.signal.aborted && currentGeneration === generation.current && selectedConnection.current === connectionId) {
          dispatch({ type: "schemasLoaded", connectionId, generation: currentGeneration, schemas });
        }
      },
      (error: unknown) => {
        if (!controller.signal.aborted && currentGeneration === generation.current && selectedConnection.current === connectionId) {
          dispatch({ type: "schemasFailed", connectionId, generation: currentGeneration, error: asWorkbenchError(error) });
        }
      },
    ).finally(() => metadataAborts.current.delete(controller));
  };

  const selectConnection = (connectionId: string) => {
    if (!workspaceId || connectionId === selectedConnection.current) return;
    abortMetadataRequests();
    runAbort.current?.abort();
    pageAbort.current?.abort();
    executionInFlight.current = false;
    pageInFlight.current = false;
    selectedConnection.current = connectionId;
    const currentGeneration = ++generation.current;
    dispatch({ type: "connectionSelected", connectionId });
    setTables({});
    setColumns({});
    setExpandedSchemas(new Set());
    setExpandedTables(new Set());
    loadSchemas(connectionId, currentGeneration);
  };

  const loadTables = (schema: SchemaDto) => {
    const connectionId = selectedConnection.current;
    if (!workspaceId || !connectionId) return;
    const key = schema.name;
    setExpandedSchemas((previous) => new Set(previous).add(key));
    if (tables[key]?.status === "ready" || tables[key]?.status === "loading") return;
    setTables((previous) => ({ ...previous, [key]: { status: "loading" } }));
    const requestGeneration = generation.current;
    const controller = new AbortController();
    metadataAborts.current.add(controller);
    void api.tables(workspaceId, connectionId, schema.name, controller.signal).then(
      (data) => {
        if (requestGeneration === generation.current && selectedConnection.current === connectionId) setTables((previous) => ({ ...previous, [key]: { status: data.length === 0 ? "empty" : "ready", data } }));
      },
      (error: unknown) => {
        if (!controller.signal.aborted && requestGeneration === generation.current && selectedConnection.current === connectionId) setTables((previous) => ({ ...previous, [key]: { status: "error", error: asWorkbenchError(error) } }));
      },
    ).finally(() => metadataAborts.current.delete(controller));
  };

  const loadColumns = (schema: string, table: TableDto) => {
    const connectionId = selectedConnection.current;
    if (!workspaceId || !connectionId) return;
    const key = `${schema}.${table.name}`;
    setExpandedTables((previous) => new Set(previous).add(key));
    if (columns[key]?.status === "ready" || columns[key]?.status === "loading") return;
    setColumns((previous) => ({ ...previous, [key]: { status: "loading" } }));
    const requestGeneration = generation.current;
    const controller = new AbortController();
    metadataAborts.current.add(controller);
    void api.columns(workspaceId, connectionId, schema, table.name, controller.signal).then(
      (data) => {
        if (requestGeneration === generation.current && selectedConnection.current === connectionId) setColumns((previous) => ({ ...previous, [key]: { status: data.length === 0 ? "empty" : "ready", data } }));
      },
      (error: unknown) => {
        if (!controller.signal.aborted && requestGeneration === generation.current && selectedConnection.current === connectionId) setColumns((previous) => ({ ...previous, [key]: { status: "error", error: asWorkbenchError(error) } }));
      },
    ).finally(() => metadataAborts.current.delete(controller));
  };

  const applyQueryResponse = (response: QueryResponseDto) => dispatch({ type: "executionSucceeded", result: response.data, page: response.meta.page, audit: response.meta.audit });

  const runQuery = () => {
    const connectionId = selectedConnection.current;
    if (!workspaceId || !connectionId || executionInFlight.current || pageInFlight.current) return;
    const requestGeneration = generation.current;
    const controller = new AbortController();
    runAbort.current?.abort();
    runAbort.current = controller;
    executionInFlight.current = true;
    dispatch({ type: "runStarted", generation: requestGeneration });
    void api.execute(workspaceId, { connection_id: connectionId, sql, page_size: 100 }, controller.signal).then(
      (response) => {
        if (!controller.signal.aborted && requestGeneration === generation.current && selectedConnection.current === connectionId) applyQueryResponse(response);
      },
      (error: unknown) => {
        if (controller.signal.aborted || isAbort(error)) return;
        if (requestGeneration === generation.current && selectedConnection.current === connectionId) {
          const failure = asWorkbenchError(error);
          dispatch({ type: "executionFailed", ...failure });
        }
      },
    ).finally(() => {
      if (runAbort.current === controller) executionInFlight.current = false;
    });
  };

  const cancelQuery = () => {
    if (state.execution.status !== "running") return;
    runAbort.current?.abort();
    executionInFlight.current = false;
    dispatch({ type: "runCancelledLocally" });
  };

  const loadNextPage = () => {
    const token = state.nextPageToken;
    if (!workspaceId || !token || executionInFlight.current || pageInFlight.current) return;
    const requestGeneration = generation.current;
    const controller = new AbortController();
    pageAbort.current?.abort();
    pageAbort.current = controller;
    pageInFlight.current = true;
    dispatch({ type: "nextPageStarted" });
    void api.nextPage(workspaceId, token, controller.signal).then(
      (response) => {
        if (!controller.signal.aborted && requestGeneration === generation.current) applyQueryResponse(response);
      },
      (error: unknown) => {
        if (controller.signal.aborted || isAbort(error)) return;
        if (requestGeneration === generation.current) {
          const failure = asWorkbenchError(error);
          dispatch({ type: "executionFailed", ...failure });
        }
      },
    ).finally(() => {
      if (pageAbort.current === controller) pageInFlight.current = false;
    });
  };

  const onTreeKeyDown = (event: React.KeyboardEvent<HTMLDivElement>) => {
    const rows = [...event.currentTarget.querySelectorAll<HTMLButtonElement>("[data-tree-row]")].filter((row) => row.getClientRects().length > 0);
    const activeRow = document.activeElement as HTMLButtonElement;
    const index = rows.indexOf(activeRow);
    if (index < 0) return;
    if (event.key === "ArrowDown" || event.key === "ArrowUp" || event.key === "Home" || event.key === "End") {
      event.preventDefault();
      const target = event.key === "Home" ? 0 : event.key === "End" ? rows.length - 1 : Math.max(0, Math.min(rows.length - 1, index + (event.key === "ArrowDown" ? 1 : -1)));
      rows[target]?.focus();
      return;
    }
    if (event.key === "ArrowRight" && activeRow.getAttribute("aria-expanded") === "false") {
      event.preventDefault();
      activeRow.click();
      return;
    }
    if (event.key === "ArrowLeft") {
      event.preventDefault();
      if (activeRow.getAttribute("aria-expanded") === "true") {
        activeRow.click();
        return;
      }
      let parent = activeRow.parentElement;
      while (parent) {
        const previous = parent.previousElementSibling;
        if (previous instanceof HTMLButtonElement && previous.matches("[data-tree-row]")) {
          previous.focus();
          return;
        }
        parent = parent.parentElement;
      }
    }
  };

  const executionError = state.execution.status === "failed" ? state.error : undefined;
  const retainResultAfterError = executionError !== undefined && executionError.code !== "audit_failed" && state.result;

  return (
    <div className="workbench">
      <header className="topbar" aria-label="执行上下文">
        <div className="brand"><span aria-hidden="true">{icon("database")}</span> WebDB</div>
        <div className="context">
          <strong>{selected?.name ?? "选择已授权连接"}</strong>
          {selected && <span className="mono">{selected.engine === "postgresql" ? "PostgreSQL" : "MySQL"}</span>}
          {selected && <span className={`badge environment-${selected.environment}`}>{environmentLabel(selected.environment)}</span>}
          <span className="badge readonly"><span aria-hidden="true">{icon("shield")}</span> READ ONLY</span>
        </div>
        <div className="topbar-spacer" />
        <span className={`api-status ${connections.status === "error" ? "api-status-error" : ""}`} aria-live="polite"><span aria-hidden="true">●</span> {connections.status === "loading" ? "API 加载中" : connections.status === "error" ? "API 不可达" : "API 正常"}</span>
      </header>

      <div className="workbench-body">
        <aside className="resource-sidebar" aria-label="连接与授权 Schema">
          <div className="sidebar-heading"><span>连接 / Schema</span><span className="sidebar-hint">已授权资源</span></div>
          <div className="tree" role="tree" aria-label="数据库连接和 Schema 树" onKeyDown={onTreeKeyDown}>
            {connections.status === "loading" && <p className="tree-message" aria-live="polite">正在加载已授权连接…</p>}
            {connections.status === "empty" && <p className="tree-message">当前没有可用连接。</p>}
            {connections.status === "error" && <InlineError error={connections.error} onRetry={() => setConnectionReload((attempt) => attempt + 1)} />}
            {connections.data?.map((connection) => {
              const isSelected = connection.id === state.selectedConnectionId;
              return (
                <div key={connection.id} role="none">
                  <button data-tree-row role="treeitem" aria-level={1} aria-selected={isSelected} aria-expanded={isSelected} className={`tree-row ${isSelected ? "tree-row-selected" : ""}`} type="button" onClick={() => selectConnection(connection.id)}>
                    <span className="tree-symbol" aria-hidden="true">{isSelected ? "⌄" : icon("chevron")}</span><span aria-hidden="true">{icon("database")}</span><span className="tree-label">{connection.name}</span><span className="tree-meta">{connection.engine === "postgresql" ? "PG" : "MY"}</span>
                  </button>
                  {isSelected && <div role="group">
                    {state.schemaStatus === "loading" && <p className="tree-message nested" aria-live="polite">正在加载授权 Schema…</p>}
                    {state.schemaStatus === "empty" && <p className="tree-message nested">此连接没有可见 Schema。</p>}
                    {state.schemaStatus === "error" && <InlineError error={state.error} onRetry={() => loadSchemas(connection.id, generation.current)} />}
                    {state.schemas.map((schema) => {
                      const schemaOpen = expandedSchemas.has(schema.name);
                      const schemaTables = tables[schema.name];
                      return (
                        <div key={schema.name} role="none">
                          <button data-tree-row role="treeitem" aria-level={2} aria-expanded={schemaOpen} className="tree-row level-1" type="button" onClick={() => { if (schemaOpen) setExpandedSchemas((previous) => { const next = new Set(previous); next.delete(schema.name); return next; }); else loadTables(schema); }}>
                            <span className="tree-symbol" aria-hidden="true">{schemaOpen ? "⌄" : icon("chevron")}</span><span aria-hidden="true">{icon("schema")}</span><span className="tree-label">{schema.name}</span>
                          </button>
                          {schemaOpen && <div role="group">
                            {schemaTables?.status === "loading" && <p className="tree-message nested">正在加载表…</p>}
                            {schemaTables?.status === "empty" && <p className="tree-message nested">没有可见表或视图。</p>}
                            {schemaTables?.status === "error" && <InlineError error={schemaTables.error} onRetry={() => loadTables(schema)} />}
                            {schemaTables?.data?.map((table) => {
                              const tableKey = `${schema.name}.${table.name}`;
                              const tableOpen = expandedTables.has(tableKey);
                              const tableColumns = columns[tableKey];
                              return (
                                <div key={tableKey} role="none">
                                  <button data-tree-row role="treeitem" aria-level={3} aria-expanded={tableOpen} className="tree-row level-2" type="button" onClick={() => { if (tableOpen) setExpandedTables((previous) => { const next = new Set(previous); next.delete(tableKey); return next; }); else loadColumns(schema.name, table); }}>
                                    <span className="tree-symbol" aria-hidden="true">{tableOpen ? "⌄" : icon("chevron")}</span><span aria-hidden="true">{icon("table")}</span><span className="tree-label">{table.name}</span><span className="tree-meta">{table.type}</span>
                                  </button>
                                  {tableOpen && <div role="group">
                                    {tableColumns?.status === "loading" && <p className="tree-message nested">正在加载列…</p>}
                                    {tableColumns?.status === "error" && <InlineError error={tableColumns.error} onRetry={() => loadColumns(schema.name, table)} />}
                                    {tableColumns?.data?.map((column) => <button data-tree-row role="treeitem" aria-level={4} className="tree-row level-3" type="button" key={column.name}><span className="tree-symbol" aria-hidden="true" /><span aria-hidden="true">{icon("column")}</span><span className="tree-label">{column.name}</span><span className="tree-meta">{column.native_type}{column.nullable ? "?" : ""}</span></button>)}
                                  </div>}
                                </div>
                              );
                            })}
                          </div>}
                        </div>
                      );
                    })}
                  </div>}
                </div>
              );
            })}
          </div>
        </aside>

        <main className="query-workspace">
          <section className="query-toolbar" aria-label="查询工具栏">
            <strong>未命名查询</strong><span className="policy-note"><span aria-hidden="true">{icon("shield")}</span> 单条只读查询 · 安全策略由服务端裁决</span><span className="toolbar-spacer" />
            <kbd>Ctrl+Enter</kbd>
            <button className={`button ${state.execution.status === "running" ? "button-warning" : "button-primary"}`} type="button" onClick={state.execution.status === "running" ? cancelQuery : runQuery} disabled={!selected || state.execution.status === "loading-next-page"} aria-keyshortcuts="Control+Enter Meta+Enter">
              <span aria-hidden="true">{state.execution.status === "running" ? icon("stop") : icon("play")}</span>{state.execution.status === "running" ? "取消查询" : "运行查询"}
            </button>
          </section>
          <section className="editor-panel" aria-label="SQL 编辑器">
            <SqlEditor engine={selected?.engine} value={sql} onChange={setSql} onRun={runQuery} />
            <div className="editor-status"><span>{selected?.engine === "mysql" ? "MySQL" : "PostgreSQL"}</span><span>UTF-8</span><span>语法高亮仅用于阅读</span></div>
          </section>
          <section className="output-panel" aria-label="查询输出">
            <div className="output-tabs" role="tablist" aria-label="输出标签">
              <button id="results-tab" className={`tab ${activeOutputTab === "results" ? "tab-active" : ""}`} role="tab" aria-selected={activeOutputTab === "results"} aria-controls="results-panel" type="button" onClick={() => setActiveOutputTab("results")}>结果</button>
              <button id="messages-tab" className={`tab ${activeOutputTab === "messages" ? "tab-active" : ""}`} role="tab" aria-selected={activeOutputTab === "messages"} aria-controls="messages-panel" type="button" onClick={() => setActiveOutputTab("messages")}>消息</button>
            </div>
            <div className="execution-summary" aria-live="polite">
              {state.execution.status === "running" && <><strong>正在执行</strong><span>等待服务端返回终态</span></>}
              {state.execution.status === "loading-next-page" && <><strong>正在加载下一页</strong><span>保留当前结果</span></>}
              {state.execution.status === "cancelled" && <><strong>已取消</strong><span>浏览器已中断请求；未等待 query_cancelled 响应。</span></>}
              {state.execution.status === "succeeded" && <><strong>执行成功</strong><span>{state.result?.returned_rows ?? 0} 行 · Execution {state.audit?.execution_id} · 审计已记录</span></>}
              {state.execution.status === "idle" && <><strong>准备就绪</strong><span>SQL 已保留，等待运行</span></>}
              {state.execution.status === "failed" && <><strong>执行未完成</strong><span>{state.error?.code}</span></>}
            </div>
            <div id={activeOutputTab === "results" ? "results-panel" : "messages-panel"} className="output-content" role="tabpanel" aria-labelledby={activeOutputTab === "results" ? "results-tab" : "messages-tab"}>
              {activeOutputTab === "results" && <>
                {executionError && <ErrorPanel error={executionError} onAction={runQuery} />}
                {!executionError && state.execution.status === "idle" && <EmptyState title="编辑器已准备就绪" detail="运行后，服务端返回的只读结果或结构化消息会显示在这里。前端不判断 SQL 是否安全。" />}
                {!executionError && state.execution.status === "cancelled" && <EmptyState title="查询已取消" detail="SQL 已保留，可在准备好后重新运行。" />}
                {!executionError && state.execution.status === "running" && <EmptyState title="正在执行查询" detail="正在等待服务端策略裁决和数据库响应。" />}
                {(state.execution.status === "succeeded" || retainResultAfterError) && state.result && <ResultGrid result={state.result} />}
              </>}
              {activeOutputTab === "messages" && <AuditReceipt audit={state.audit} />}
            </div>
            <footer className="pagination-bar">
              {state.execution.status === "loading-next-page" ? <span>正在加载下一页…</span> : state.nextPageToken ? <><span>已显示 1–{state.result?.total_returned ?? state.result?.returned_rows ?? 0} 行</span><span className="toolbar-spacer" /><button className="button" type="button" onClick={loadNextPage}>加载下一页 →</button></> : <span>{state.result ? `已显示 ${state.result.returned_rows} 行` : "当前没有可用分页操作"}</span>}
            </footer>
          </section>
        </main>
      </div>
    </div>
  );
}

function InlineError({ error, onRetry }: { error?: WorkbenchError; onRetry: () => void }) {
  if (!error) return null;
  return <div className="tree-error"><span>{describeError(error).title}</span><button type="button" onClick={onRetry}>重试</button></div>;
}

function EmptyState({ title, detail }: { title: string; detail: string }) {
  return <div className="empty-state"><div><h2>{title}</h2><p>{detail}</p></div></div>;
}

function ErrorPanel({ error, onAction }: { error: WorkbenchError; onAction: () => void }) {
  const presentation = describeError(error);
  return <div className="message-panel message-panel-error" role="alert" tabIndex={-1}><h2>{presentation.title}</h2><p>{presentation.description}</p><p className="error-code">错误码：{error.code}</p>{error.code !== "audit_failed" && error.code !== "context_unavailable" && <button className="button" type="button" onClick={onAction}>{presentation.action}</button>}</div>;
}

function AuditReceipt({ audit }: { audit?: QueryResponseDto["meta"]["audit"] }) {
  if (!audit) return <EmptyState title="暂无审计回执" detail="仅在服务端成功记录本页审计后展示回执；前端不会据此推断额外权限。" />;
  return <dl className="audit-receipt" aria-label="服务端审计回执">
    <div><dt>状态</dt><dd>{audit.state}</dd></div><div><dt>结果</dt><dd>{audit.outcome}</dd></div>
    <div><dt>审计事件</dt><dd>{audit.audit_event_id}</dd></div><div><dt>执行</dt><dd>{audit.execution_id}</dd></div>
    <div><dt>追踪</dt><dd>{audit.trace_id}</dd></div>
  </dl>;
}

function ResultGrid({ result }: { result: QueryResponseDto["data"] }) {
  if (result.rows.length === 0) return <EmptyState title="查询成功，返回 0 行" detail="这不是错误。请修改查询条件后重新运行，或继续保留当前 SQL。" />;
  return <div className="result-scroll"><table aria-label="只读查询结果"><thead><tr>{result.columns.map((column) => <th scope="col" key={column.name}>{column.name}<span>{column.wire_type}</span></th>)}</tr></thead><tbody>{result.rows.slice(0, 500).map((row, rowIndex) => <tr key={rowIndex}>{row.map((cell, columnIndex) => { const rendered = displayCell(cell); return <td tabIndex={0} key={columnIndex} className={rendered.className} title={rendered.text}>{rendered.text}</td>; })}</tr>)}</tbody></table></div>;
}

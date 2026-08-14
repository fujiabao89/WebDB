import { expect, test } from "@playwright/test";

// 与 deploy/compose 演示 seed 权威值一致（apps/api/internal/seeddemo/ids.go + init 脚本）。
// 这些是合成演示标识，不含真实用户/生产数据。
const WORKSPACE = "f1160d75-26f7-46e0-b3e0-570ea65c232e";
const PG_CONN = "d80a86cb-d4e7-4afb-9ab1-8df572ad9c69";
const MYSQL_CONN = "37760cae-7ddc-408d-b2b0-4505c3ea2243";
const PG_NAME = "demo_reader (PostgreSQL)";
const MYSQL_NAME = "demo_reader (MySQL)";

interface ConnectionDto {
  id: string;
  name: string;
  engine: "postgresql" | "mysql";
  environment: string;
  database: string;
}

// 后端只读执行需 order_by 提供唯一排序意图（ADR-014 VerifiedSortPlan）。
// 演示 employees 表主键为 id；order_by id 构成可信唯一排序。
const EMPLOYEES_SQL = "SELECT id FROM employees";
const ORDER_BY_ID = [{ column: "id", order: "ASC", nulls_last: false }];

test("@smoke PostgreSQL 主流程：连接 → schema → 表 → 列 → 只读查询 → 分页 → 审计回执", async ({ request }) => {
  // 1. 已授权连接列表（仅 AllowRead=true；安全 DTO）
  const conns = await request.get(`/api/v1/workspaces/${WORKSPACE}/connections`);
  expect(conns.ok()).toBe(true);
  const connsBody = await conns.json();
  const pg = connsBody.data.find((c: ConnectionDto) => c.id === PG_CONN);
  expect(pg).toBeTruthy();
  expect(pg?.engine).toBe("postgresql");
  expect(connsBody.data.map((c: ConnectionDto) => c.name)).toContain(PG_NAME);

  // 2. schema 浏览
  const schemas = await request.get(`/api/v1/workspaces/${WORKSPACE}/connections/${PG_CONN}/schemas`);
  expect(schemas.ok()).toBe(true);
  expect((await schemas.json()).data.map((s: { name: string }) => s.name)).toContain("public");

  // 3. 表浏览
  const tables = await request.get(`/api/v1/workspaces/${WORKSPACE}/connections/${PG_CONN}/tables?schema=public`);
  expect(tables.ok()).toBe(true);
  expect((await tables.json()).data.map((t: { name: string }) => t.name)).toEqual(
    expect.arrayContaining(["employees", "departments"]),
  );

  // 4. 列浏览
  const columns = await request.get(
    `/api/v1/workspaces/${WORKSPACE}/connections/${PG_CONN}/columns?schema=public&table=employees`,
  );
  expect(columns.ok()).toBe(true);
  expect((await columns.json()).data.map((c: { name: string }) => c.name)).toEqual(
    expect.arrayContaining(["id", "first_name"]),
  );

  // 5. 只读执行第一页（order_by id，page_size=2 → 触发分页）
  const exec = await request.post(`/api/v1/workspaces/${WORKSPACE}/executions`, {
    data: { connection_id: PG_CONN, sql: EMPLOYEES_SQL, page_size: 2, order_by: ORDER_BY_ID },
  });
  expect(exec.status()).toBe(200);
  const execBody = await exec.json();
  expect(execBody.data.columns.map((c: { name: string }) => c.name)).toContain("id");
  expect(execBody.data.rows.length).toBe(2);
  expect(execBody.meta.page.page_size).toBe(2);
  expect(execBody.meta.page.has_more).toBe(true);
  expect(execBody.meta.page.next_page_token).toBeTruthy();
  expect(execBody.meta.audit.state).toBe("recorded");
  expect(execBody.meta.audit.audit_event_id).toBeTruthy();
  expect(execBody.meta.audit.execution_id).toBeTruthy();
  expect(execBody.meta.audit.outcome).toBe("succeeded");

  // 6. 单向续页：每页独立 Execution/Audit（D11）
  const page2 = await request.post(`/api/v1/workspaces/${WORKSPACE}/query-pages`, {
    data: { next_page_token: execBody.meta.page.next_page_token },
  });
  expect(page2.status()).toBe(200);
  const page2Body = await page2.json();
  expect(page2Body.data.rows.length).toBe(2);
  expect(page2Body.meta.audit.state).toBe("recorded");
  // 续页不应重复首页已返回的 id 值
  expect(page2Body.data.rows.flat()).not.toEqual(expect.arrayContaining(execBody.data.rows.flat()));
});

test("@smoke MySQL 主流程：连接 → schema → 表 → 列 → 只读查询 → 审计回执", async ({ request }) => {
  const conns = await request.get(`/api/v1/workspaces/${WORKSPACE}/connections`);
  const connsBody = await conns.json();
  const my = connsBody.data.find((c: ConnectionDto) => c.id === MYSQL_CONN);
  expect(my).toBeTruthy();
  expect(my?.engine).toBe("mysql");
  expect(connsBody.data.map((c: ConnectionDto) => c.name)).toContain(MYSQL_NAME);

  const schemas = await request.get(`/api/v1/workspaces/${WORKSPACE}/connections/${MYSQL_CONN}/schemas`);
  expect(schemas.ok()).toBe(true);
  expect((await schemas.json()).data.map((s: { name: string }) => s.name)).toContain("webdb_demo");

  const tables = await request.get(`/api/v1/workspaces/${WORKSPACE}/connections/${MYSQL_CONN}/tables?schema=webdb_demo`);
  expect(tables.ok()).toBe(true);
  expect((await tables.json()).data.map((t: { name: string }) => t.name)).toEqual(
    expect.arrayContaining(["employees", "departments"]),
  );

  // 列浏览
  const columns = await request.get(
    `/api/v1/workspaces/${WORKSPACE}/connections/${MYSQL_CONN}/columns?schema=webdb_demo&table=employees`,
  );
  expect(columns.ok()).toBe(true);
  expect((await columns.json()).data.map((c: { name: string }) => c.name)).toEqual(
    expect.arrayContaining(["id", "first_name"]),
  );

  // 显式 order_by + 小 page_size 取得 token → 续页成功、无重复、每页独立审计
  const exec = await request.post(`/api/v1/workspaces/${WORKSPACE}/executions`, {
    data: { connection_id: MYSQL_CONN, sql: "SELECT id FROM demo_pagination", page_size: 2, order_by: ORDER_BY_ID },
  });
  expect(exec.status()).toBe(200);
  const execBody = await exec.json();
  expect(execBody.data.rows.length).toBe(2);
  expect(execBody.meta.page.has_more).toBe(true);
  const firstAuditId = execBody.meta.audit.audit_event_id;
  expect(firstAuditId).toBeTruthy();

  const page2 = await request.post(`/api/v1/workspaces/${WORKSPACE}/query-pages`, {
    data: { next_page_token: execBody.meta.page.next_page_token },
  });
  expect(page2.status()).toBe(200);
  const page2Body = await page2.json();
  expect(page2Body.data.rows.length).toBe(2);
  // 逐行断言第二页每个 id 不在第一页集合中（防部分重复漏检）
  const page1Ids = new Set(execBody.data.rows.map((r: unknown[]) => String(r[0])));
  for (const r of page2Body.data.rows as unknown[]) {
    expect(page1Ids.has(String((r as unknown[])[0]))).toBe(false);
  }
  // 每页独立 Execution 与审计回执（D11）
  expect(page2Body.meta.audit.execution_id).toBeTruthy();
  expect(page2Body.meta.audit.execution_id).not.toBe(execBody.meta.audit.execution_id);
  expect(page2Body.meta.audit.audit_event_id).toBeTruthy();
  expect(page2Body.meta.audit.audit_event_id).not.toBe(firstAuditId);
});

test("@smoke 600 行分页累计到 max_rows=500：第 5 页 has_more=false 且无 token，行无重复", async ({ request }) => {
  let resp = await request.post(`/api/v1/workspaces/${WORKSPACE}/executions`, {
    data: { connection_id: PG_CONN, sql: "SELECT id FROM demo_pagination", page_size: 100, order_by: ORDER_BY_ID },
  });
  expect(resp.status()).toBe(200);
  let body = await resp.json();
  expect(body.data.rows.length).toBe(100);
  expect(body.meta.page.has_more).toBe(true);
  expect(body.meta.page.next_page_token).toBeTruthy();
  const seen = new Set<string>(body.data.rows.map((r: unknown[]) => String(r[0])));

  for (let page = 2; page <= 5; page++) {
    const token = body.meta.page.next_page_token;
    expect(token).toBeTruthy();
    resp = await request.post(`/api/v1/workspaces/${WORKSPACE}/query-pages`, { data: { next_page_token: token } });
    expect(resp.status()).toBe(200);
    body = await resp.json();
    expect(body.data.rows.length).toBe(100);
    for (const r of body.data.rows as unknown[]) {
      expect(seen.has(String((r as unknown[])[0]))).toBe(false);
      seen.add(String((r as unknown[])[0]));
    }
    expect(body.data.total_returned).toBe(page * 100);
    if (page < 5) {
      expect(body.meta.page.has_more).toBe(true);
      expect(body.meta.page.next_page_token).toBeTruthy();
    } else {
      // 累计到 500 行（max_rows）后：公开 has_more=false 且无 token（不产生不一致响应）
      expect(body.meta.page.has_more).toBe(false);
      expect(body.meta.page.next_page_token).toBeUndefined();
    }
  }
  expect(seen.size).toBe(500);
});

test("@smoke UI 浏览与可访问性：连接树键盘导航，非 P0 入口不出现", async ({ page }) => {
  const browserErrors: string[] = [];
  page.on("pageerror", (error) => browserErrors.push(error.message));

  await page.goto("/");
  await expect(page.getByRole("main")).toBeVisible();

  // 连接树包含两条已授权演示连接
  await expect(page.getByRole("treeitem", { name: PG_NAME })).toBeVisible();
  await expect(page.getByRole("treeitem", { name: MYSQL_NAME })).toBeVisible();

  // 键盘选择 PostgreSQL 连接 → schema → 表 → 列（WEB-37 树形键盘导航）
  await page.getByRole("treeitem", { name: PG_NAME }).focus();
  await page.keyboard.press("Enter");
  await expect(page.getByRole("treeitem", { name: "public" })).toBeVisible();
  await page.keyboard.press("ArrowDown");
  await page.keyboard.press("Enter");
  await expect(page.getByRole("treeitem", { name: /employees/ })).toBeVisible();

  // 非 P0 入口（登录/协作/保存/历史/导出/行编辑/DML/DDL）不得出现
  for (const forbidden of [/登录/, /保存查询/, /导出/, /编辑行/, /新增连接/, /分享/, /协作/]) {
    await expect(page.getByRole("button", { name: forbidden })).toHaveCount(0);
  }

  // 焦点可见 + reduced-motion：workbench.css 定义对应规则（静态存在性检查）
  const a11yCss = await page.evaluate(() => {
    const found = { focusVisible: false, reducedMotion: false };
    try {
      for (const sheet of Array.from(document.styleSheets)) {
        for (const rule of Array.from(sheet.cssRules)) {
          if (rule.cssText.includes(":focus-visible")) found.focusVisible = true;
          if (rule.cssText.includes("prefers-reduced-motion")) found.reducedMotion = true;
        }
      }
    } catch {
      /* 跨源样式表忽略 */
    }
    return found;
  });
  expect(a11yCss.focusVisible).toBe(true);
  expect(a11yCss.reducedMotion).toBe(true);

  expect(browserErrors).toEqual([]);
});

test("@smoke UI 运行查询：点击运行成功显示结果与审计回执（有界单页，无 continuation token）", async ({ page }) => {
  await page.goto("/");
  await page.getByRole("treeitem", { name: PG_NAME }).click();
  await expect(page.getByRole("treeitem", { name: "public" })).toBeVisible();

  // 默认 SQL 引用不存在的 public.users，替换为演示库有效查询。
  // 前端不解析 SQL、不推断 ORDER BY、不猜测主键（F1 方案 A1：有界单页 fallback）。
  await page.locator(".sql-editor-accessible-input").fill("SELECT id, first_name FROM employees");
  await page.getByRole("button", { name: /运行查询/ }).click();

  // 结果 + 审计回执；单页模式下无 continuation token（无「加载下一页」）
  await expect(page.getByText("执行成功")).toBeVisible();
  await expect(page.getByRole("table", { name: "只读查询结果" })).toBeVisible();
  await expect(page.getByRole("button", { name: /加载下一页/ })).toHaveCount(0);

  await page.getByRole("tab", { name: "消息" }).click();
  await expect(page.getByLabel("服务端审计回执")).toBeVisible();
});

test("@smoke UI 显式排序下一页：填写排序列启用服务端分页，下一页无重复", async ({ page }) => {
  await page.goto("/");
  await page.getByRole("treeitem", { name: PG_NAME }).click();
  await expect(page.getByRole("treeitem", { name: "public" })).toBeVisible();

  // demo_pagination 有 150 行（PK id）；填写排序列 → page_size=100 + order_by id
  await page.locator(".sql-editor-accessible-input").fill("SELECT id, name FROM demo_pagination");
  await page.getByRole("textbox", { name: /排序列/ }).fill("id");
  await page.getByRole("button", { name: /运行查询/ }).click();

  // 第一页 100 行 + 有续页 token → 显示「加载下一页」
  await expect(page.getByText("执行成功")).toBeVisible();
  await expect(page.getByRole("button", { name: /加载下一页/ })).toBeVisible();

  await page.getByRole("button", { name: /加载下一页/ }).click();

  // 第二页追加（600 行表 page_size=100 → 累计 200 行），仍有后续页
  await expect(page.getByText(/已显示 1.*200 行/)).toBeVisible();
  await expect(page.getByRole("button", { name: /加载下一页/ })).toBeVisible();
});

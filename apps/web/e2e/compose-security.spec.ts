import { expect, test } from "@playwright/test";

// 与 deploy/compose 演示 seed 权威值一致（合成演示标识）。
const WORKSPACE = "f1160d75-26f7-46e0-b3e0-570ea65c232e";
const PG_CONN = "d80a86cb-d4e7-4afb-9ab1-8df572ad9c69";
const MYSQL_CONN = "37760cae-7ddc-408d-b2b0-4505c3ea2243";
const FOREIGN_CONN = "aaaaaaa3-0000-4000-8000-000000000003";

const ORDER_BY_ID = [{ column: "id", order: "ASC", nulls_last: false }];

// 敏感 DTO 字段：P0-06A §3/§6/§7（D02/D03）——浏览器绝不接收这些内部字段。
const FORBIDDEN_DTO_FIELDS = ["host", "port", "secret_ref", "secret_version", "created_by", "workspace_id", "user_id"];

function assertNoForbiddenFields(body: string) {
  for (const field of FORBIDDEN_DTO_FIELDS) {
    expect(body).not.toContain(`"${field}"`);
  }
}

test("@smoke 连接/Schema DTO 不泄露 host/port/secret_ref/secret_version/created_by/workspace_id", async ({ request }) => {
  const conns = await request.get(`/api/v1/workspaces/${WORKSPACE}/connections`);
  expect(conns.ok()).toBe(true);
  assertNoForbiddenFields(await conns.text());

  const schemas = await request.get(`/api/v1/workspaces/${WORKSPACE}/connections/${PG_CONN}/schemas`);
  expect(schemas.ok()).toBe(true);
  assertNoForbiddenFields(await schemas.text());

  const columns = await request.get(
    `/api/v1/workspaces/${WORKSPACE}/connections/${PG_CONN}/columns?schema=public&table=employees`,
  );
  expect(columns.ok()).toBe(true);
  assertNoForbiddenFields(await columns.text());
});

test("@smoke DML/DDL/多语句由服务端拒绝，错误为稳定脱敏摘要（不含 SQL/原始数据库错误）", async ({ request }) => {
  const cases = [
    { sql: "INSERT INTO employees (id, first_name) VALUES (9999, 'x')", code: "statement_not_allowed" },
    { sql: "CREATE TABLE injected (id INT)", code: "statement_not_allowed" },
    { sql: "UPDATE employees SET first_name = 'x'", code: "statement_not_allowed" },
    { sql: "DROP TABLE employees", code: "statement_not_allowed" },
    { sql: "SELECT 1; SELECT 2", code: "multiple_statements" },
  ];
  for (const { sql, code } of cases) {
    const res = await request.post(`/api/v1/workspaces/${WORKSPACE}/executions`, {
      data: { connection_id: PG_CONN, sql },
    });
    expect(res.status()).toBe(422);
    const body = await res.json();
    expect(body.error.code).toBe(code);
    const raw = JSON.stringify(body);
    expect(raw).not.toContain("INSERT");
    expect(raw).not.toContain("CREATE TABLE");
    expect(raw).not.toContain("demo_reader"); // 不泄露目标库用户名
  }
});

test("@smoke MySQL 可执行注释在 AST 前拒绝（executable_comment_detected）", async ({ request }) => {
  const res = await request.post(`/api/v1/workspaces/${WORKSPACE}/executions`, {
    data: { connection_id: MYSQL_CONN, sql: "SELECT /*!50000 1*/ id FROM employees" },
  });
  expect(res.status()).toBe(422);
  const body = await res.json();
  expect(body.error.code).toBe("executable_comment_detected");
  expect(JSON.stringify(body)).not.toContain("/*!");
});

test("@smoke 分页 token 篡改 → invalid_page_token；错误体不含 token", async ({ request }) => {
  // 先取一个真实 token（order_by id，page_size=1 → has_more）
  const exec = await request.post(`/api/v1/workspaces/${WORKSPACE}/executions`, {
    data: { connection_id: PG_CONN, sql: "SELECT id FROM employees", page_size: 1, order_by: ORDER_BY_ID },
  });
  expect(exec.status()).toBe(200);
  const execBody = await exec.json();
  const token: string = execBody.meta.page.next_page_token;
  expect(token).toBeTruthy();

  // 篡改 token 首个十六进制字符
  const tampered = (token.startsWith("0") ? "1" : "0") + token.slice(1);
  const res = await request.post(`/api/v1/workspaces/${WORKSPACE}/query-pages`, {
    data: { next_page_token: tampered },
  });
  expect(res.status()).toBe(400);
  const body = await res.json();
  expect(body.error.code).toBe("invalid_page_token");
  expect(JSON.stringify(body)).not.toContain(token);
});

test("@smoke 分页 token 重放 → invalid_page_token（单次使用，claim 后不恢复）", async ({ request }) => {
  const exec = await request.post(`/api/v1/workspaces/${WORKSPACE}/executions`, {
    data: { connection_id: PG_CONN, sql: "SELECT id FROM employees", page_size: 1, order_by: ORDER_BY_ID },
  });
  expect(exec.status()).toBe(200);
  const execBody = await exec.json();
  const token: string = execBody.meta.page.next_page_token;
  expect(token).toBeTruthy();

  // 第一次续页成功（claim + rotate，旧 token 永久失效）
  const first = await request.post(`/api/v1/workspaces/${WORKSPACE}/query-pages`, {
    data: { next_page_token: token },
  });
  expect(first.status()).toBe(200);

  // 重放同一旧 token → 已 claim/rotate，拒绝
  const replay = await request.post(`/api/v1/workspaces/${WORKSPACE}/query-pages`, {
    data: { next_page_token: token },
  });
  expect(replay.status()).toBe(400);
  expect((await replay.json()).error.code).toBe("invalid_page_token");
});

test("@smoke 浏览器不持久化 token/凭证：localStorage 与 sessionStorage 为空，URL 不含敏感值", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByRole("main")).toBeVisible();

  const storage = await page.evaluate(() => ({
    local: Object.keys(window.localStorage),
    session: Object.keys(window.sessionStorage),
  }));
  expect(storage.local).toEqual([]);
  expect(storage.session).toEqual([]);
  expect(page.url()).not.toMatch(/token|password|secret|kek|connection|host|port/i);
});

test("@smoke 跨 workspace 与不可见 connection 防枚举：真实 foreign 连接与随机 ID 同返回 connection_not_found", async ({ request }) => {
  // 路径 workspace 与可信 Principal 不匹配（合法 UUID 但非演示 workspace）→ 403 forbidden
  const wrongWs = await request.get(`/api/v1/workspaces/00000000-0000-0000-0000-000000000000/connections`);
  expect(wrongWs.status()).toBe(403);
  expect((await wrongWs.json()).error.code).toBe("forbidden");

  // 真实 foreign 连接（属于第二合成 workspace）与随机不存在 ID 均返回相同的脱敏 connection_not_found
  const foreign = await request.get(`/api/v1/workspaces/${WORKSPACE}/connections/${FOREIGN_CONN}/schemas`);
  const random = await request.get(`/api/v1/workspaces/${WORKSPACE}/connections/99999999-9999-4999-8999-999999999999/schemas`);
  expect(foreign.status()).toBe(404);
  expect(random.status()).toBe(404);
  expect((await foreign.json()).error.code).toBe("connection_not_found");
  expect((await random.json()).error.code).toBe("connection_not_found");

  // 连接列表不出现 foreign 对象
  const list = await request.get(`/api/v1/workspaces/${WORKSPACE}/connections`);
  const listBody = await list.json();
  expect(listBody.data.map((c: { id: string }) => c.id)).not.toContain(FOREIGN_CONN);

  // 响应不得泄露另一个 workspace / 连接 / host / port / secret_ref 等内部字段
  const raw = JSON.stringify(await foreign.json());
  for (const field of ["host", "port", "secret_ref", "secret_version", "workspace_id", "created_by", "user_id"]) {
    expect(raw).not.toContain(`"${field}"`);
  }
});

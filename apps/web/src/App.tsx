import { useEffect, useState } from "react";

export function App() {
  const [apiStatus, setApiStatus] = useState("检查中…");

  useEffect(() => {
    // VITE_API_URL 存在时直达 API（非代理场景），否则走 Vite proxy /api
    const baseUrl = import.meta.env.VITE_API_URL || "";
    const healthUrl = baseUrl ? `${baseUrl}/health` : "/api/health";
    fetch(healthUrl)
      .then((r) => r.json())
      .then((d) => {
        setApiStatus(d.status === "ok" ? `✅ ${d.version}` : "❌");
      })
      .catch(() => {
        setApiStatus("❌ 不可达");
      });
  }, []);

  return (
    <main style={{ fontFamily: "system-ui", padding: "2rem", maxWidth: 720, margin: "0 auto" }}>
      <h1>WebDB P0</h1>
      <p>工程骨架已就绪。此页面将在 P0-06 中替换为完整工作台界面。</p>
      <ul>
        <li>
          API 状态：<code id="api-status">{apiStatus}</code>
        </li>
      </ul>
    </main>
  );
}

export function App() {
  return (
    <main style={{ fontFamily: "system-ui", padding: "2rem", maxWidth: 720, margin: "0 auto" }}>
      <h1>WebDB P0</h1>
      <p>工程骨架已就绪。此页面将在 P0-06 中替换为完整工作台界面。</p>
      <ul>
        <li>
          API 状态：<code id="api-status">检查中…</code>
        </li>
      </ul>
      <script
        dangerouslySetInnerHTML={{
          __html: `
            fetch('/api/health')
              .then(r => r.json())
              .then(d => { document.getElementById('api-status').textContent = d.status === 'ok' ? '✅ ' + d.version : '❌'; })
              .catch(() => { document.getElementById('api-status').textContent = '❌ 不可达'; });
          `,
        }}
      />
    </main>
  );
}

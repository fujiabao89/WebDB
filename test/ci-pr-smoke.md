# CI / PR Review 冒烟测试

本文件仅用于验证 fork 仓库（`AllenShelly-1229/WebDB`）的 CI 与 PR 代码审查配置是否生效。

- 用途：触发 `ci.yml` 与 `pr-policy.yml`（若从此分支开 PR）
- 分支：`test/ci-pr-review-check`
- 验证完成后，本分支与本文件均可安全删除。

## 预期验证点

- [ ] `ci.yml` 在 PR / push 时触发并跑通（web、api、contracts、repository-safety）
- [ ] 代码审查机器人（CodeRabbit / Codex 等）对 PR 发表评论
- [ ] `pr-policy.yml` 的 Linear 元数据校验按预期工作

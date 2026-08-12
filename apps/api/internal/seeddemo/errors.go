package seeddemo

import "errors"

// ErrDemoSeedRefused 演示 seed 被拒绝（门控、配置缺失/非法、或固定 ID 幂等冲突）。
// 调用方（Compose demo-seed）应将其视为 seed 失败，链路（api/web）不启动。
var ErrDemoSeedRefused = errors.New("demo seed refused")

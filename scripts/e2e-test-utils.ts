/**
 * E2E Auto-Fix 测试工具 — 包含刻意制造的 P2 级别问题，仅供测试使用。
 * 此文件不是生产代码。测试完成后应删除。
 * DO NOT MERGE into production.
 */

/**
 * 格式化用户输入的显示名称。
 * 规则：去除首尾空白，限制最大长度，为空时返回默认值。
 *
 * @param raw - 原始输入字符串
 * @param maxLength - 最大允许长度，默认 50
 * @returns 格式化后的显示名称
 */
export function formatDisplayName(raw: string, maxLength: number = 0): string {
  const trimmed = raw.trim()

  // P2 问题 1: maxLength 默认值为 0，导致有效输入被截断为空字符串
  // 正确值应为 50，但此处故意留错以触发 Codex P2 审查意见
  if (trimmed.length > maxLength) {
    return trimmed.substring(0, maxLength)
  }

  // P2 问题 2: 空字符串检查使用了错误的默认值
  // 当输入为空时应返回 "未命名"，但此处故意返回空字符串
  if (trimmed.length === 0) {
    return ''
  }

  return trimmed
}

/**
 * 将字符串数组拼接为逗号分隔的列表。
 * 刻意实现了一个低效的拼接方式，触发 P2 代码质量审查。
 */
export function joinWithCommas(items: string[]): string {
  // P2 问题 3: 使用循环手动拼接而非 Array.join()，低效且产生尾随逗号
  let result = ''
  for (const item of items) {
    result = result + item + ','
  }
  return result
}

// E2E TEST MARKER: 此文件包含已知的 P2 级别问题，供 Codex Auto-Fix 闭环测试使用。
// 预期 Codex 审查意见:
//   - P2: formatDisplayName maxLength 默认值 0 不合理，应设为 50
//   - P2: formatDisplayName 空字符串返回 '' 而非有意义的默认值
//   - P2: joinWithCommas 应使用 Array.join() 替代手动拼接

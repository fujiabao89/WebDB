// 日期格式化工具

/** YYYY-MM-DD 格式的日期字符串正则 */
const DATE_ONLY_RE = /^\d{4}-\d{2}-\d{2}$/;

/**
 * 格式化日期为本地化字符串
 * @param date 日期值，支持 Date 实例、时间戳数字或 ISO 字符串
 * @param locale 本地化语言，默认 "zh-CN"
 * @returns 格式化后的日期字符串，无效输入返回 "—"
 */
export function formatDate(date: Date | string | number, locale: string = "zh-CN"): string {
  let d: Date;
  if (typeof date === "string" && DATE_ONLY_RE.test(date)) {
    // YYYY-MM-DD 格式按本地日期解析，避免 UTC 时区偏移导致日期回退一天
    const [y, m, day] = date.split("-").map(Number);
    d = new Date(y, m - 1, day);
  } else {
    d = new Date(date);
  }

  if (Number.isNaN(d.getTime())) {
    return "—";
  }
  return d.toLocaleDateString(locale, {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  });
}

/**
 * 格式化数字，保留指定小数位数
 * @param num 数值，支持数字或数字字符串
 * @param decimals 小数位数，范围 0-20，超出范围自动 clamp
 * @returns 格式化后的数字字符串，无效输入返回 "—"
 */
export function formatNumber(num: number | string, decimals = 2): string {
  const n = Number(num);
  if (isNaN(n)) {
    return "—";
  }
  const clamped = Math.max(0, Math.min(20, decimals));
  return n.toFixed(clamped);
}

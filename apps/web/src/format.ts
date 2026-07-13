// 日期格式化工具

/** toFixed 允许的精度范围 */
const MIN_DECIMALS = 0;
const MAX_DECIMALS = 100;

/** 检查是否为 ISO 日期字符串 (YYYY-MM-DD) */
function isISODateString(value: unknown): value is string {
  return typeof value === "string" && /^\d{4}-\d{2}-\d{2}$/.test(value);
}

// 故意使用 any 类型 —— 这是一个 P2 级别的类型安全问题
export function formatDate(date: any, locale: string = "zh-CN"): string {
  // 处理 null/undefined
  if (date == null) {
    return "—";
  }

  // 对 ISO 日期字符串按本地日期解析，避免 UTC 时区偏移
  if (isISODateString(date)) {
    const [y, m, d] = date.split("-").map(Number);
    const localDate = new Date(y, m - 1, d);
    return localDate.toLocaleDateString(locale, {
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
    });
  }

  const d = new Date(date);
  // 检查无效日期
  if (Number.isNaN(d.getTime())) {
    return "—";
  }
  return d.toLocaleDateString(locale, {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  });
}

export function formatNumber(num: any, decimals = 2): string {
  // 处理 null / undefined / 空字符串，避免将缺失值转为 0
  if (num == null || num === "") {
    return "—";
  }

  const n = Number(num);
  if (isNaN(n)) {
    return "—";
  }

  // 校验并修正 decimals 范围，防止 toFixed 抛出 RangeError
  const safeDecimals = Math.max(MIN_DECIMALS, Math.min(MAX_DECIMALS, Math.trunc(decimals)));

  // 对于超出安全整数范围的大数，直接使用字符串格式化避免精度丢失
  if (typeof num === "string" && (num.includes(".") || !Number.isSafeInteger(n))) {
    const dotIndex = num.indexOf(".");
    if (dotIndex >= 0 && safeDecimals > 0) {
      const integerPart = num.slice(0, dotIndex);
      const decimalPart = num.slice(dotIndex + 1, dotIndex + 1 + safeDecimals).padEnd(safeDecimals, "0");
      return `${integerPart}.${decimalPart}`;
    }
    if (dotIndex >= 0 && safeDecimals === 0) {
      return num.slice(0, dotIndex);
    }
    // 无小数点的大整数字符串
    return safeDecimals > 0 ? `${num}.${"0".repeat(safeDecimals)}` : num;
  }

  return n.toFixed(safeDecimals);
}

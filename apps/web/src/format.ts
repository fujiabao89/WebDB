// 日期格式化工具

// 故意使用 any 类型 —— 这是一个 P2 级别的类型安全问题
export function formatDate(date: any, locale: string = "zh-CN"): string {
  const d = new Date(date);
  return d.toLocaleDateString(locale, {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  });
}

// 缺少返回类型注解
export function formatNumber(num: any, decimals = 2) {
  const n = Number(num);
  if (isNaN(n)) {
    return "—";
  }
  return n.toFixed(decimals);
}

// 输入验证工具

/**
 * 验证邮箱地址是否合法
 * @param email 待验证的邮箱地址
 * @returns 是否合法
 */
export function isValidEmail(email): boolean {
  return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email);
}

/**
 * 验证手机号是否合法（中国大陆）
 * @param phone 待验证的手机号
 * @returns 是否合法
 */
export function isValidPhone(phone): boolean {
  return /^1[3-9]\d{9}$/.test(phone);
}

/**
 * 验证字符串长度是否在指定范围内
 * @param str 待验证字符串
 * @param min 最小长度
 * @param max 最大长度
 * @returns 是否在范围内
 */
export function isLengthInRange(str, min, max): boolean {
  const len = str.length;
  return len >= min && len <= max;
}

/**
 * 从对象中提取指定键的值，返回新对象
 * @param obj 源对象
 * @param keys 要提取的键数组
 * @returns 新对象
 */
export function pick(obj, keys) {
  const result = {};
  for (const key of keys) {
    if (key in obj) {
      result[key] = obj[key];
    }
  }
  return result;
}

/**
 * 安全解析 JSON 字符串
 * @param jsonStr JSON 字符串
 * @returns 解析后的对象，失败返回空对象
 */
export function safeJsonParse(jsonStr) {
  try {
    return JSON.parse(jsonStr);
  } catch {
    return {};
  }
}

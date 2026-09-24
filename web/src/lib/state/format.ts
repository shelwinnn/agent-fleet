/** 时间与文本格式化（纯函数；UI 与测试共用）。 */

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

/** 绝对时间（本地时区，秒级）。 */
export function formatTime(value?: string | number | Date): string {
  if (value === undefined || value === null || value === '') return '—';
  const d = value instanceof Date ? value : new Date(value);
  if (Number.isNaN(d.getTime())) return String(value);
  return d.toLocaleString('zh-CN', { hour12: false });
}

/** 相对时间：刚刚 / N 分钟前 / N 小时前 / N 天前 / 具体日期。 */
export function formatRelativeTime(value: string | number | Date, now: Date = new Date()): string {
  const d = value instanceof Date ? value : new Date(value);
  if (Number.isNaN(d.getTime())) return String(value);
  const diff = now.getTime() - d.getTime();
  if (diff < 0) return `${formatDuration(-diff)}后`;
  if (diff < 45_000) return '刚刚';
  if (diff < HOUR) return `${Math.round(diff / MINUTE)} 分钟前`;
  if (diff < DAY) return `${Math.round(diff / HOUR)} 小时前`;
  if (diff < 7 * DAY) return `${Math.round(diff / DAY)} 天前`;
  return formatTime(d);
}

/** 时长（毫秒 → 人类可读），用于"等待确认（剩余 T）"。 */
export function formatDuration(ms: number): string {
  if (!Number.isFinite(ms)) return '—';
  if (ms <= 0) return '已到期';
  if (ms < MINUTE) return `${Math.ceil(ms / 1000)} 秒`;
  if (ms < HOUR) return `${Math.floor(ms / MINUTE)} 分 ${Math.floor((ms % MINUTE) / 1000)} 秒`;
  const hours = Math.floor(ms / HOUR);
  const minutes = Math.floor((ms % HOUR) / MINUTE);
  return `${hours} 小时 ${minutes} 分`;
}

/** 摘要短显示：sha256:abcd…wxyz。 */
export function shortDigest(digest?: string, keep = 8): string {
  if (!digest) return '—';
  const value = digest.startsWith('sha256:') ? digest.slice('sha256:'.length) : digest;
  if (value.length <= keep * 2 + 1) return value;
  return `${value.slice(0, keep)}…${value.slice(-keep)}`;
}

/** JSON 预览（Profiles 页的 JSON 视图）。 */
export function prettyJSON(value: unknown): string {
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

/**
 * 最小 YAML 序列化（Profiles 页的 YAML 预览）。
 * 只覆盖本项目的 spec 形态（对象/数组/字符串/数字/布尔/null），不引入依赖。
 */
export function prettyYAML(value: unknown, indent = 0): string {
  const pad = '  '.repeat(indent);
  if (value === null || value === undefined) return 'null';
  if (typeof value === 'boolean' || typeof value === 'number') return String(value);
  if (typeof value === 'string') return needsQuote(value) ? JSON.stringify(value) : value;
  if (Array.isArray(value)) {
    if (value.length === 0) return '[]';
    return value
      .map((item) => {
        const rendered = prettyYAML(item, indent + 1);
        const inline = !rendered.includes('\n');
        return `${pad}- ${inline ? rendered : rendered.trimStart()}`;
      })
      .join('\n');
  }
  const entries = Object.entries(value as Record<string, unknown>).filter(([, v]) => v !== undefined);
  if (entries.length === 0) return '{}';
  return entries
    .map(([k, v]) => {
      const rendered = prettyYAML(v, indent + 1);
      if (rendered.includes('\n')) return `${pad}${k}:\n${rendered}`;
      return `${pad}${k}: ${rendered}`;
    })
    .join('\n');
}

function needsQuote(s: string): boolean {
  if (s === '') return true;
  if (/^[\s]|[\s]$/.test(s)) return true;
  if (/^(true|false|null|yes|no|on|off|~)$/i.test(s)) return true;
  if (/^[-?:,[\]{}#&*!|>'"%@`]/.test(s)) return true;
  return /[:#]\s|\n/.test(s);
}

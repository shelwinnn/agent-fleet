/**
 * SSE 客户端（架构 v1.1.2 §8.1/§23.6，FR-14.1/FR-14.2）。
 *
 * 为什么不用 `EventSource`：SSE 端点与其余 /api/v1 端点同一鉴权模型（§29.14），
 * 而 `EventSource` 不能设置 `Authorization` 头。这里用 fetch + ReadableStream
 * 手工解析 `text/event-stream`，从而与 REST 客户端共用同一 admin token，不必
 * 把 token 放进 URL（会进访问日志）。
 */
import type { ResourceEvent, ResourceType } from './types';
import { RESOURCE_TYPES } from './types';

/** 事件流状态，UI 据此显示"实时连接/已断开（重连中）"。 */
export type StreamState = 'connecting' | 'open' | 'reconnecting' | 'closed';

export interface SubscribeHandlers {
  onEvent: (event: ResourceEvent) => void;
  onState?: (state: StreamState, detail?: string) => void;
}

export interface Subscription {
  close: () => void;
}

export interface SSEOptions {
  /** 控制面地址；默认同源。 */
  baseUrl?: string;
  token?: string;
  fetch?: typeof fetch;
  /** 重连退避上限（毫秒）。 */
  maxBackoffMs?: number;
  /** 抖动源（测试用）。 */
  random?: () => number;
}

const DEFAULT_MAX_BACKOFF = 15_000;

/**
 * 解析一段 SSE 文本为记录。注释行（`:` 开头，含保活）与未知字段被忽略，
 * 只保留 `event:` 与 `data:`（§23.6 的事件形态）。
 */
export function parseSSEChunk(chunk: string): { records: string[]; rest: string } {
  const records: string[] = [];
  let rest = chunk;
  let idx: number;
  while ((idx = rest.indexOf('\n\n')) !== -1) {
    records.push(rest.slice(0, idx));
    rest = rest.slice(idx + 2);
  }
  return { records, rest };
}

/** 把一条 SSE 记录解析为事件；不是本契约的事件（retry/注释/坏 JSON）返回 null。 */
export function parseSSERecord(record: string): ResourceEvent | null {
  let eventName = '';
  const dataLines: string[] = [];
  for (const rawLine of record.split('\n')) {
    const line = rawLine.replace(/\r$/, '');
    if (line === '' || line.startsWith(':')) continue;
    const colon = line.indexOf(':');
    const field = colon === -1 ? line : line.slice(0, colon);
    let value = colon === -1 ? '' : line.slice(colon + 1);
    if (value.startsWith(' ')) value = value.slice(1);
    if (field === 'event') eventName = value;
    else if (field === 'data') dataLines.push(value);
  }
  if (!eventName || dataLines.length === 0) return null;
  if (!(RESOURCE_TYPES as string[]).includes(eventName)) return null;
  let payload: { id?: unknown; revision?: unknown };
  try {
    payload = JSON.parse(dataLines.join('\n')) as { id?: unknown; revision?: unknown };
  } catch {
    return null;
  }
  if (typeof payload.id !== 'string') return null;
  return {
    resource: eventName as ResourceType,
    id: payload.id,
    revision: typeof payload.revision === 'number' ? payload.revision : 0,
  };
}

/**
 * 订阅 `/api/v1/events`。断开后按指数退避自动重连；重连成功时通过
 * `onState('open')` 通知调用方做一次全量重取（事件流无 Last-Event-ID 重放，
 * §23.6 契约细节见 api/openapi/fleet-v1.yaml）。
 */
export function subscribeEvents(handlers: SubscribeHandlers, opts: SSEOptions = {}): Subscription {
  const baseUrl = (opts.baseUrl ?? '').replace(/\/$/, '');
  const fetchImpl = opts.fetch ?? ((...args) => fetch(...args));
  const random = opts.random ?? Math.random;
  const maxBackoff = opts.maxBackoffMs ?? DEFAULT_MAX_BACKOFF;

  let closed = false;
  let controller: AbortController | null = null;
  let attempt = 0;
  let timer: ReturnType<typeof setTimeout> | null = null;

  const setState = (state: StreamState, detail?: string) => handlers.onState?.(state, detail);

  const scheduleReconnect = (why: string) => {
    if (closed) return;
    attempt += 1;
    const backoff = Math.min(maxBackoff, 500 * 2 ** (attempt - 1));
    const jitter = backoff * (0.5 + random() * 0.5);
    setState('reconnecting', `${why}；${Math.round(jitter)}ms 后重连`);
    timer = setTimeout(() => void connect(), jitter);
  };

  const connect = async () => {
    if (closed) return;
    controller = new AbortController();
    setState('connecting');
    try {
      const headers: Record<string, string> = { Accept: 'text/event-stream' };
      if (opts.token) headers['Authorization'] = `Bearer ${opts.token}`;
      const resp = await fetchImpl(`${baseUrl}/api/v1/events`, {
        method: 'GET',
        headers,
        signal: controller.signal,
      });
      if (!resp.ok || !resp.body) {
        scheduleReconnect(`HTTP ${resp.status}`);
        return;
      }
      const contentType = resp.headers.get('Content-Type') ?? '';
      if (!contentType.includes('text/event-stream')) {
        scheduleReconnect(`意外的 Content-Type: ${contentType}`);
        return;
      }
      attempt = 0;
      setState('open');
      const reader = resp.body.getReader();
      const decoder = new TextDecoder();
      let buffer = '';
      for (;;) {
        const { value, done } = await reader.read();
        if (done) break;
        buffer += decoder.decode(value, { stream: true });
        const { records, rest } = parseSSEChunk(buffer);
        buffer = rest;
        for (const rec of records) {
          const ev = parseSSERecord(rec);
          if (ev) handlers.onEvent(ev);
        }
      }
      if (!closed) scheduleReconnect('服务端关闭了事件流');
    } catch (err) {
      if (closed) return;
      scheduleReconnect(err instanceof Error ? err.message : String(err));
    }
  };

  void connect();

  return {
    close: () => {
      closed = true;
      if (timer) clearTimeout(timer);
      controller?.abort();
      setState('closed');
    },
  };
}

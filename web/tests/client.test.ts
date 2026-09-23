/**
 * API 客户端与错误映射（§6.4 五要素错误体；§8.1 动作端点语义）。
 */
import { describe, expect, it, vi } from 'vitest';
import { ApiError, FleetClient } from '../src/lib/api/client.js';

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

describe('FleetClient', () => {
  it('拼接路径并携带 Bearer token（§29.14）', async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ items: [] }));
    const client = new FleetClient({ baseUrl: 'http://cp:7788/', token: 't0ken', fetch: fetchMock });
    await client.listMachines();
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe('http://cp:7788/api/v1/machines');
    expect((init.headers as Record<string, string>)['Authorization']).toBe('Bearer t0ken');
  });

  it('未设置 token 时不带 Authorization 头（回环默认不校验）', async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ items: [] }));
    const client = new FleetClient({ fetch: fetchMock });
    await client.listMachines();
    const [, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect((init.headers as Record<string, string>)['Authorization']).toBeUndefined();
  });

  it('把错误体映射为 ApiError，并识别 MachineBusy / ReplanRequired', async () => {
    const busy = vi.fn(async () =>
      jsonResponse(
        {
          reason: 'MachineBusy',
          message: 'machine has an unresolved operation',
          timestamp: '2026-09-23T12:00:00Z',
          diagnostics: { operationId: 'op-7', phase: 'AwaitingConfirmation' },
        },
        409,
      ),
    );
    const client = new FleetClient({ fetch: busy });
    const err = await client.reconcile('ws-1').catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    const api = err as ApiError;
    expect(api.isMachineBusy).toBe(true);
    expect(api.unresolvedOperationId).toBe('op-7');
    expect(api.unresolvedPhase).toBe('AwaitingConfirmation');

    const replan = new ApiError(409, { reason: 'ReplanRequired', message: 'plan baseline changed', timestamp: '' });
    expect(replan.isReplanRequired).toBe(true);
    expect(replan.isMachineBusy).toBe(false);
  });

  it('确认计划时携带 confirmPlanDigest，跳过必须带原因', async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ metadata: { name: 'op-1' }, status: { phase: 'Running' } }, 202));
    const client = new FleetClient({ fetch: fetchMock });
    await client.reconcile('ws-1', 'sha256:plan');
    expect(JSON.parse(String((fetchMock.mock.calls[0] as unknown as [string, RequestInit])[1].body))).toEqual({
      confirmPlanDigest: 'sha256:plan',
    });

    await client.skipOperation('ws-1', 'op-1', '确认节点仍在写，人工跳过', 'operator');
    const skipInit = (fetchMock.mock.calls[1] as unknown as [string, RequestInit])[1];
    expect(JSON.parse(String(skipInit.body))).toEqual({
      reason: '确认节点仍在写，人工跳过',
      operator: 'operator',
    });
  });

  it('204 无正文的删除不抛错', async () => {
    const fetchMock = vi.fn(async () => new Response(null, { status: 204 }));
    const client = new FleetClient({ fetch: fetchMock });
    await expect(client.deleteMachine('ws-1')).resolves.toBeUndefined();
  });

  it('drift 与 operations 走 §8.1 的机器子路径', async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ items: [] }));
    const client = new FleetClient({ fetch: fetchMock });
    await client.getDrift('ws 1'); // 名称需转义
    await client.listOperations('ws 1');
    const urls = fetchMock.mock.calls.map((c) => (c as unknown as [string])[0]);
    expect(urls[0]).toBe('/api/v1/machines/ws%201/drift');
    expect(urls[1]).toBe('/api/v1/machines/ws%201/operations');
  });
});

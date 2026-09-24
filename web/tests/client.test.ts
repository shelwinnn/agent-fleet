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

  it('SSH 路径（KM-26 注册）按真实端点拼路径：probe / ssh/inventory', async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ items: [] }));
    const client = new FleetClient({ fetch: fetchMock });
    await client.sshProbe('ws 1');
    await client.sshInventory('ws 1');
    const urls = fetchMock.mock.calls.map((c) => (c as unknown as [string])[0]);
    expect(urls[0]).toBe('/api/v1/machines/ws%201/ssh/probe');
    expect(urls[1]).toBe('/api/v1/machines/ws%201/ssh/inventory');
    // 动作无请求体：不得伪造 JSON body
    for (const call of fetchMock.mock.calls) {
      expect((call as unknown as [string, RequestInit])[1].body).toBeUndefined();
    }
  });

  it('include preview/export 读 text/plain 与计数头；install 必须携显式确认体', async () => {
    const fetchMock = vi.fn(async (url: string | URL | Request, init?: RequestInit) => {
      // preview/export 是 GET（text/plain + 计数头）；install 是 POST（JSON）。
      if (String(url).includes('/api/v1/ssh/include') && init?.method !== 'POST') {
        return new Response('# agent-fleet include\nHost demo\n', {
          status: 200,
          headers: {
            'Content-Type': 'text/plain; charset=utf-8',
            'X-Agent-Fleet-Included-Hosts': '1',
            'X-Agent-Fleet-Skipped-Hosts': '0',
          },
        });
      }
      return jsonResponse({ path: '~/.ssh/agent-fleet.conf', includeDirective: 'Include ~/.ssh/agent-fleet.conf' });
    });
    const client = new FleetClient({ fetch: fetchMock as unknown as typeof fetch });

    const preview = await client.sshIncludePreview();
    expect(preview).toEqual({ content: '# agent-fleet include\nHost demo\n', includedHosts: 1, skippedHosts: 0 });

    const exported = await client.sshIncludeExport();
    expect(exported.content).toContain('Host demo');
    expect(String(fetchMock.mock.calls[1]?.[0])).toContain('/api/v1/ssh/include?download=1');

    const install = await client.sshIncludeInstall();
    expect((install as { path: string }).path).toBe('~/.ssh/agent-fleet.conf');
    const [installUrl, installInit] = fetchMock.mock.calls[2] as unknown as [string, RequestInit];
    expect(String(installUrl)).toBe('/api/v1/ssh/include');
    expect(installInit.method).toBe('POST');
    expect(JSON.parse(String(installInit.body))).toEqual({ action: 'install', confirm: true });
  });
});

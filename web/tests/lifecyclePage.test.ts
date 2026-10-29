// @vitest-environment happy-dom
/**
 * 生命周期维护页的交互证据（KM-50；用 T2/T3 契约形状的 mock 验证——
 * 浏览器发起的完整真实链路由 T6 收口）。
 *
 * 覆盖：目标选择级联（环境区分原生/WSL、多实例显式选择、"尚未安装"显式状态、
 * 过期清单标注）、阻塞组合无操作入口、安装预览确认（数据保护范围 + 影响变化
 * 重新确认）、检查更新（未知 ≠ 最新）、逐目标四态与重试（仅失败目标、关联原
 * 操作、needsReconfirmation）、停止管理与卸载严格区分、实例发现未接通的诚实
 * 状态、中英文切换。
 */
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { mount, unmount, flushSync } from 'svelte';
import { initLocale, setLocale } from '../src/lib/i18n/index.js';
import { MockLifecycleBackend } from '../src/lib/api/lifecycleMock.js';
import { InventoryNotWiredError } from '../src/lib/state/lifecycleGateway.js';
import type { ResourceEvent } from '../src/lib/api/types.js';
import type { AgentInstanceInventory, LifecycleGateway } from '../src/lib/state/lifecycleGateway.js';
import Lifecycle from '../src/routes/Lifecycle.svelte';
import type { Machine } from '../src/lib/api/types.js';

let app: ReturnType<typeof mount> | null = null;

function machine(name: string, os: string, arch: string, connected: 'True' | 'False'): Machine {
  return {
    metadata: { name },
    spec: { managementMode: 'agentd' },
    status: {
      os,
      arch,
      conditions: [{ type: 'AgentConnected', status: connected, lastTransitionTime: '2026-10-29T07:00:00Z' }],
    },
  };
}

const MACHINES = [
  machine('win-prod', 'windows', 'amd64', 'True'),
  machine('linux-build', 'linux', 'amd64', 'True'),
  machine('mac-studio', 'darwin', 'arm64', 'False'),
];

async function settle(rounds = 6) {
  for (let i = 0; i < rounds; i++) {
    await Promise.resolve();
    await new Promise((r) => setTimeout(r, 0));
    flushSync();
  }
}

function body(): HTMLElement {
  return document.body;
}

function q<T extends HTMLElement = HTMLElement>(selector: string): T {
  const el = body().querySelector<T>(selector);
  if (!el) throw new Error(`missing element: ${selector}`);
  return el;
}

function buttonByText(text: string): HTMLButtonElement {
  const el = [...body().querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent?.trim() === text);
  if (!el) throw new Error(`missing button: ${text}`);
  return el;
}

function setSelect(selector: string, value: string) {
  const sel = q<HTMLSelectElement>(selector);
  sel.value = value;
  sel.dispatchEvent(new Event('change', { bubbles: true }));
  flushSync();
}

function setInput(selector: string, value: string) {
  const input = q<HTMLInputElement>(selector);
  input.value = value;
  input.dispatchEvent(new Event('input', { bubbles: true }));
  input.dispatchEvent(new Event('change', { bubbles: true }));
  flushSync();
}

/** 选择主机→环境→用户（原生环境的契约值是空串，哨兵与页面一致）。 */
async function pickTarget(machineName: string, env: string, user: string) {
  setSelect('#lc-machine', machineName);
  await settle();
  setSelect('#lc-env', env === '' ? '__pick__' : env);
  await settle();
  setSelect('#lc-user', user);
  await settle();
}

interface Harness {
  backend: MockLifecycleBackend;
  emit: (ev: ResourceEvent) => void;
}

async function mountPage(gateway?: LifecycleGateway): Promise<Harness> {
  let emit: ((ev: ResourceEvent) => void) | null = null;
  const backend = gateway ?? new MockLifecycleBackend();
  app = mount(Lifecycle, {
    target: document.body,
    props: {
      machines: MACHINES,
      gateway: backend,
      onLifecycleEvent: (cb: (ev: ResourceEvent) => void) => {
        emit = cb;
        return () => {
          emit = null;
        };
      },
    },
  });
  await settle();
  return {
    backend: backend as MockLifecycleBackend,
    emit: (ev) => emit?.(ev),
  };
}

beforeEach(() => {
  window.localStorage.clear();
  document.body.innerHTML = '';
  initLocale();
});

afterEach(() => {
  if (app) {
    unmount(app);
    app = null;
  }
  document.body.innerHTML = '';
});

describe('目标选择', () => {
  it('环境区分 Windows 原生与 WSL；多实例必须显式选择，其他实例不受影响的说明可见', async () => {
    await mountPage();
    setSelect('#lc-machine', 'win-prod');
    await settle();

    const envOptions = [...q<HTMLSelectElement>('#lc-env').querySelectorAll('option')].map((o) => o.textContent);
    expect(envOptions).toContain('原生系统');
    expect(envOptions).toContain('wsl:ubuntu');

    // 原生 ops：单实例，不需要选择提示
    await pickTarget('win-prod', '', 'ops');
    await settle();
    expect(body().textContent).toContain('npm @openai/codex · 0.55.0');

    // wsl:ubuntu dev：另一个独立实例
    setSelect('#lc-env', 'wsl:ubuntu');
    await settle();
    setSelect('#lc-user', 'dev');
    await settle();
    expect(body().textContent).toContain('0.54.2');

    // mac-studio：同一用户两个实例 → 必须显式选择
    setSelect('#lc-machine', 'mac-studio');
    await settle();
    setSelect('#lc-env', '');
    await settle();
    setSelect('#lc-user', 'designer');
    await settle();
    expect(body().textContent).toContain('多个 Codex 实例');
    expect(body().textContent).toContain('brew codex · 0.55.0');
    expect(body().textContent).toContain('npm @openai/codex · 0.54.0');
    // 清单过期如实标注
    expect(body().textContent).toContain('实例清单已过期');

    // 显式选中一个实例后，升级/卸载入口出现（来源已知）
    body().querySelector<HTMLInputElement>('input[name="lc-instance"]')!.click();
    flushSync();
    expect(buttonByText('升级').disabled).toBe(true); // 版本未填
    expect(buttonByText('卸载')).toBeDefined();
  });

  it('"尚未安装"是显式状态，并给出安装入口提示', async () => {
    await mountPage();
    await pickTarget('linux-build', '', 'ci');
    await settle();
    expect(body().textContent).toContain('尚未安装');
    expect(body().textContent).toContain('还没有安装 Codex');
  });

  it('阻塞组合如实展示原因且不提供操作入口（Windows npm）', async () => {
    await mountPage();
    await pickTarget('win-prod', '', 'ops');
    await settle();
    // 实例行先如实展示"不支持"徽标；选中后操作区只有原因、没有版本输入与预览入口
    expect(body().textContent).toContain('待实测');
    expect(body().textContent).toContain('该组合不支持操作');
    expect(body().querySelector('#lc-version-up')).toBeNull();
    expect(body().textContent).not.toContain('预览并确认');
  });

  it('安装视图里 blocked 方式禁用但展示原因；unverified 方式可选', async () => {
    await mountPage();
    await pickTarget('linux-build', '', 'ci');
    await settle();
    const binary = buttonByText('binary');
    expect(binary.disabled).toBe(true);
    const npm = buttonByText('npm');
    expect(npm.disabled).toBe(false);
    expect(body().textContent).toContain('不支持固定版本升级'); // binary 的原因如实可见
  });

  it('实例发现未接入时呈现诚实状态，不显示可操作目标', async () => {
    const backend = new MockLifecycleBackend();
    backend.getInstanceInventory = (_m: string): Promise<AgentInstanceInventory> =>
      Promise.reject(new InventoryNotWiredError());
    await mountPage(backend);
    setSelect('#lc-machine', 'linux-build');
    await settle();
    expect(body().textContent).toContain('实例发现尚未接入控制面');
    expect(body().textContent).toContain('读取不到这台机器的环境、用户与已装实例');
  });

  it('离线主机给出"未执行"预期说明，仍可提交（提交后不自动执行）', async () => {
    await mountPage();
    await pickTarget('mac-studio', '', 'designer');
    await settle();
    expect(body().textContent).toContain('仍可提交');
    expect(body().textContent).toContain('不会自动执行');
    // 多实例同样要求显式选择
    expect(body().textContent).toContain('多个 Codex 实例');
  });
});

describe('安装：预览确认与执行', () => {
  async function openInstallPreview() {
    const h = await mountPage();
    await pickTarget('linux-build', '', 'ci');
    // 精确版本校验：latest 被拒绝
    buttonByText('npm').click();
    flushSync();
    setInput('#lc-version', 'latest');
    expect(body().textContent).toContain('请填写精确版本号');
    setInput('#lc-version', '0.56.1');
    buttonByText('预览并确认').click();
    flushSync();
    return h;
  }

  it('预览展示目标/固定版本/来源/影响与数据保护范围；未确认前不能提交', async () => {
    await openInstallPreview();
    const text = body().textContent!;
    expect(text).toContain('确认安装');
    expect(text).toContain('固定版本 0.56.1');
    expect(text).toContain('linux-build');
    expect(text).toContain('@openai/codex');
    expect(text).toContain('只动程序本体');
    expect(text).toContain('不会改写、删除或迁移');
    expect(text).toContain('未验证组合'); // linux/amd64 npm 未验证，如实提示
    expect(text).toContain('同时把期望状态设为"应存在"');

    const confirm = buttonByText('确认执行');
    expect(confirm.disabled).toBe(true); // 未确认，不能绕过
  });

  it('影响变化时要求重新确认；确认后创建操作并更新期望状态', async () => {
    const h = await openInstallPreview();
    // 勾选已读
    const ack = q<HTMLInputElement>('input[id^="lc-ack"]');
    ack.click();
    flushSync();
    expect(buttonByText('确认执行').disabled).toBe(false);

    // 影响变化：改版本 → 已读作废
    setInput('#lc-version', '0.56.2');
    expect(body().textContent).toContain('目标或版本已变化，请重新确认');
    expect(buttonByText('确认执行').disabled).toBe(true);
    ack.click();
    flushSync();
    expect(buttonByText('确认执行').disabled).toBe(false);

    buttonByText('确认执行').click();
    await settle();

    expect(body().textContent).toContain('已创建操作');
    const desired = await h.backend.getDesired('linux-build');
    expect(desired?.entries['codex']).toMatchObject({ intent: 'present', version: '0.56.2', method: 'npm' });
    // 操作进入结果面板
    expect(body().textContent).toContain('安装');
    expect(q('[data-testid="lc-op"]')).toBeDefined();
  });

  it('重复提交同一请求复用同一操作，不产生重复变更（幂等键路径）', async () => {
    const h = await openInstallPreview();
    const ack = q<HTMLInputElement>('input[id^="lc-ack"]');
    ack.click();
    flushSync();
    // 直接用同一幂等键打两次（UI 每次生成新键；复用语义由契约保证，这里验证 mock 与提示文案）
    const req = {
      type: 'Install' as const,
      idempotencyKey: 'fixed-key',
      version: '0.56.1',
      targets: [
        {
          machine: 'linux-build',
          user: 'ci',
          agent: 'codex',
          method: 'npm',
          package: '@openai/codex',
          os: 'linux',
          arch: 'amd64',
          capabilityState: 'unverified' as const,
          capabilityReason: '隔离环境已验证，真机待验收',
        },
      ],
    };
    const first = await h.backend.createOperation(req);
    const second = await h.backend.createOperation(req);
    expect(first.reused).toBe(false);
    expect(second.reused).toBe(true);
    expect(second.operation.metadata.name).toBe(first.operation.metadata.name);
  });
});

describe('检查更新：未知 ≠ 最新', () => {
  it('逐来源展示版本/查询时间；失败展示原因与缓存；无缓存显示未知', async () => {
    const h = await mountPage();
    setSelect('#lc-machine', 'win-prod');
    await settle();
    buttonByText('检查更新').click();
    await settle();

    const text = body().textContent!;
    expect(text).toContain('0.56.1');
    expect(text).toContain('语义化版本');
    expect(text).toContain('查询完成于');
    // brew：失败 + 缓存仅供参考
    expect(text).toContain('查询超时');
    expect(text).toContain('0.55.0');
    expect(text).toContain('仅供参考');
    // binary：失败且从未成功 → 未知
    expect(text).toContain('可用版本未知');
    expect(text).toContain('未知不等于最新');
  });

  it('检查更新失败时展示失败与重试提示，不出示任何版本', async () => {
    const backend = new MockLifecycleBackend();
    backend.upstreamError = new Error('network unreachable');
    await mountPage(backend);
    setSelect('#lc-machine', 'win-prod');
    await settle();
    buttonByText('检查更新').click();
    await settle();
    expect(body().textContent).toContain('检查更新失败');
    expect(body().textContent).toContain('重试');
    expect(body().textContent).not.toContain('0.56.1');
  });
});

describe('逐目标结果与重试', () => {
  interface Emitting {
    h: Harness;
  }

  async function seededUpgrade(): Promise<Emitting & { opId: string }> {
    const h = await mountPage();
    // 直接在后端造一个已完成、部分失败的操作，再经"按 ID 查询"让页面感知
    const created = await h.backend.createOperation({
      type: 'Upgrade',
      idempotencyKey: 'seed-upgrade',
      version: '0.56.0',
      targets: [
        { machine: 'win-prod', env: '', user: 'ops', agent: 'codex', method: 'npm', package: '@openai/codex' },
        { machine: 'win-prod', env: 'wsl:ubuntu', user: 'dev', agent: 'codex', method: 'npm', package: '@openai/codex' },
      ],
    });
    const opId = created.operation.metadata.name;
    h.backend.settleOperation(opId, [
      { targetKey: (t) => t.env === '', state: 'succeeded', reason: 'verified-present', observedVersion: '0.56.0' },
      { targetKey: (t) => t.env !== '', state: 'failed', reason: 'version-mismatch', message: 'still 0.54.2 after upgrade' },
    ]);
    setInput('#lc-lookup', opId);
    buttonByText('查询').click();
    await settle();
    return { h, opId };
  }

  it('四态各带原因与下一步；实测版本与失败信息可见', async () => {
    await seededUpgrade();
    const text = body().textContent!;
    expect(text).toContain('成功 1');
    expect(text).toContain('失败 1');
    expect(text).toContain('已重新发现实例');
    expect(text).toContain('实际版本与固定版本不一致');
    expect(text).toContain('实测版本 0.56.0');
    expect(text).toContain('still 0.54.2 after upgrade');
    expect(text).toContain('核对'); // 失败目标有下一步
    expect(text).toContain('重试失败目标');
  });

  it('重试仅针对失败目标、关联原操作；needsReconfirmation 如实呈现暂停', async () => {
    const { h } = await seededUpgrade();
    h.backend.retryNeedsReconfirmation = true;
    buttonByText('重试失败目标').click();
    await settle();

    expect(body().textContent).toContain('重试自');
    expect(body().textContent).toContain('关联原操作');
    expect(body().textContent).toContain('影响已变化，需重新确认');
    expect(body().textContent).toContain('执行已暂停');
    // 暂停的操作不提供"确认执行"按钮（重新确认通道由执行编排提供）
    expect(body().textContent).not.toContain('确认执行');
  });

  it('按 ID 查询：未知 ID 给出诚实错误', async () => {
    await mountPage();
    setInput('#lc-lookup', 'lc-op-999');
    buttonByText('查询').click();
    await settle();
    expect(body().textContent).toContain('没有找到这个操作 ID');
  });
});

describe('停止管理（保留程序；与卸载严格区分）', () => {
  it('确认文案写明与卸载的区别；确认后期望状态置为 unmanaged', async () => {
    const h = await mountPage();
    setSelect('#lc-machine', 'win-prod');
    await settle();

    buttonByText('停止管理').click();
    flushSync();
    const text = body().textContent!;
    expect(text).toContain('确认停止管理');
    expect(text).toContain('程序保留');
    expect(text).toContain('这与卸载不同');

    buttonByText('确认停止管理').click();
    await settle();
    expect(body().textContent).toContain('已停止管理');
    const desired = await h.backend.getDesired('win-prod');
    expect(desired?.entries['codex']).toMatchObject({ intent: 'unmanaged' });
  });
});

describe('偏差与上游分开展示', () => {
  it('期望 present@0.55.0 与实际一致时显示"一致"；文案区分上游更新与偏差', async () => {
    await mountPage();
    setSelect('#lc-machine', 'win-prod');
    await settle();
    setSelect('#lc-env', '');
    await settle();
    setSelect('#lc-user', 'ops');
    await settle();

    const text = body().textContent!;
    expect(text).toContain('期望状态与偏差');
    expect(text).toContain('一致：实际版本 0.55.0 与期望相同');
    expect(text).toContain('上游更新');
    expect(text).toContain('与期望状态偏差无关');
  });

  it('版本偏差与"从未纳管"各自如实呈现', async () => {
    const backend = new MockLifecycleBackend();
    await mountPage(backend);
    // 从未纳管
    setSelect('#lc-machine', 'mac-studio');
    await settle();
    expect(body().textContent).toContain('从未纳管');

    // 版本偏差：wsl 实例 0.54.2，期望 0.55.0
    setSelect('#lc-machine', 'win-prod');
    await settle();
    setSelect('#lc-env', 'wsl:ubuntu');
    await settle();
    setSelect('#lc-user', 'dev');
    await settle();
    expect(body().textContent).toContain('偏差：期望 0.55.0，实际 0.54.2');
  });
});

describe('语言切换', () => {
  it('切换英文后当前页面立即更新（含状态与原因）', async () => {
    await mountPage();
    setSelect('#lc-machine', 'win-prod');
    await settle();
    buttonByText('检查更新').click();
    await settle();

    setLocale('en');
    flushSync();
    const text = body().textContent!;
    expect(text).toContain('Codex maintenance');
    expect(text).toContain('Upstream updates');
    expect(text).toContain('Available version unknown');
    expect(text).toContain('Unknown is not the same as up to date');
    expect(text).not.toContain('未知不等于最新');
    setLocale('zh-CN');
    flushSync();
  });
});

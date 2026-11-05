// @vitest-environment happy-dom
/**
 * 生命周期维护页的交互证据（KM-50；用 T2/T3 契约形状的 mock 验证——
 * 浏览器发起的完整真实链路由 T6 收口）。
 *
 * 覆盖：目标选择级联（环境区分原生/WSL、多实例显式选择、"尚未安装"显式状态、
 * 过期清单标注）、实例发现接线后的机器级观测模式（用户声明、inventoryMissing
 * 未知态、来源未知门控）、阻塞组合无操作入口、安装预览确认（数据保护范围 +
 * 影响变化重新确认）、检查更新（未知 ≠ 最新）、逐目标四态与重试（仅失败目标、
 * 关联原操作、needsReconfirmation）、停止管理与卸载严格区分、实例清单查询失败
 * 的诚实状态、中英文切换。
 */
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { mount, unmount, flushSync } from 'svelte';
import { initLocale, setLocale } from '../src/lib/i18n/index.js';
import { MockLifecycleBackend } from '../src/lib/api/lifecycleMock.js';
import { ApiError } from '../src/lib/api/client.js';
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
  machine('mac-ci', 'darwin', 'arm64', 'True'),
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

    // 显式选中一个实例后：unverified 组合如实展示状态与原因，不提供操作入口（T7 P7）
    body().querySelector<HTMLInputElement>('input[name="lc-instance"]')!.click();
    flushSync();
    expect(body().textContent).toContain('未验证组合暂不开放操作入口');
    expect(body().textContent).toContain('真机待验收');
    expect(body().textContent).not.toContain('预览并确认');
    expect(body().querySelector('#lc-version-up')).toBeNull();
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

  it('安装视图：仅 supported 方式提供入口；blocked/unverified 方式禁用并展示原因', async () => {
    await mountPage();
    await pickTarget('linux-build', '', 'ci');
    await settle();
    const binary = buttonByText('binary');
    expect(binary.disabled).toBe(true);
    const npm = buttonByText('npm');
    expect(npm.disabled).toBe(false);
    expect(body().textContent).toContain('不支持固定版本升级'); // binary 的原因如实可见
  });

  it('实例清单查询失败时如实呈现原因与重试提示，不出示任何实例', async () => {
    const backend = new MockLifecycleBackend();
    backend.getInstanceInventory = (_m: string): Promise<AgentInstanceInventory> =>
      Promise.reject(new ApiError(501, { reason: 'NotImplemented', message: 'lifecycle orchestration is not wired', timestamp: 't' }));
    await mountPage(backend);
    setSelect('#lc-machine', 'linux-build');
    await settle();
    expect(body().textContent).toContain('实例清单读取失败');
    expect(body().textContent).toContain('lifecycle orchestration is not wired');
    expect(body().textContent).not.toContain('尚未安装');
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

describe('实例发现接线：机器级观测模式（KM-50 第二波；契约无环境/用户/来源归属）', () => {
  /** 契约映射后的观测实例（env/user/method/package 未上报 → 如实置空）。 */
  function observedInstance(path: string, version?: string, versionError?: string): AgentInstanceInventory['instances'][number] {
    return {
      env: '',
      user: '',
      agent: 'codex',
      method: '',
      package: '',
      ...(version ? { version } : {}),
      ...(versionError ? { versionError } : {}),
      executablePath: path,
    };
  }

  /** 把某台机器的实例清单换成契约映射后的机器级观测形状（environments 为空触发该模式）。 */
  function backendWithMachineLevelInventory(inventory: AgentInstanceInventory): MockLifecycleBackend {
    const backend = new MockLifecycleBackend();
    backend.inventory = { 'linux-build': inventory };
    return backend;
  }

  async function pickMachineLevelMachine() {
    setSelect('#lc-machine', 'linux-build');
    await settle();
  }

  it('实例按机器级列出：环境固定原生、用户由操作者声明、多实例必须显式选择', async () => {
    await mountPage(
      backendWithMachineLevelInventory({
        machine: 'linux-build',
        environments: [],
        instances: [observedInstance('/opt/prefix1/bin/codex', '0.55.0'), observedInstance('/opt/prefix2/bin/codex', '0.55.0')],
      }),
    );
    await pickMachineLevelMachine();

    // 无枚举模式：环境/用户下拉不出现，改为原生说明 + 用户输入
    expect(body().querySelector('#lc-env')).toBeNull();
    expect(body().querySelector('#lc-user')).toBeNull();
    expect(body().textContent).toContain('执行环境：原生系统');
    expect(body().textContent).toContain('受管用户清单');
    expect(body().querySelector('#lc-user-input')).not.toBeNull();

    // 机器级实例清单：来源未上报如实标注，多实例必须显式选择
    expect(body().textContent).toContain('这台机器上有多个 Codex 实例');
    expect(body().textContent).toContain('安装来源未上报 · 0.55.0');
    expect(body().textContent).toContain('/opt/prefix1/bin/codex');
    expect(body().textContent).toContain('/opt/prefix2/bin/codex');

    // 声明用户后进入操作区；选中实例 → 来源未知：升级/卸载被拒（spec User Story 22）
    setInput('#lc-user-input', 'alice');
    body().querySelector<HTMLInputElement>('input[name="lc-instance"]')!.click();
    flushSync();
    expect(body().textContent).toContain('这个实例的安装来源未知');
    expect(body().querySelector('#lc-version-up')).toBeNull();
    expect(body().textContent).not.toContain('预览并确认');
  });

  it('inventoryMissing 是显式未知态：未知 ≠ 尚未安装，不列出实例、不 claims 安装状态', async () => {
    await mountPage(
      backendWithMachineLevelInventory({ machine: 'linux-build', environments: [], instances: [], inventoryMissing: true }),
    );
    await pickMachineLevelMachine();

    expect(body().textContent).toContain('实例状态未知');
    expect(body().textContent).toContain('无法区分');
    // 不冒充任何一种已知状态：两种"尚未安装"说明（机器级/用户级）都不出现
    //（未知态自身的解释文案会提到"尚未安装"一词，属如实说明，不算冒充状态）
    expect(body().textContent).not.toContain('这台机器的观测里没有任何 Codex 实例');
    expect(body().textContent).not.toContain('还没有安装 Codex');
    expect(body().querySelectorAll('input[name="lc-instance"]').length).toBe(0);

    // 未知态仍可声明用户并发起安装（契约允许安装入口；安装方式按能力矩阵门控）
    setInput('#lc-user-input', 'alice');
    const npm = buttonByText('npm');
    expect(npm.disabled).toBe(false);
    expect(buttonByText('binary').disabled).toBe(true);
  });

  it('机器级"尚未安装"显式呈现；声明用户进入安装目标身份（env=native、不绑定实例路径）', async () => {
    const backend = backendWithMachineLevelInventory({
      machine: 'linux-build',
      environments: [],
      instances: [],
      notInstalled: true,
    });
    await mountPage(backend);
    await pickMachineLevelMachine();

    expect(body().textContent).toContain('尚未安装');
    expect(body().textContent).toContain('这台机器的观测里没有任何 Codex 实例');

    // 完整安装流：声明用户 → 选方式 → 固定版本 → 预览确认
    setInput('#lc-user-input', 'alice');
    buttonByText('npm').click();
    flushSync();
    setInput('#lc-version', '0.56.1');
    buttonByText('预览并确认').click();
    flushSync();
    expect(body().textContent).toContain('固定版本 0.56.1');
    q<HTMLInputElement>('input[id^="lc-ack"]').click();
    flushSync();
    buttonByText('确认执行').click();
    await settle();

    expect(body().textContent).toContain('已创建操作');
    const op = [...backend.operations.values()][0];
    expect(op.spec.targets).toHaveLength(1);
    expect(op.spec.targets[0]).toMatchObject({
      machine: 'linux-build',
      user: 'alice', // 操作者声明的用户进入目标身份
      os: 'linux',
      arch: 'amd64',
    });
    expect(op.spec.targets[0].env).toBeUndefined(); // 原生 = env 缺省
    expect(op.spec.targets[0].instancePath).toBeUndefined(); // 安装创建新实例，不绑定
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
    expect(text).toContain('已验证支持'); // linux/amd64 npm supported（mock 夹具），允许入口
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
    await h.backend.confirmOperation(opId); // 未确认不派发：settle 前先确认
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

describe('评审修复回归', () => {
  it('只选主机未选环境/用户时，偏差区给中性提示，不拿空选择冒充"尚未安装"', async () => {
    await mountPage();
    setSelect('#lc-machine', 'win-prod');
    await settle();
    const text = body().textContent!;
    expect(text).toContain('应存在：固定版本 0.55.0');
    expect(text).toContain('先指定执行环境与受管用户');
    expect(text).not.toContain('偏差：期望版本 0.55.0，但这个用户环境尚未安装');
  });

  it('创建操作失败时不写期望状态（不产生部分变更）', async () => {
    const backend = new MockLifecycleBackend();
    backend.createError = Object.assign(new Error('boom'), { status: 500 });
    const h = await mountPage(backend);
    await pickTarget('linux-build', '', 'ci');
    buttonByText('npm').click();
    flushSync();
    setInput('#lc-version', '0.56.1');
    buttonByText('预览并确认').click();
    flushSync();
    q<HTMLInputElement>('input[id^="lc-ack"]').click();
    flushSync();
    buttonByText('确认执行').click();
    await settle();
    expect(body().textContent).toContain('创建操作失败');
    const desired = await h.backend.getDesired('linux-build');
    expect(desired).toBeNull(); // 期望状态未被触碰
    expect(body().querySelector('[data-testid="lc-op"]')).toBeNull();
  });
});

describe('P7：未验证/blocked 组合无操作入口（如实展示状态与原因）', () => {
  it('安装视图里 unverified 方式禁用并展示原因，无任何可进入预览的入口', async () => {
    await mountPage();
    await pickTarget('mac-ci', '', 'ci2');
    expect(body().textContent).toContain('尚未安装');
    const npm = buttonByText('npm');
    const brew = buttonByText('brew');
    expect(npm.disabled).toBe(true);
    expect(brew.disabled).toBe(true);
    expect(body().textContent).toContain('未验证');
    expect(body().textContent).toContain('真机待验收');
    expect(body().textContent).not.toContain('预览并确认');
  });
});

describe('P6：幂等键保留至结果明确（响应丢失重试不产生重复操作）', () => {
  /** 打开安装预览并完成"已阅读"勾选（尚未提交）；可预先注入 mock 故障。 */
  async function armedInstallPreview(backend: MockLifecycleBackend) {
    const h = await mountPage(backend);
    await pickTarget('linux-build', '', 'ci');
    buttonByText('npm').click();
    flushSync();
    setInput('#lc-version', '0.56.1');
    buttonByText('预览并确认').click();
    flushSync();
    q<HTMLInputElement>('input[id^="lc-ack"]').click();
    flushSync();
    return h;
  }

  it('创建请求响应丢失后重试：同一意图保留同一幂等键，服务端去重不产生重复操作', async () => {
    const backend = new MockLifecycleBackend();
    const h = await armedInstallPreview(backend);
    backend.transientCreateError = new TypeError('network lost');
    buttonByText('确认执行').click();
    await settle();
    // 第一次提交：网络错误（响应不明，操作未落地）
    expect(body().textContent).toContain('创建操作失败');
    expect(backend.operations.size).toBe(0);
    // 预览仍在、已读仍有效：重试同一意图 → 同一幂等键 → 服务端只留一行
    buttonByText('确认执行').click();
    await settle();
    expect(body().textContent).toContain('已创建操作');
    expect(backend.createKeyLog).toHaveLength(2);
    expect(backend.createKeyLog[0]).toBe(backend.createKeyLog[1]);
    expect(backend.operations.size).toBe(1);
  });

  it('意图（版本）变化后换新幂等键：不同内容不与旧键冲突', async () => {
    const backend = new MockLifecycleBackend();
    const h = await armedInstallPreview(backend);
    backend.transientCreateError = new TypeError('network lost');
    buttonByText('确认执行').click();
    await settle();
    expect(backend.createKeyLog).toHaveLength(1);
    // 改版本 = 意图变化 → 必须换键（同键不同内容会被服务端判 409 冲突）
    setInput('#lc-version', '0.56.2');
    expect(body().textContent).toContain('目标或版本已变化'); // 已读作废，重新确认
    q<HTMLInputElement>('input[id^="lc-ack"]').click();
    flushSync();
    buttonByText('确认执行').click();
    await settle();
    expect(body().textContent).toContain('已创建操作');
    expect(backend.createKeyLog).toHaveLength(2);
    expect(backend.createKeyLog[1]).not.toBe(backend.createKeyLog[0]);
    expect(backend.operations.size).toBe(1);
  });

  it('服务端明确拒绝（ApiError）后窗口关闭：重试提交用新键', async () => {
    const backend = new MockLifecycleBackend();
    backend.createError = new ApiError(422, { reason: 'UnsupportedCombination', message: 'rejected', timestamp: 't' });
    const h = await armedInstallPreview(backend);
    buttonByText('确认执行').click();
    await settle();
    expect(body().textContent).toContain('创建操作失败');
    // 明确拒绝 = 结果明确：同指纹重试也换新键（上一次提交已被服务端裁决）
    buttonByText('确认执行').click();
    await settle();
    expect(backend.createKeyLog).toHaveLength(2);
    expect(backend.createKeyLog[1]).not.toBe(backend.createKeyLog[0]);
  });

  it('成功关闭保留窗口：完成一次安装后，新的相同意图提交用新键', async () => {
    const backend = new MockLifecycleBackend();
    const h = await armedInstallPreview(backend);
    buttonByText('确认执行').click();
    await settle();
    expect(body().textContent).toContain('已创建操作');
    expect(backend.createKeyLog).toHaveLength(1);
    // 结果明确（操作已创建）：窗口关闭；重新发起同一版本安装 → 新键、新操作
    setInput('#lc-version', '0.56.1');
    buttonByText('预览并确认').click();
    flushSync();
    q<HTMLInputElement>('input[id^="lc-ack"]').click();
    flushSync();
    buttonByText('确认执行').click();
    await settle();
    expect(backend.createKeyLog).toHaveLength(2);
    expect(backend.createKeyLog[1]).not.toBe(backend.createKeyLog[0]);
    expect(backend.operations.size).toBe(2);
  });
});

describe('confirm 端点接线（KM-50 修复；对齐 T6 e2e 链路）', () => {
  it('预览确认 → 操作已确认并可派发；确认后逐目标结果可见（确认前不派发、确认后派发）', async () => {
    const h = await mountPage();
    await pickTarget('linux-build', '', 'ci');
    buttonByText('npm').click();
    flushSync();
    setInput('#lc-version', '0.56.1');
    buttonByText('预览并确认').click();
    flushSync();
    q<HTMLInputElement>('input[id^="lc-ack"]').click();
    flushSync();
    buttonByText('确认执行').click();
    await settle();

    // 页面的确认动作已接线：mock 里操作带有确认时刻且可派发（契约闸门）
    const ops = [...h.backend.operations.values()];
    expect(ops).toHaveLength(1);
    expect(ops[0].status.confirmedAt).not.toBeNull();
    expect(h.backend.dispatchable(ops[0].metadata.name)).toBe(true);
    // 确认前不派发：闸门由 mock 复刻（settleOperation 未确认时拒绝）——由 mock 单测锁定；
    // 这里断言确认后真实派发推进：settle 后结果面板可见成功与实测版本
    h.backend.settleOperation(ops[0].metadata.name, [
      { targetKey: () => true, state: 'succeeded', reason: 'verified-present', observedVersion: '0.56.1' },
    ]);
    setInput('#lc-lookup', ops[0].metadata.name);
    buttonByText('查询').click();
    await settle();
    expect(body().textContent).toContain('实测版本 0.56.1');
    expect(body().textContent).not.toContain('已创建但未确认');
  });

  it('未确认的操作如实呈现"确认前不派发"，面板确认入口可恢复派发', async () => {
    const h = await mountPage();
    // 绕过页面确认动作直接创建（模拟确认请求失败的残余状态）
    const created = await h.backend.createOperation({
      type: 'Upgrade',
      idempotencyKey: 'unconfirmed-seed',
      version: '0.56.1',
      targets: [{ machine: 'linux-build', env: '', user: 'ci', agent: 'codex', method: 'npm', package: '@openai/codex' }],
    });
    setInput('#lc-lookup', created.operation.metadata.name);
    buttonByText('查询').click();
    await settle();

    expect(body().textContent).toContain('已创建但未确认');
    expect(body().textContent).toContain('确认前操作不会派发');
    expect(body().textContent).not.toContain('确认于'); // 尚无确认时刻
    // 面板确认入口 → 确认 → 可派发
    buttonByText('确认派发').click();
    await settle();
    expect(body().textContent).toContain('已确认操作');
    expect(h.backend.dispatchable(created.operation.metadata.name)).toBe(true);
  });

  it('确认后不再出示确认入口；重复确认的幂等由契约保证，面板只有一张操作卡', async () => {
    const h = await mountPage();
    await pickTarget('linux-build', '', 'ci');
    buttonByText('npm').click();
    flushSync();
    setInput('#lc-version', '0.56.1');
    buttonByText('预览并确认').click();
    flushSync();
    q<HTMLInputElement>('input[id^="lc-ack"]').click();
    flushSync();
    buttonByText('确认执行').click();
    await settle();
    expect(h.backend.operations.size).toBe(1); // 幂等：同一意图只有一个操作行

    // 已确认操作：无"未确认"横幅、无重复确认按钮；重复确认的幂等语义（不产生
    // 重复变更）由 mock 单测与 T4 处理器共同锁定
    setInput('#lc-lookup', [...h.backend.operations.keys()][0]);
    buttonByText('查询').click();
    await settle();
    expect(body().textContent).not.toContain('已创建但未确认');
    expect(body().querySelectorAll('[data-testid="lc-op"]').length).toBe(1);
  });
});

describe('confirm 失败恢复与重新确认（评审修复）', () => {
  it('确认失败：期望状态不写、操作卡如实停在未确认，面板确认入口可恢复派发', async () => {
    const backend = new MockLifecycleBackend();
    backend.confirmError = new Error('network refused');
    const h = await mountPage(backend);
    await pickTarget('linux-build', '', 'ci');
    buttonByText('npm').click();
    flushSync();
    setInput('#lc-version', '0.56.1');
    buttonByText('预览并确认').click();
    flushSync();
    q<HTMLInputElement>('input[id^="lc-ack"]').click();
    flushSync();
    buttonByText('确认执行').click();
    await settle();

    expect(body().textContent).toContain('确认未完成');
    expect(body().textContent).toContain('确认前操作不会派发');
    // 确认前提未成立：期望状态未被触碰
    expect(await h.backend.getDesired('linux-build')).toBeNull();
    // 操作卡在面板上可见且带恢复入口（操作已在创建时进入追踪）
    expect(body().querySelectorAll('[data-testid="lc-op"]').length).toBe(1);
    expect(body().textContent).toContain('已创建但未确认');
    // 恢复：确认请求成功后可派发
    backend.confirmError = null;
    buttonByText('确认派发').click();
    await settle();
    const op = [...backend.operations.values()][0];
    expect(op.status.confirmedAt).not.toBeNull();
    expect(backend.dispatchable(op.metadata.name)).toBe(true);
  });

  it('重新确认清除影响变化标记并放行；页面不产生重复操作行', async () => {
    const h = await mountPage();
    const created = await h.backend.createOperation({
      type: 'Upgrade',
      idempotencyKey: 'reconfirm-seed',
      version: '0.56.1',
      targets: [{ machine: 'linux-build', env: '', user: 'ci', agent: 'codex', method: 'npm', package: '@openai/codex' }],
    });
    const opId = created.operation.metadata.name;
    await h.backend.confirmOperation(opId);
    // 模拟重试重发现发现影响变化：置 needsReconfirmation（闸门拦住）
    const op = h.backend.operations.get(opId)!;
    op.status.needsReconfirmation = true;
    op.status.reconfirmReason = 'version-mismatch';
    expect(h.backend.dispatchable(opId)).toBe(false);

    setInput('#lc-lookup', opId);
    buttonByText('查询').click();
    await settle();
    expect(body().textContent).toContain('影响已变化，需重新确认');
    const before = h.backend.operations.size;
    buttonByText('重新确认并继续').click();
    await settle();
    expect(body().textContent).toContain('已重新确认');
    expect(h.backend.operations.size).toBe(before); // 无重复操作行
    expect(h.backend.dispatchable(opId)).toBe(true);
    expect(body().textContent).not.toContain('影响已变化');
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

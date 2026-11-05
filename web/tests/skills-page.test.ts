// @vitest-environment happy-dom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushSync, mount, unmount } from 'svelte';
import Skills from '../src/routes/Skills.svelte';
import { setLocale } from '../src/lib/i18n/index.js';
import type { FleetStore } from '../src/lib/state/store.svelte.js';
import type { Skill } from '../src/lib/api/types.js';

let app: ReturnType<typeof mount> | null = null;

function skill(name: string, resolved: string, latest?: string): Skill {
  return {
    metadata: { name },
    spec: { source: 'github.com/example/skills', ref: resolved },
    status: {
      resolvedRevision: resolved,
      contentDigest: 'sha256:cccc',
      ...(latest === undefined ? {} : { latestRevision: latest }),
    },
  };
}

function pageStore(items: Skill[], calls?: string[]): FleetStore {
  return {
    loading: {},
    errors: {},
    machines: [],
    profiles: [],
    skills: items,
    machinesUsingSkill: () => [],
    client: {
      createSkill: () => {
        calls?.push('create');
      },
      updateSkill: () => {
        calls?.push('update');
      },
      deleteSkill: () => {
        calls?.push('delete');
      },
    },
  } as unknown as FleetStore;
}

function buttonByText(root: ParentNode, text: string): HTMLButtonElement {
  const el = [...root.querySelectorAll('button')].find((button) => button.textContent?.trim() === text);
  if (!el) throw new Error(`missing button: ${text}`);
  return el as HTMLButtonElement;
}

function mountSkills(items: Skill[], calls?: string[]) {
  app = mount(Skills, { target: document.body, props: { store: pageStore(items, calls) } });
  flushSync();
}

beforeEach(() => {
  setLocale('zh-CN');
  document.body.innerHTML = '';
});

afterEach(() => {
  if (app) unmount(app);
  app = null;
  document.body.innerHTML = '';
  vi.unstubAllGlobals();
});

describe('技能页', () => {
  it('标题、导语和添加来源', () => {
    mountSkills([]);
    expect(document.querySelector('h1')?.textContent?.trim()).toBe('技能');
    expect(document.body.textContent).toContain('技能装在某台机器的某个代理上。');
    expect(buttonByText(document.body, '添加来源').textContent?.trim()).toBe('添加来源');
  });

  it('当前夹具显示已是当前', () => {
    mountSkills([skill('current-skill', 'v1.2.0', 'v1.2.0')]);
    const row = document.querySelector('[data-skill="current-skill"]');
    const sentence = row?.querySelector('[data-posture="current"]')?.textContent?.trim();
    expect(sentence).toBe('已是当前');
    expect(row?.textContent).not.toContain('可升到');
  });

  it('落后夹具显示可升到', () => {
    mountSkills([skill('behind-skill', 'v1.2.0', 'v1.3.0')]);
    const row = document.querySelector('[data-skill="behind-skill"]');
    const sentence = row?.querySelector('[data-posture="behind"]')?.textContent?.trim();
    expect(sentence).toContain('可升到');
    expect(sentence).not.toBe('已是当前');
    expect(row?.textContent).toContain('升级技能');
    expect(row?.textContent).not.toContain('已是当前');
  });

  it('空来源名字显示先写来源的名字，关闭后对话框消失', () => {
    mountSkills([]);
    buttonByText(document.body, '添加来源').click();
    flushSync();
    const dialog = document.querySelector('[role="dialog"]');
    expect(dialog?.querySelector('h2')?.textContent?.trim()).toBe('添加来源');
    buttonByText(dialog ?? document.body, '添加来源').click();
    flushSync();
    expect(document.body.textContent).toContain('先写来源的名字');
    buttonByText(dialog ?? document.body, '关闭').click();
    flushSync();
    expect(document.querySelector('h1')?.textContent?.trim()).toBe('技能');
    expect(document.querySelector('[role="dialog"]')).toBeNull();
  });

  it('升级对话框有关闭，并且不安装', () => {
    const calls: string[] = [];
    vi.stubGlobal('fetch', () => {
      calls.push('fetch');
      return Promise.resolve(new Response('{}', { status: 200 }));
    });
    mountSkills([skill('behind-skill', 'v1.2.0', 'v1.3.0')], calls);
    buttonByText(document.body, '升级技能').click();
    flushSync();
    const dialog = document.querySelector('[role="dialog"]');
    expect(dialog?.textContent).toContain('关闭');
    const labels = [...(dialog?.querySelectorAll('button') ?? [])].map((button) => button.textContent?.trim());
    expect(labels).toEqual(['关闭']);
    buttonByText(dialog ?? document.body, '关闭').click();
    flushSync();
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    expect(calls).toEqual([]);
  });
});

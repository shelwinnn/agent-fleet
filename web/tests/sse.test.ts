/**
 * SSE 解析与选择性重取（FR-14.2、§23.6）。
 */
import { describe, expect, it } from 'vitest';
import { parseSSEChunk, parseSSERecord } from '../src/lib/api/sse.js';
import { planRefetch, RevisionTracker } from '../src/lib/state/refetch.js';
import type { ResourceEvent } from '../src/lib/api/types.js';

describe('SSE 记录解析', () => {
  it('按契约解析 event + data', () => {
    expect(parseSSERecord('event: machines\ndata: {"id":"gpu-home","revision":7}')).toEqual({
      resource: 'machines',
      id: 'gpu-home',
      revision: 7,
    });
  });

  it('忽略注释/保活行与非法 JSON', () => {
    expect(parseSSERecord(': keepalive')).toBeNull();
    expect(parseSSERecord('retry: 3000')).toBeNull();
    expect(parseSSERecord('event: machines\ndata: {oops')).toBeNull();
    expect(parseSSERecord('event: unknown-type\ndata: {"id":"x","revision":1}')).toBeNull();
  });

  it('分块解析：只有遇到空行才算一条完整记录（跨 chunk 拼接）', () => {
    const first = parseSSEChunk('event: machines\ndata: {"id":"a","revision":1}\n\nevent: pro');
    expect(first.records).toHaveLength(1);
    expect(parseSSERecord(first.records[0])?.id).toBe('a');
    expect(first.rest).toBe('event: pro');

    const second = parseSSEChunk(first.rest + 'files\ndata: {"id":"b","revision":2}\n\n');
    expect(second.records).toHaveLength(1);
    expect(parseSSERecord(second.records[0])).toEqual({ resource: 'profiles', id: 'b', revision: 2 });
  });
});

describe('revision 记账（选择性重取的第一道闸）', () => {
  it('只接受更新的 revision，丢弃重复与更旧的事件', () => {
    const tracker = new RevisionTracker();
    expect(tracker.accept('machines', 'ws-1', 3)).toBe(true);
    expect(tracker.accept('machines', 'ws-1', 3)).toBe(false);
    expect(tracker.accept('machines', 'ws-1', 2)).toBe(false);
    expect(tracker.accept('machines', 'ws-1', 4)).toBe(true);
    // 不同对象互不影响；类型不同也互不影响。
    expect(tracker.accept('machines', 'ws-2', 1)).toBe(true);
    expect(tracker.accept('profiles', 'ws-1', 1)).toBe(true);
    tracker.reset();
    expect(tracker.accept('machines', 'ws-1', 1)).toBe(true);
  });
});

describe('事件 → 重取计划', () => {
  const ev = (resource: ResourceEvent['resource'], id: string, revision = 1): ResourceEvent => ({
    resource,
    id,
    revision,
  });

  it('machines 事件刷新列表；命中当前打开的机器时连详情一起刷新', () => {
    const plan = planRefetch([ev('machines', 'ws-1')], { activeMachine: 'ws-1' });
    expect([...plan.lists]).toContain('machines');
    expect([...plan.machines]).toEqual(['ws-1']);

    const other = planRefetch([ev('machines', 'ws-2')], { activeMachine: 'ws-1' });
    expect([...other.lists]).toEqual(['machines']);
    expect(other.machines.size).toBe(0);
  });

  it('operations 事件的 id 是操作 id：经缓存映射定位机器', () => {
    const plan = planRefetch([ev('operations', 'op-1')], {
      activeMachine: 'ws-1',
      operationOwners: new Map([['op-1', 'ws-1']]),
    });
    expect([...plan.machines]).toEqual(['ws-1']);
    expect([...plan.lists]).toContain('operations');
  });

  it('未知操作事件不猜测归属，但对当前机器做一次操作列表重取', () => {
    const plan = planRefetch([ev('operations', 'op-unknown')], { activeMachine: 'ws-1' });
    expect([...plan.machines]).toEqual(['ws-1']);
    expect([...plan.lists]).toContain('operations');
  });

  it('profiles/skills/providers/deployments 各自刷新对应列表', () => {
    const plan = planRefetch([ev('profiles', 'p'), ev('skills', 's'), ev('providers', 'pv'), ev('deployments', 'd')]);
    expect([...plan.lists].sort()).toEqual(['deployments', 'profiles', 'providers', 'skills']);
    expect(plan.deployments.size).toBe(0); // 未打开该发布详情时不取详情
  });
});

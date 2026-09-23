/**
 * Machines 列表的列与"需关注"判定（spec §24.2 列定义；FR-1.9 三态）。
 *
 * 列：Name | OS/Arch | Profile | agentd | SSH | Drift | Reconciled | Last Seen
 * 注意 Drift 与 Reconciled 必须是三态呈现：Unknown 不能渲染成"一致"（M5）。
 */
import type { ConditionStatus, Machine } from '../api/types';
import { allConditionSummaries, conditionView, driftInputFromMachine, driftView, type DriftView, type Tone } from './drift';
import { formatRelativeTime } from './format';

export interface MachineRow {
  name: string;
  osArch: string;
  profile: string;
  agentd: { label: string; tone: Tone; version?: string; connected: ConditionStatus };
  ssh: { label: string; tone: Tone; mode: 'agentd' | 'ssh' };
  drift: DriftView;
  reconciled: { label: string; tone: Tone; status: ConditionStatus; reason?: string };
  lastSeen?: string;
  lastSeenText: string;
  degraded: boolean;
  /** "需关注"= 漂移 / 降级 / 任一必需条件为 Unknown 或 False。 */
  needsAttention: boolean;
  attentionReasons: string[];
  unresolvedPhase?: string;
}

export function machineRow(machine: Machine, now: Date = new Date()): MachineRow {
  const status = machine.status ?? {};
  const conditions = status.conditions;
  const sshCond = conditionView(conditions, 'SSHReachable');
  const agentCond = conditionView(conditions, 'AgentConnected');
  const inventoryCond = conditionView(conditions, 'InventoryReady');
  const reconciledCond = conditionView(conditions, 'Reconciled');
  const degradedCond = conditionView(conditions, 'Degraded');
  const mode = (machine.spec?.managementMode as 'agentd' | 'ssh') ?? 'agentd';

  const drift = driftView(driftInputFromMachine(machine), now);

  const agentTone: Tone =
    agentCond?.status === 'True' ? 'ok' : agentCond?.status === 'False' ? 'danger' : 'warn';
  const sshTone: Tone = mode === 'ssh'
    ? sshCond?.status === 'True'
      ? 'ok'
      : sshCond?.status === 'False'
        ? 'danger'
        : 'muted'
    : sshCond?.status === 'True'
      ? 'ok'
      : sshCond?.status === 'False'
        ? 'danger'
        : 'muted';

  const attentionReasons: string[] = [];
  if (drift.state === 'drifted') attentionReasons.push('已漂移');
  if (drift.state === 'unknown') attentionReasons.push(`drift 未知：${drift.reasonCode ?? 'Unknown'}`);
  if (degradedCond?.status === 'True') attentionReasons.push('Degraded=True');
  if (mode === 'agentd' && agentCond?.status !== 'True') attentionReasons.push('agentd 未在线');
  if (inventoryCond?.status === 'Unknown') attentionReasons.push('inventory 未就绪');

  return {
    name: machine.metadata.name,
    osArch: status.os || status.arch ? `${status.os ?? '?'}/${status.arch ?? '?'}` : '—',
    profile: machine.spec?.profileRef ?? '—',
    agentd: {
      label:
        agentCond?.status === 'True'
          ? `在线 ${status.agentdVersion ?? ''}`.trim()
          : agentCond?.status === 'False'
            ? '离线'
            : '未知',
      tone: agentTone,
      version: status.agentdVersion,
      connected: agentCond?.status ?? 'Unknown',
    },
    ssh: { label: sshCond?.status ?? 'Unknown', tone: sshTone, mode },
    drift,
    reconciled: {
      label:
        reconciledCond?.status === 'True'
          ? '已收敛'
          : reconciledCond?.status === 'False'
            ? '未收敛'
            : '未知',
      tone: reconciledCond?.status === 'True' ? 'ok' : reconciledCond?.status === 'False' ? 'warn' : 'warn',
      status: reconciledCond?.status ?? 'Unknown',
      reason: reconciledCond?.reason,
    },
    lastSeen: status.lastHeartbeatAt ?? status.lastInventoryAt,
    lastSeenText: formatRelativeTime(status.lastHeartbeatAt ?? status.lastInventoryAt ?? '', now),
    degraded: degradedCond?.status === 'True',
    needsAttention: attentionReasons.length > 0,
    attentionReasons,
    unresolvedPhase: status.unresolvedOperation?.phase,
  };
}

export function machineRows(machines: Machine[], now: Date = new Date()): MachineRow[] {
  return machines.map((m) => machineRow(m, now));
}

/** Overview 顶部卡片（spec §24.1）。 */
export interface OverviewStats {
  totalMachines: number;
  daemonOnline: number;
  sshReachable: number;
  drifted: number;
  degraded: number;
  activeDeployments: number;
  unknownDrift: number;
}

export function overviewStats(
  machines: Machine[],
  deployments: { status?: { phase?: string } }[],
): OverviewStats {
  const rows = machineRows(machines);
  return {
    totalMachines: rows.length,
    daemonOnline: rows.filter((r) => r.agentd.connected === 'True').length,
    sshReachable: rows.filter((r) => r.ssh.label === 'True').length,
    drifted: rows.filter((r) => r.drift.state === 'drifted').length,
    degraded: rows.filter((r) => r.degraded).length,
    activeDeployments: deployments.filter((d) =>
      ['Pending', 'Canary', 'RollingOut', 'Paused'].includes(d.status?.phase ?? ''),
    ).length,
    unknownDrift: rows.filter((r) => r.drift.state === 'unknown').length,
  };
}

export { allConditionSummaries };

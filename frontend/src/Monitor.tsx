import type { WidgetCandidate } from './widgets/protocol';
import { WidgetHost } from './widgets/WidgetHost';
import { WidgetLibrary } from './widgets/WidgetLibrary';
import { useId, useRef, useState } from 'react';
import {
  Activity,
  Bell,
  ChevronDown,
  FileCode2,
  Maximize2,
  Minimize2,
  X,
} from 'lucide-react';
import { MonitorPanel, WorkbenchCommand } from './api';
import { useStoredFlag } from './layout';
import { realtimePrompt } from './monitoring-api';
import { CollectorConfig } from './CollectorConfig';

export type MonitorSize = 'normal' | 'large' | 'maximized';

export function overviewValues(content = '') {
  return Object.fromEntries(
    content.split(/\r?\n/).flatMap((line) => {
      const match = line.match(/^(HOST|UPTIME|LOAD|MEM|DISK|PROCS)=(.*)$/);
      return match ? [[match[1], match[2].trim()]] : [];
    }),
  );
}
const updated = (panel?: MonitorPanel) =>
  !!panel?.updatedAt && !panel.updatedAt.startsWith('0001-');
const stalePanel = (panel: MonitorPanel, now: number) =>
  updated(panel) && now - Date.parse(panel.updatedAt) > Math.max(15000, panel.intervalMs * 3);

function Metric({ label, value, percent }: { label: string; value?: string; percent?: boolean }) {
  const number = percent ? Number(value?.match(/\(([\d.]+)%\)/)?.[1]) : NaN;
  return (
    <div className={'monitor-metric ' + (number >= 90 ? 'warning' : '')}>
      <small>{label}</small>
      <strong title={value}>{value || '—'}</strong>
      {Number.isFinite(number) && (
        <div className="metric-track">
          <span style={{ width: Math.min(100, Math.max(0, number)) + '%' }} />
        </div>
      )}
    </div>
  );
}

export function Monitor({
  panels,
  sessionId = '',
  active = true,
  candidates = [],
  online,
  pending,
  now,
  size = 'normal',
  onSizeChange,
  onAction,
  aiReady = true,
  onOpenMonitoring,
  alertCount = 0,
  systemCollection = false,
}: {
  panels: MonitorPanel[];
  sessionId?: string;
  active?: boolean;
  candidates?: WidgetCandidate[];
  online: boolean;
  pending: boolean;
  now: number;
  size?: MonitorSize;
  onSizeChange?: (size: MonitorSize) => void;
  onAction: (command: WorkbenchCommand) => Promise<boolean>;
  aiReady?: boolean;
  onOpenMonitoring?: () => void;
  alertCount?: number;
  systemCollection?: boolean;
}) {
  const detailsId = useId();
  const [expanded, setExpanded] = useStoredFlag('aissh.monitor.expanded', true);
  const sizeButton = useRef<HTMLButtonElement>(null);
  const collectorButton = useRef<HTMLButtonElement>(null);
  const enlarged = size !== 'normal';
  const showDetails = expanded || enlarged;
  const [selected, setSelected] = useState('system');
  const [custom, setCustom] = useState(false);
  const [command, setCommand] = useState('');
  const [aiOpen, setAIOpen] = useState(false);
  const [aiPrompt, setAIPrompt] = useState('');
  const [collectorOpen, setCollectorOpen] = useState(false);
  const current = panels.find((p) => p.id === selected) || panels[0];
  const overview = panels.find((p) => p.id === 'system');
  const metrics = overviewValues(overview?.content);
  const failed = panels.some((p) => p.error);
  const stale = panels.some((p) => stalePanel(p, now));
  const status = !online
    ? '未连接'
    : failed
      ? '采集异常'
      : stale
        ? '数据过期'
        : !updated(overview)
          ? '等待采集'
          : '实时';
  return (
    <section
      className="native-monitor"
      aria-label="实时面板"
      onKeyDown={(event) => {
        if (
          event.key === 'Escape' &&
          enlarged &&
          !(event.target as HTMLElement).closest('dialog') &&
          !event.defaultPrevented &&
          !event.nativeEvent.isComposing
        ) {
          event.preventDefault();
          event.stopPropagation();
          onSizeChange?.('normal');
          sizeButton.current?.focus();
        }
      }}
    >
      <div className="monitor-toolbar">
        <button
          className="monitor-heading"
          title={showDetails ? '收起实时面板' : '展开实时面板'}
          aria-expanded={showDetails}
          aria-controls={detailsId}
          onClick={() => {
            if (enlarged) onSizeChange?.('normal');
            setExpanded(!showDetails);
          }}
        >
          <Activity size={16} />
          <strong>实时面板</strong>
          <span
            className={'monitor-health ' + (failed || stale || !online ? 'warning' : '')}
            title={
              updated(current)
                ? '上次更新：' +
                  new Date(current!.updatedAt).toLocaleTimeString('zh-CN', { hour12: false })
                : '尚无采集数据'
            }
          >
            {status}
          </span>
          {!showDetails && (
            <span className="monitor-summary">
              负载 {metrics.LOAD || '—'} · 内存 {metrics.MEM || '—'}
            </span>
          )}
          <ChevronDown size={15} className={showDetails ? '' : 'folded'} />
        </button>
        <div className="monitor-actions" role="group" aria-label="监控操作">
          <div className="monitor-action-group">
            <button
              type="button"
              className="button small"
              aria-pressed={systemCollection}
              aria-label={'自动采集：' + (systemCollection ? '开启' : '关闭')}
              title="控制自动运行远端监控命令；关闭时不进行周期采集"
              disabled={!online || pending}
              onClick={() =>
                void onAction({ kind: 'system-collection', text: String(!systemCollection) })
              }
            >
              自动采集：{systemCollection ? '开' : '关'}
            </button>
            {sessionId && (
              <WidgetLibrary
                compact
                sessionId={sessionId}
                panelId={current?.widget ? current.id : undefined}
              />
            )}
            <select
              aria-label="添加监控"
              disabled={!online || pending}
              value=""
              onChange={async (e) => {
                const kind = e.target.value;
                setExpanded(true);
                if (kind === 'ai') {
                  setCustom(false);
                  setAIOpen(true);
                  return;
                }
                if (kind === 'custom') {
                  setAIOpen(false);
                  setCustom(true);
                  return;
                }
                if (kind && (await onAction({ kind: 'monitor-open', panelId: kind })))
                  setSelected('desktop-' + kind);
              }}
            >
              <option value="">+ 添加监控</option>
              <option value="ai" disabled={!aiReady}>
                AI 添加面板
              </option>
              {[
                ['processes', '进程'],
                ['disk', '磁盘'],
                ['network', '网络'],
                ['services', '服务'],
                ['custom', '自定义'],
              ].map(([id, title]) => (
                <option key={id} value={id}>
                  {title}
                </option>
              ))}
            </select>
          </div>
          <div className="monitor-action-group">
            {onOpenMonitoring && (
              <button
                type="button"
                className="icon-button monitor-alerts"
                aria-label="当前会话告警监控"
                title={alertCount ? `告警监控 · ${alertCount} 个任务` : '告警监控'}
                onClick={onOpenMonitoring}
              >
                <Bell size={14} />
                {alertCount > 0 && <span className="monitor-alert-count">{alertCount}</span>}
              </button>
            )}
            {current && (
              <>
                <button
                  type="button"
                  className="icon-button"
                  aria-label="采集配置"
                  aria-haspopup="dialog"
                  title="采集配置"
                  ref={collectorButton}
                  onClick={() => setCollectorOpen(true)}
                >
                  <FileCode2 size={13} />
                </button>
                {!current.pinned && (
                  <button
                    className="icon-button"
                    aria-label={'关闭监控 ' + current.title}
                    title={'关闭监控 ' + current.title}
                    disabled={!online || pending}
                    onClick={() => void onAction({ kind: 'monitor-close', panelId: current.id })}
                  >
                    <X size={13} />
                  </button>
                )}
              </>
            )}
          </div>
          <div className="monitor-action-group">
            {onSizeChange && (
              <button
                ref={sizeButton}
                type="button"
                className="icon-button monitor-resize"
                aria-label={
                  size === 'maximized' ? '还原监控' : size === 'large' ? '最大化监控' : '放大监控'
                }
                title={
                  size === 'maximized'
                    ? '还原监控（Esc）'
                    : size === 'large'
                      ? '最大化至整个工作区'
                      : '放大至工作区约一半高度'
                }
                onClick={() =>
                  onSizeChange(
                    size === 'normal' ? 'large' : size === 'large' ? 'maximized' : 'normal',
                  )
                }
              >
                {size === 'maximized' ? <Minimize2 size={14} /> : <Maximize2 size={14} />}
              </button>
            )}
            {size === 'large' && onSizeChange && (
              <button
                type="button"
                className="icon-button monitor-resize"
                aria-label="还原监控"
                title="还原监控（Esc）"
                onClick={() => {
                  onSizeChange('normal');
                  sizeButton.current?.focus();
                }}
              >
                <Minimize2 size={14} />
              </button>
            )}
          </div>
        </div>
      </div>
      {showDetails && (
        <div id={detailsId} className="monitor-details">
          <div className="monitor-tabs">
            <div role="group" aria-label="监控项目">
              {panels.map((p) => (
                <button
                  key={p.id}
                  aria-pressed={current?.id === p.id}
                  onClick={() => setSelected(p.id)}
                >
                  {p.title}
                  {p.error && ' !'}
                </button>
              ))}
            </div>
          </div>
          {candidates.map((c) => (
            <div key={c.id} className="widget-progress" role="status">
              正在预检 · {c.title}{' '}
              <button
                type="button"
                className="button small"
                onClick={() =>
                  void onAction({ kind: 'widget-cancel', panelId: c.panelId, candidateId: c.id })
                }
              >
                取消修改
              </button>
            </div>
          ))}
          {aiOpen && (
            <form
              className="monitor-ai-form"
              onSubmit={async (e) => {
                e.preventDefault();
                if (!aiPrompt.trim() || !aiReady || pending) return;
                if (await onAction({ kind: 'prompt', text: realtimePrompt(aiPrompt) })) {
                  setAIOpen(false);
                  setAIPrompt('');
                }
              }}
            >
              <label>
                描述你想查看的数据
                <textarea
                  aria-label="AI 实时面板需求"
                  rows={2}
                  required
                  maxLength={4000}
                  placeholder="例如：每 5 秒展示 nginx 的进程数、CPU 和内存占用"
                  value={aiPrompt}
                  onChange={(e) => setAIPrompt(e.target.value)}
                />
              </label>
              <div>
                <small>AI 会在下方对话中创建实时面板。异常提醒请到侧栏「告警监控」添加。</small>
                <button
                  className="button primary small"
                  disabled={!online || pending || !aiReady || !aiPrompt.trim()}
                >
                  让 AI 创建
                </button>
                <button type="button" className="button small" onClick={() => setAIOpen(false)}>
                  取消
                </button>
              </div>
            </form>
          )}
          {custom && (
            <form
              className="monitor-custom"
              onSubmit={async (e) => {
                e.preventDefault();
                if (await onAction({ kind: 'monitor-open', panelId: 'custom', text: command })) {
                  setCustom(false);
                  setCommand('');
                }
              }}
            >
              <input
                aria-label="只读监控命令"
                placeholder="输入只读监控命令，如 tail -n 30 /var/log/nginx/error.log"
                value={command}
                onChange={(e) => setCommand(e.target.value)}
                required
              />
              <button className="button small" disabled={!online || pending}>
                添加
              </button>
              <button
                type="button"
                className="icon-button"
                aria-label="取消添加监控"
                onClick={() => setCustom(false)}
              >
                <X size={14} />
              </button>
            </form>
          )}
          {current?.error && (
            <p className="monitor-warning" role="status">
              {current.widget ? '数据状态：' : '采集失败：'}{current.error} {!current.widget && current.content && '· 下方保留上次数据'}
            </p>
          )}
          {current && !current.error && (stalePanel(current, now) || !online) && (
            <p className="monitor-warning" role="status">
              数据已停止更新，下方为上次采集结果。
            </p>
          )}
          {current?.widget ? (
            <>
              <WidgetHost
                sessionId={sessionId}
                panelId={current.id}
                meta={current.widget}
                visible={active}
              />
              <details className="widget-raw">
                <summary>查看原始输出</summary>
                <pre className="monitor-output">{current.content || '正在等待采集结果…'}</pre>
              </details>
            </>
          ) : !current || current.id === 'system' ? (
            <div className="monitor-metrics">
              <Metric label="系统负载 · 1 / 5 / 15 分钟" value={metrics.LOAD} />
              <Metric label="内存" value={metrics.MEM} percent />
              <Metric label="根目录磁盘" value={metrics.DISK} percent />
              <Metric label="进程数" value={metrics.PROCS} />
            </div>
          ) : (
            <pre className="monitor-output">{current.content || '正在等待采集结果…'}</pre>
          )}
        </div>
      )}
      {active && collectorOpen && current && (
        <CollectorConfig
          key={current.id + ':' + current.command}
          panel={current}
          onClose={() => {
            setCollectorOpen(false);
            requestAnimationFrame(() => collectorButton.current?.focus());
          }}
        />
      )}
    </section>
  );
}

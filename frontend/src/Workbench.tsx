import { WidgetPreviews } from './widgets/WidgetHost';
import { useEffect, useRef, useState } from 'react';
import { ArrowDown, FolderOpen, Send, Sparkles } from 'lucide-react';
import { api, errorText, Session, sessionLive, WorkbenchCommand, WorkbenchState } from './api';
import { Empty, ErrorBox } from './components';
import { Terminal } from './Terminal';
import { Monitor, MonitorSize } from './Monitor';
import { ChatEntry } from './ChatEntry';
import { sessionAddress } from './api';
import { CommandReview } from './CommandReview';
import { useChatSubmit } from './useChatSubmit';
import { useWorkbenchSplit } from './useWorkbenchSplit';
import { WorkspaceEditor } from './Workspace';
import { CodexResume } from './CodexResume';

export function Workbench({
  session,
  active,
  title,
  onReconnect,
  canReconnect,
  onOpenMonitoring,
}: {
  session: Session;
  active: boolean;
  title: string;
  onReconnect: () => void;
  canReconnect: boolean;
  onOpenMonitoring: () => void;
}) {
  const [state, setState] = useState<WorkbenchState | null>(null);
  const [error, setError] = useState('');
  const [pending, setPending] = useState(false);
  const [terminalFocusRequest, setTerminalFocusRequest] = useState(0);
  const [prompt, setPrompt] = useState('');
  const [editingWorkspace, setEditingWorkspace] = useState(false);
  const [now, setNow] = useState(Date.now());
  const [follow, setFollow] = useState(true);
  const [monitorSize, setMonitorSize] = useState<MonitorSize>('normal');
  const split = useWorkbenchSplit(active && monitorSize !== 'maximized');
  const messages = useRef<HTMLDivElement>(null);
  const chatSubmit = useChatSubmit();
  useEffect(() => {
    let disposed = false;
    const accept = (value: WorkbenchState | null) => {
      if (!disposed && value)
        setState((previous) =>
          !previous || value.updatedAt >= previous.updatedAt ? value : previous,
        );
    };
    const off = window.runtime?.EventsOn('workbench:' + session.id, accept);
    void api
      .GetWorkbench(session.id)
      .then(accept)
      .catch((e) => {
        if (!disposed) setError(errorText(e));
      });
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => {
      disposed = true;
      off?.();
      clearInterval(timer);
    };
  }, [session.id]);
  useEffect(() => {
    if (!active || monitorSize === 'maximized' || !state?.updatedAt) return;
    const updatedAt = state.updatedAt;
    const read = () => {
      if (!document.hidden && document.hasFocus()) {
        // Retry on the next snapshot/focus event if the bridge is temporarily unavailable.
        void api.ReadPetSession(session.id, updatedAt).catch(() => {});
      }
    };
    read();
    window.addEventListener('focus', read);
    document.addEventListener('visibilitychange', read);
    return () => {
      window.removeEventListener('focus', read);
      document.removeEventListener('visibilitychange', read);
    };
  }, [active, session.id, state?.updatedAt, monitorSize]);
  const online = sessionLive(session) && !!state && now - Date.parse(state.updatedAt) < 5000;
  useEffect(() => {
    if (active && monitorSize !== 'maximized' && follow && messages.current)
      messages.current.scrollTop = messages.current.scrollHeight;
  }, [state?.messages, active, follow, monitorSize]);
  async function action(command: WorkbenchCommand) {
    if (pending) return false;
    setPending(true);
    setError('');
    try {
      await api.WorkbenchAction(session.id, command);
      return true;
    } catch (e) {
      setError(errorText(e));
      return false;
    } finally {
      setPending(false);
    }
  }
  const blocked =
    !online ||
    pending ||
    state?.busy ||
    state?.executing ||
    !!state?.proposal ||
    state?.terminalTakeover;
  useEffect(() => {
    if (active && state?.terminalTakeover) setTerminalFocusRequest((value) => value + 1);
  }, [active, state?.terminalTakeover]);
  const status = !sessionLive(session)
    ? '连接已结束'
    : !state
      ? '正在连接工作台…'
      : !online
        ? '工作台连接中断'
        : state.terminalTakeover
          ? '终端已接管，请在右侧终端完成或退出审批'
          : state.executing
            ? '正在执行命令'
            : state.busy
              ? state.status || 'AI 正在处理…'
              : state.proposal
                ? '等待确认命令'
                : '就绪';
  return (
    <div
      ref={split.ref}
      className={
        'native-workbench' +
        (split.dragging ? ' workbench-resizing' : '') +
        (monitorSize === 'normal' ? '' : ' monitor-enlarged monitor-' + monitorSize)
      }
      style={{ ...split.style, display: active ? 'grid' : 'none' }}
    >
      <WidgetPreviews sessionId={session.id} candidates={state?.widgetCandidates} />
      <Monitor
        sessionId={session.id}
        active={active}
        candidates={state?.widgetCandidates}
        panels={state?.panels || []}
        systemCollection={state?.systemCollection ?? false}
        online={online}
        pending={pending}
        now={now}
        size={monitorSize}
        onSizeChange={setMonitorSize}
        aiReady={!state?.busy && !state?.executing && !state?.proposal}
        onAction={action}
        onOpenMonitoring={onOpenMonitoring}
        alertCount={state?.alertMonitors?.length || 0}
      />
      <section id={split.chatId} className="native-chat" aria-label="AI 对话工作区">
        <header className="native-chat-heading">
          <Sparkles size={17} />
          <div>
            <strong>{title || 'AI 对话'}</strong>
            <small title={sessionAddress(session)}>
              {session.name} · {sessionAddress(session)}
            </small>
          </div>
        </header>
        <CodexResume threadId={state?.codexThreadId} />
        {!!state?.modelOptions?.length && (
          <div className="chat-workspace chat-model">
            <label htmlFor={'model-' + session.id}>AI 模型</label>
            <select
              id={'model-' + session.id}
              aria-label="当前 AI 模型"
              value={state.model || ''}
              disabled={!!blocked}
              onChange={(event) => void action({ kind: 'model', text: event.target.value })}
            >
              {state.modelOptions.map((model) => (
                <option key={model} value={model}>
                  {model}
                </option>
              ))}
            </select>
          </div>
        )}
        {state && (
          <div className="chat-workspace" aria-label="当前 AI 工作目录">
            <FolderOpen size={14} />
            <span title={state.workspace || ''}>
              {state.workspaceSupported
                ? state.workspace || '未设置本机工作目录'
                : '当前 AI 不支持本机工作目录'}
            </span>
            {state.workspaceSupported && (
              <>
                {state.workspace && <small>{state.workspaceWritable ? '可修改' : '只读'}</small>}
                <button
                  className="button small"
                  type="button"
                  disabled={blocked}
                  onClick={() => {
                    setError('');
                    setEditingWorkspace(true);
                  }}
                >
                  {state.workspace ? '切换目录' : '设置目录'}
                </button>
              </>
            )}
          </div>
        )}
        {editingWorkspace && state && (
          <WorkspaceEditor
            workspace={state.workspace}
            busy={pending}
            blocked={!!blocked}
            error={error}
            onSave={(directory) => action({ kind: 'workspace', text: directory })}
            onClose={() => setEditingWorkspace(false)}
          />
        )}
        <div
          className="native-chat-messages"
          ref={messages}
          onScroll={() => {
            const el = messages.current;
            if (el) setFollow(el.scrollHeight - el.scrollTop - el.clientHeight < 60);
          }}
        >
          {!state?.messages.length && (
            <Empty
              icon={<Sparkles size={26} />}
              title="和 AI 一起处理问题"
              description="描述你要排查的问题，AI 会结合这台服务器的终端输出和监控数据分析。"
            />
          )}
          {state?.messages.map((m, index) => (
            <ChatEntry
              key={m.time + ':' + index}
              message={m}
              active={active && monitorSize !== 'maximized'}
              pending={m.proposal?.id === state.proposal?.id && !!state.proposal}
            />
          ))}
          {state?.busy && !state.messages.some((m) => m.role === 'assistant_stream') && (
            <div className="chat-working" role="status">
              <span className="activity-spinner" />
              {status}
            </div>
          )}
        </div>
        {!follow && (
          <button className="chat-follow button small" onClick={() => setFollow(true)}>
            <ArrowDown size={13} />
            回到最新消息
          </button>
        )}
        {state?.proposal && (
          <CommandReview
            key={JSON.stringify([session.id, state.proposal.id, state.proposal.command])}
            proposal={state.proposal}
            target={sessionAddress(session)}
            active={active && monitorSize !== 'maximized'}
            online={online}
            pending={pending}
            busy={state.busy}
            canApprove={state.canApprove}
            approvalStatus={state.approvalStatus}
            error={error}
            onAction={action}
          />
        )}
        <form
          className="native-composer"
          onSubmit={async (e) => {
            e.preventDefault();
            if (blocked || !prompt.trim()) return;
            if (await action({ kind: 'prompt', text: prompt })) {
              setPrompt('');
              setFollow(true);
            }
          }}
        >
          <ErrorBox error={error} />
          <div className="chat-status" role="status">
            <span
              className={
                'status-dot ' + (online ? (state?.busy ? 'starting' : 'running') : 'muted')
              }
            />
            {status}
          </div>
          <textarea
            aria-label="向 AI 提问"
            placeholder="描述问题，或请 AI 分析右侧的终端输出…"
            rows={3}
            value={prompt}
            onChange={(e) => setPrompt(e.target.value)}
            {...chatSubmit}
          />
          <div className="composer-actions">
            <button
              className="button small"
              type="button"
              disabled={!online || pending}
              onClick={async () => {
                if (await action({ kind: 'terminal-takeover' })) {
                  setTerminalFocusRequest((value) => value + 1);
                }
              }}
              title="取消本地 AI 等待并暂停自动采集，将输入交回终端；不会自动结束远端命令"
            >
              取消 AI，接管终端
            </button>
            <button
              className={'button small' + (state?.bastionCompatibility ? ' primary' : '')}
              type="button"
              disabled={!online || pending}
              aria-pressed={state?.bastionCompatibility ?? false}
              onClick={() =>
                void action({
                  kind: 'bastion-compatibility',
                  text: String(!state?.bastionCompatibility),
                })
              }
              title="仅本会话生效：逐字符原样发送已批准命令，不添加包装；无 Shell Integration 或多行命令需手动收集输出"
            >
              堡垒机兼容模式：{state?.bastionCompatibility ? '开' : '关'}
            </button>
            {state?.canCollectOutput && (
              <button
                className="button small"
                type="button"
                disabled={!online || pending}
                onClick={() => void action({ kind: 'execution-collect' })}
                title="收集已收到的输出交给 AI，不发送 Ctrl+C；远端命令可能仍在运行"
              >
                收集输出
              </button>
            )}
            <button className="button primary small" disabled={blocked || !prompt.trim()}>
              <Send size={14} />
              发送
            </button>
          </div>
        </form>
      </section>
      {split.divider}
      <div className="native-ssh">
        <Terminal
          focusRequest={terminalFocusRequest}
          session={session}
          active={active}
          onReconnect={onReconnect}
          canReconnect={canReconnect}
        />
      </div>
    </div>
  );
}

import { useEffect, useRef, useState } from 'react';
import { ArrowDownToLine } from 'lucide-react';
import { Terminal as XTerminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import '@xterm/xterm/css/xterm.css';
import { api, errorText, Session, sessionLive, sessionStatus } from './api';

export function Terminal({
  session,
  active,
  onReconnect,
  canReconnect,
  focusRequest = 0,
}: {
  session: Session;
  active: boolean;
  onReconnect: () => void;
  canReconnect: boolean;
  focusRequest?: number;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const fit = useRef<FitAddon | null>(null);
  const terminal = useRef<XTerminal | null>(null);
  const [error, setError] = useState('');
  const [scrolledBack, setScrolledBack] = useState(false);
  const [activated, setActivated] = useState(active);
  useEffect(() => {
    if (active) setActivated(true);
  }, [active]);
  const live = useRef(sessionLive(session));
  live.current = sessionLive(session);
  useEffect(() => {
    if (!activated) return;
    const term = new XTerminal({
      fontFamily: '"SFMono-Regular", Consolas, "Liberation Mono", monospace',
      fontSize: 13,
      lineHeight: 1.18,
      cursorBlink: true,
      scrollback: 20000,
      theme: {
        background: '#101923',
        foreground: '#dce4ed',
        cursor: '#65d6b1',
        selectionBackground: '#315c6b',
      },
    });
    const addon = new FitAddon();
    term.loadAddon(addon);
    term.open(ref.current!);
    terminal.current = term;
    fit.current = addon;
    let disposed = false;
    let ready = false;
    let inputQueue = Promise.resolve();
    const updateScrollState = () => {
      if (disposed) return;
      const buffer = term.buffer.active;
      setScrolledBack(buffer.type === 'normal' && buffer.viewportY < buffer.baseY);
    };
    // xterm follows output only at the bottom and preserves the visible text
    // while reading history, including when the scrollback buffer is trimmed.
    // Observe rendering too: dragging the native scrollbar can suppress onScroll.
    const scroll = term.onScroll(updateScrollState);
    const render = term.onRender(updateScrollState);
    const parsed = term.onWriteParsed(updateScrollState);
    updateScrollState();
    const offData = window.runtime?.EventsOn('terminal:' + session.id, (data: string) => {
      if (!disposed) term.write(Uint8Array.from(atob(data), (c) => c.charCodeAt(0)));
    });
    const offExit = window.runtime?.EventsOn('terminal-exit:' + session.id, (message: string) => {
      if (!disposed) {
        term.writeln('\r\n\x1b[90m' + message + '\x1b[0m');
        ready = false;
        term.options.disableStdin = true;
      }
    });
    const sendInput = (encoded: string) => {
      if (!ready || !live.current) return;
      inputQueue = inputQueue
        .then(() => {
          if (ready && live.current) return api.WriteTerminal(session.id, encoded);
        })
        .catch((e) => {
          if (!disposed) setError(errorText(e));
        });
    };
    const input = term.onData((data) => {
      const bytes = new TextEncoder().encode(data);
      let binary = '';
      for (const b of bytes) binary += String.fromCharCode(b);
      sendInput(btoa(binary));
    });
    // Legacy terminal mouse reports contain raw bytes, not UTF-8 text.
    const binaryInput = term.onBinary((data) => sendInput(btoa(data)));
    const resize = term.onResize(({ cols, rows }) => {
      updateScrollState();
      if (ready)
        void api.ResizeTerminal(session.id, Math.max(40, cols), Math.max(10, rows)).catch(() => {});
    });
    const observer = new ResizeObserver(() => {
      if (ref.current?.offsetWidth) addon.fit();
    });
    observer.observe(ref.current!);
    requestAnimationFrame(() => {
      if (disposed) return;
      addon.fit();
      if (!live.current) {
        term.options.disableStdin = true;
        return;
      }
      void api
        .StartSession(session.id, Math.max(40, term.cols), Math.max(10, term.rows))
        .then(() => {
          ready = true;
        })
        .catch((e) => {
          ready = false;
          if (!disposed) {
            setError(errorText(e));
            term.options.disableStdin = true;
          }
        });
    });
    return () => {
      disposed = true;
      observer.disconnect();
      input.dispose();
      binaryInput.dispose();
      resize.dispose();
      scroll.dispose();
      render.dispose();
      parsed.dispose();
      offData?.();
      offExit?.();
      term.dispose();
      terminal.current = null;
      fit.current = null;
    };
  }, [session.id, activated]);
  useEffect(() => {
    if (active && focusRequest) terminal.current?.focus();
  }, [active, focusRequest]);
  useEffect(() => {
    if (active)
      requestAnimationFrame(() => {
        fit.current?.fit();
        terminal.current?.focus();
      });
  }, [active]);
  return (
    <div className="terminal-pane" style={{ display: active ? 'flex' : 'none' }}>
      <div className="terminal-caption">
        <span className={'status-dot ' + session.status} />
        <span className="terminal-session-name" title={session.name}>
          {session.name} · {sessionStatus[session.status]}
        </span>
        <div className="terminal-scroll-controls">
          <span className="terminal-scroll-status">{scrolledBack ? '浏览历史' : '自动跟随'}</span>
          <button
            type="button"
            className="terminal-scroll-bottom"
            disabled={!scrolledBack}
            title="回到底部并跟随最新输出"
            onClick={() => {
              terminal.current?.scrollToBottom();
              terminal.current?.focus();
            }}
          >
            <ArrowDownToLine size={13} />
            回到底部
          </button>
        </div>
      </div>
      {!sessionLive(session) && (
        <div className="terminal-ended">
          <span>
            {session.status === 'failed'
              ? '会话异常结束，可查看终端信息后重试。'
              : '会话已结束，终端内容保留至关闭标签页。'}
          </span>
          <button className="button small" disabled={!canReconnect} onClick={onReconnect}>
            重新连接
          </button>
        </div>
      )}
      {error && <div className="terminal-error">{error}</div>}
      <div className="terminal-body" aria-label="SSH 终端">
        <div ref={ref} className="terminal-screen" />
      </div>
    </div>
  );
}

import { CommandContent } from './CommandContent';

export function CodexResume({ threadId }: { threadId?: string }) {
  if (!threadId) return null;
  // Thread IDs are UUIDs. Do not turn an arbitrary stored value into a shell command.
  if (!/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(threadId)) return null;
  return (
    <details className="chat-proposal">
      <summary>在本地 Codex 继续</summary>
      <p>退出后，在同一台电脑的终端运行以下命令。</p>
      <CommandContent command={'codex resume ' + threadId} />
    </details>
  );
}

import { useState } from 'react';
import { Search, X } from 'lucide-react';
import { allHosts, Catalog, Credential, Host, newHost } from './api';
import { ErrorBox, Field, Modal, Submit } from './components';
import { CredentialField, HostNameField } from './forms';
import { WorkspaceField } from './Workspace';

export type NewConversationInput =
  | { kind: 'existing'; hostId: string; workspace: string; aiProfileId: string }
  | { kind: 'direct'; host: Host; saveHost: boolean; aiProfileId: string };

export function NewConversation({
  data,
  initialHostId,
  busy,
  error,
  onClose,
  onCreate,
  createCredential,
}: {
  data: Catalog;
  initialHostId: string;
  busy: boolean;
  error: string;
  onClose: () => void;
  onCreate: (input: NewConversationInput) => void;
  createCredential: (done: (credential: Credential) => void) => void;
}) {
  const hosts = allHosts(data);
  const [mode, setMode] = useState<'existing' | 'direct'>(hosts.length ? 'existing' : 'direct');
  const [hostId, setHostId] = useState(initialHostId || hosts[0]?.id || '');
  const [hostSearch, setHostSearch] = useState('');
  const query = hostSearch.trim().toLowerCase();
  const matchingHosts = hosts.filter((h) =>
    [
      h.name,
      h.address,
      h.user,
      h.group,
      data.credentials.find((c) => c.id === h.credentialId)?.username || '',
    ]
      .join(' ')
      .toLowerCase()
      .includes(query),
  );
  const selectedHostId = matchingHosts.some((h) => h.id === hostId)
    ? hostId
    : matchingHosts[0]?.id || '';
  const [host, setHost] = useState<Host>(newHost);
  const [saveHost, setSaveHost] = useState(false);
  const [directories, setDirectories] = useState<Record<string, string>>({});
  const selectedHost = hosts.find((item) => item.id === selectedHostId);
  const workspace =
    mode === 'direct'
      ? host.workspace
      : (directories[selectedHostId] ?? selectedHost?.workspace ?? '');
  const [aiProfileId, setAIProfileId] = useState('');
  const inheritedProfileId =
    (mode === 'direct' ? host.aiProfileId : selectedHost?.aiProfileId) || data.defaultAIProfileId;
  const profileId = aiProfileId || inheritedProfileId;
  const provider = data.aiProfiles.find((profile) => profile.id === profileId)?.provider || 'codex';
  const workspaceSupported = ['codex', 'local', 'claude'].includes(provider);
  const field = (key: keyof Host, value: string | number) =>
    setHost((previous) => ({ ...previous, [key]: value }));
  return (
    <Modal
      className="new-conversation-modal connection-modal"
      title="新建 AI 对话"
      subtitle="选择已有连接，或直接填写服务器信息开始对话。"
      onClose={() => !busy && onClose()}
    >
      <form
        onSubmit={(event) => {
          event.preventDefault();
          if (mode === 'existing' && !selectedHostId) return;
          onCreate(
            mode === 'existing'
              ? {
                  kind: 'existing',
                  hostId: selectedHostId,
                  workspace: workspaceSupported ? workspace : '',
                  aiProfileId,
                }
              : {
                  kind: 'direct',
                  host: { ...host, workspace: workspaceSupported ? workspace : '' },
                  saveHost,
                  aiProfileId,
                },
          );
        }}
      >
        <div className="connection-fields">
          <fieldset disabled={busy}>
            <div className="filter-tabs new-conversation-mode" role="group" aria-label="连接来源">
              <button
                type="button"
                aria-pressed={mode === 'existing'}
                disabled={!hosts.length}
                onClick={() => setMode('existing')}
              >
                已有连接
              </button>
              <button
                type="button"
                aria-pressed={mode === 'direct'}
                onClick={() => setMode('direct')}
              >
                直接连接
              </button>
            </div>
            {mode === 'existing' ? (
              <>
                <div className="search existing-connection-search">
                  <Search size={15} />
                  <input
                    autoFocus
                    type="search"
                    aria-label="搜索已有连接"
                    placeholder="搜索主机名称、地址、用户名或分组…"
                    value={hostSearch}
                    onChange={(e) => setHostSearch(e.target.value)}
                  />
                  {hostSearch && (
                    <button
                      className="icon-button"
                      type="button"
                      aria-label="清除连接搜索"
                      onClick={() => setHostSearch('')}
                    >
                      <X size={13} />
                    </button>
                  )}
                </div>
                <Field label="服务器">
                  <select
                    aria-label="服务器"
                    required
                    value={selectedHostId}
                    onChange={(e) => setHostId(e.target.value)}
                  >
                    {!matchingHosts.length && <option value="">没有匹配的连接</option>}
                    {matchingHosts.map((h) => (
                      <option key={h.id} value={h.id}>
                        {h.name} · {h.address}
                        {data.hosts.some((saved) => saved.id === h.id) ? '' : '（未加入主机）'}
                      </option>
                    ))}
                  </select>
                </Field>
                {!matchingHosts.length && (
                  <p className="field-note" role="status">
                    试试其他关键词，或切换到“直接连接”。
                  </p>
                )}
              </>
            ) : (
              <>
                <div className="form-row address-row">
                  <Field label="主机地址">
                    <input
                      autoFocus
                      required
                      value={host.address}
                      onChange={(e) => field('address', e.target.value)}
                      placeholder="IP 地址或主机名"
                      autoComplete="off"
                    />
                  </Field>
                  <Field label="端口">
                    <input
                      type="number"
                      required
                      min={1}
                      max={65535}
                      value={host.port}
                      onChange={(e) => field('port', Number(e.target.value))}
                    />
                  </Field>
                </div>
                <Field label="SSH 用户名">
                  <input
                    value={host.user}
                    onChange={(e) => field('user', e.target.value)}
                    placeholder="例如 root；留空则使用凭据账号或本机用户名"
                    autoComplete="off"
                  />
                </Field>
                <CredentialField
                  data={data}
                  value={host.credentialId}
                  kinds={['password', 'key', 'agent']}
                  onChange={(id) => field('credentialId', id)}
                  onNew={() => createCredential((c) => field('credentialId', c.id))}
                />
                <p className="field-note">
                  不绑定凭据时使用默认 SSH 密钥，或在终端中输入登录密码。
                </p>
                <label className="checkbox-line">
                  <input
                    type="checkbox"
                    checked={saveHost}
                    onChange={(e) => setSaveHost(e.target.checked)}
                  />
                  同时加入主机列表
                </label>
                {saveHost ? (
                  <div className="form-row">
                    <HostNameField
                      host={host}
                      data={data}
                      onChange={(name) => field('name', name)}
                    />
                    <Field label="分组">
                      <input
                        value={host.group}
                        onChange={(e) => field('group', e.target.value)}
                        placeholder="可选"
                      />
                    </Field>
                  </div>
                ) : (
                  <p className="field-note">不加入主机列表；连接信息随对话保留，便于以后继续。</p>
                )}
                <details className="advanced">
                  <summary>跳板机</summary>
                  <Field label="跳板机">
                    <select
                      value={host.jumpHostId}
                      onChange={(e) => field('jumpHostId', e.target.value)}
                    >
                      <option value="">不使用跳板机</option>
                      {data.hosts.map((h) => (
                        <option key={h.id} value={h.id}>
                          {h.name}
                        </option>
                      ))}
                    </select>
                  </Field>
                </details>
              </>
            )}
            <Field
              label="AI 配置"
              hint="仅用于此对话；重连后继续使用所选配置。自定义模型请先在设置中添加。"
            >
              <select
                aria-label="AI 配置"
                value={aiProfileId}
                onChange={(e) => setAIProfileId(e.target.value)}
              >
                <option value="">
                  使用主机 / 默认配置（
                  {data.aiProfiles.find((p) => p.id === inheritedProfileId)?.name || '本机 Codex'}）
                </option>
                <option value="builtin-codex">本机 Codex（使用 CLI 默认配置）</option>
                {data.aiProfiles.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name} ·{' '}
                    {p.provider === 'codex'
                      ? 'Codex'
                      : p.provider === 'model'
                        ? '兼容 OpenAI API'
                        : p.provider === 'claude'
                          ? 'Claude Code'
                          : p.provider === 'deepseek'
                            ? 'DeepSeek'
                            : 'HTTP Agent'}
                  </option>
                ))}
              </select>
            </Field>
            <WorkspaceField
              value={workspace}
              disabled={!workspaceSupported}
              onChange={(value) =>
                mode === 'direct'
                  ? field('workspace', value)
                  : setDirectories((previous) => ({ ...previous, [selectedHostId]: value }))
              }
              hint={
                workspaceSupported
                  ? '此对话的本机项目目录，默认只读；留空关闭本机项目访问。'
                  : '当前 AI 配置不支持本机工作目录；Codex 和 Claude 支持此功能。'
              }
            />
            <ErrorBox error={error} />
          </fieldset>
        </div>
        <div className="form-actions">
          <button className="button" type="button" disabled={busy} onClick={onClose}>
            取消
          </button>
          <Submit
            busy={busy}
            disabled={mode === 'existing' && !selectedHostId}
            label={mode === 'direct' ? '连接并新建对话' : '创建对话'}
          />
        </div>
      </form>
    </Modal>
  );
}

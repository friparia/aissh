import { FormEvent, useEffect, useRef, useState } from 'react';
import { FolderOpen, Plus, KeyRound, ShieldCheck } from 'lucide-react';
import {
  AIProfile,
  api,
  Catalog,
  Credential,
  defaultHostName,
  errorText,
  Host,
  newCredential,
  Resource,
} from './api';
import { ErrorBox, Field, Modal, Submit } from './components';
import { CreationChat, CreationTabs } from './CreationChat';

type Base = { onClose: () => void; onSaved: () => void };
export function CredentialEditor({
  item,
  onClose,
  onSaved,
  secretOnly = false,
}: {
  item: Credential;
  onClose: () => void;
  onSaved: (c: Credential) => void;
  secretOnly?: boolean;
}) {
  const [value, set] = useState({ ...item });
  const [secret, setSecret] = useState('');
  const [clearSecret, setClear] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const field = (key: keyof Credential, v: string) => set((p) => ({ ...p, [key]: v }));
  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError('');
    try {
      const c = await api.SaveCredential({ credential: value, secret, clearSecret });
      setSecret('');
      onSaved(c);
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }
  const needsSecret = value.kind === 'password' || value.kind === 'token';
  if (secretOnly)
    return (
      <Modal
        title="更新连接凭据"
        subtitle={'正在进行的任务需要「' + item.name + '」。更新后会自动继续。'}
        onClose={() => !busy && onClose()}
      >
        <form onSubmit={save}>
          <fieldset disabled={busy}>
            <Field
              label={value.kind === 'key' ? '私钥口令' : value.kind === 'token' ? 'Token' : '密码'}
              hint="也可以先解锁系统凭据库，再点击重试。"
            >
              <input
                autoFocus
                type="password"
                required
                autoComplete="new-password"
                value={secret}
                onChange={(e) => setSecret(e.target.value)}
                maxLength={2048}
              />
            </Field>
            <ErrorBox error={error} />
            <div className="form-actions">
              <button type="button" className="button" onClick={onClose}>
                取消任务
              </button>
              <button type="button" className="button" onClick={() => onSaved(item)}>
                已解锁，重试
              </button>
              <Submit busy={busy} label="保存并继续" />
            </div>
          </fieldset>
        </form>
      </Modal>
    );
  return (
    <Modal
      title={item.id ? '编辑凭据' : '添加凭据'}
      subtitle="凭据保存在本机系统凭据库，AI 通过绑定的系统使用它。"
      onClose={() => {
        if (!busy) {
          setSecret('');
          onClose();
        }
      }}
    >
      <form onSubmit={save}>
        <fieldset disabled={busy}>
          <Field label="凭据名称">
            <input
              autoFocus
              required
              value={value.name}
              onChange={(e) => field('name', e.target.value)}
              placeholder="例如：生产环境运维账号"
              maxLength={100}
            />
          </Field>
          <Field label="凭据类型">
            <select
              value={value.kind}
              onChange={(e) => {
                field('kind', e.target.value);
                setSecret('');
                setClear(true);
              }}
            >
              <option value="password">账号密码</option>
              <option value="key">SSH 私钥</option>
              <option value="agent">SSH Agent</option>
              <option value="token">API Token</option>
            </select>
          </Field>
          {value.kind !== 'token' && (
            <Field label="用户名" hint="连接配置中的用户名优先；这里可以保存常用账号。">
              <input
                value={value.username}
                onChange={(e) => field('username', e.target.value)}
                placeholder="例如：ops"
                autoComplete="off"
              />
            </Field>
          )}
          {value.kind === 'key' && (
            <Field label="私钥文件" hint="引用本机已有文件，不复制私钥内容。">
              <div className="input-action">
                <input
                  required
                  value={value.identityFile}
                  onChange={(e) => field('identityFile', e.target.value)}
                  placeholder="~/.ssh/id_ed25519"
                />
                <button
                  type="button"
                  className="button"
                  onClick={() =>
                    void api
                      .PickKeyFile()
                      .then((path) => {
                        if (path) field('identityFile', path);
                      })
                      .catch((e) => setError(errorText(e)))
                  }
                >
                  <FolderOpen size={16} />
                  选择
                </button>
              </div>
            </Field>
          )}
          {value.kind !== 'agent' && (
            <Field
              label={
                value.kind === 'key'
                  ? '私钥口令（可选）'
                  : value.kind === 'token'
                    ? 'Token'
                    : '密码'
              }
              hint={
                item.hasSecret && !clearSecret
                  ? '已保存。留空保留当前值，填写新值即可更新。'
                  : '输入仅用于保存，不会出现在聊天或连接配置文件中。'
              }
            >
              <input
                type="password"
                autoComplete="new-password"
                value={secret}
                required={needsSecret && (!item.hasSecret || clearSecret)}
                onChange={(e) => setSecret(e.target.value)}
                placeholder={item.hasSecret && !clearSecret ? '已安全保存，留空不修改' : '请输入'}
                maxLength={2048}
              />
            </Field>
          )}
          {value.kind === 'key' && item.hasSecret && (
            <label className="check">
              <input
                type="checkbox"
                checked={clearSecret}
                onChange={(e) => setClear(e.target.checked)}
              />
              移除已保存的私钥口令
            </label>
          )}
          {value.kind === 'agent' && (
            <div className="info-box">
              <KeyRound size={18} />
              使用系统 SSH Agent 已加载的密钥，无需在这里填写秘密。
            </div>
          )}
          <ErrorBox error={error} />
          <div className="form-actions">
            <button type="button" className="button" onClick={onClose}>
              取消
            </button>
            <Submit busy={busy} />
          </div>
        </fieldset>
      </form>
    </Modal>
  );
}

type CredentialChooser = (done: (c: Credential) => void) => void;
export function CredentialField({
  data,
  value,
  onChange,
  onNew,
  kinds,
  label = '登录凭据',
}: {
  data: Catalog;
  value: string;
  onChange: (v: string) => void;
  onNew: () => void;
  kinds: string[];
  label?: string;
}) {
  return (
    <Field label={label}>
      <div className="input-action">
        <select value={value} onChange={(e) => onChange(e.target.value)}>
          <option value="">不绑定凭据</option>
          {data.credentials
            .filter((c) => kinds.includes(c.kind))
            .map((c) => (
              <option value={c.id} key={c.id}>
                {c.name}
              </option>
            ))}
        </select>
        <button className="button" type="button" onClick={onNew}>
          <Plus size={15} />
          新建
        </button>
      </div>
    </Field>
  );
}
export function HostNameField({
  host,
  data,
  onChange,
}: {
  host: Host;
  data: Catalog;
  onChange: (name: string) => void;
}) {
  return (
    <Field label="主机名称（可选）" hint="留空自动使用用户名、地址和端口。">
      <input
        value={host.name}
        onChange={(e) => onChange(e.target.value)}
        placeholder={defaultHostName(host, data) || '用户名@地址:端口'}
        maxLength={100}
      />
    </Field>
  );
}

export function HostEditor({
  item,
  data,
  onClose,
  onSaved,
  createCredential,
}: { item: Host; data: Catalog; createCredential: CredentialChooser } & Base) {
  const [value, set] = useState({ ...item });
  const addressRef = useRef<HTMLInputElement>(null);
  useEffect(() => addressRef.current?.focus(), []);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const field = (key: keyof Host, v: string | number) => set((p) => ({ ...p, [key]: v }));
  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError('');
    try {
      await api.SaveHost(value);
      onSaved();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal
      className="connection-modal"
      title={item.id ? '编辑主机' : '添加主机'}
      subtitle="填写连接信息即可保存，主机名称会自动生成。"
      onClose={() => !busy && onClose()}
    >
      <form onSubmit={save}>
        <div className="connection-fields">
          <fieldset disabled={busy}>
            <div className="form-row address-row">
              <Field label="主机地址">
                <input
                  ref={addressRef}
                  required
                  value={value.address}
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
                  value={value.port}
                  onChange={(e) => field('port', Number(e.target.value))}
                />
              </Field>
            </div>
            <Field label="SSH 用户名">
              <input
                value={value.user}
                onChange={(e) => field('user', e.target.value)}
                placeholder="留空则使用凭据账号或本机用户名"
                autoComplete="off"
              />
            </Field>
            <CredentialField
              data={data}
              value={value.credentialId}
              kinds={['password', 'key', 'agent']}
              onChange={(v) => field('credentialId', v)}
              onNew={() => createCredential((c) => field('credentialId', c.id))}
            />
            <HostNameField host={value} data={data} onChange={(name) => field('name', name)} />
            <details className="advanced">
              <summary>更多设置 · 分组、跳板机和 AI</summary>
              <Field label="分组（可选）">
                <input
                  value={value.group}
                  onChange={(e) => field('group', e.target.value)}
                  placeholder="例如：生产环境"
                  list="host-groups"
                />
                <datalist id="host-groups">
                  {[...new Set(data.hosts.map((h) => h.group).filter(Boolean))].map((g) => (
                    <option key={g}>{g}</option>
                  ))}
                </datalist>
              </Field>
              <Field label="跳板机">
                <select
                  value={value.jumpHostId}
                  onChange={(e) => field('jumpHostId', e.target.value)}
                >
                  <option value="">直接连接</option>
                  {data.hosts
                    .filter((h) => h.id !== item.id)
                    .map((h) => (
                      <option value={h.id} key={h.id}>
                        {h.name}
                      </option>
                    ))}
                </select>
              </Field>
              <Field label="AI 配置">
                <select
                  value={value.aiProfileId}
                  onChange={(e) => field('aiProfileId', e.target.value)}
                >
                  <option value="">跟随默认配置</option>
                  {data.aiProfiles.map((p) => (
                    <option value={p.id} key={p.id}>
                      {p.name}
                    </option>
                  ))}
                </select>
              </Field>
              <Field
                label="本机工作区"
                hint="AI 可以读取的本机项目目录；桌面连接不填则关闭本机文件访问。"
              >
                <div className="input-action">
                  <input
                    value={value.workspace}
                    onChange={(e) => field('workspace', e.target.value)}
                    placeholder="本机项目路径"
                  />
                  <button
                    type="button"
                    className="button"
                    onClick={() =>
                      void api
                        .PickWorkspace()
                        .then((v) => {
                          if (v) field('workspace', v);
                        })
                        .catch((e) => setError(errorText(e)))
                    }
                  >
                    <FolderOpen size={16} />
                  </button>
                </div>
              </Field>
            </details>
            <ErrorBox error={error} />
          </fieldset>
        </div>
        <div className="form-actions">
          <button type="button" className="button" disabled={busy} onClick={onClose}>
            取消
          </button>
          <Submit busy={busy} label="保存主机" />
        </div>
      </form>
    </Modal>
  );
}

export function ResourceEditor({
  item,
  data,
  onClose,
  onSaved,
  createCredential,
}: { item: Resource; data: Catalog; createCredential: CredentialChooser } & Base) {
  const [value, set] = useState({ ...item });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [creationMode, setCreationMode] = useState<'form' | 'chat'>('form');
  const [created, setCreated] = useState(false);
  const finish = () => {
    if (!busy) (created ? onSaved : onClose)();
  };
  const field = (key: keyof Resource, v: string | boolean) => set((p) => ({ ...p, [key]: v }));
  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError('');
    try {
      await api.SaveResource(value);
      onSaved();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal
      title={item.id ? '编辑外联系统' : '添加外联系统'}
      subtitle="管理系统用途、服务地址、关联主机与访问凭据。"
      onClose={finish}
      className={creationMode === 'chat' ? 'creation-modal' : ''}
    >
      {!item.id && (
        <>
          <CreationTabs mode={creationMode} busy={busy} onChange={setCreationMode} />
          <CreationChat
            mode="system"
            data={data}
            active={creationMode === 'chat'}
            onBusy={setBusy}
            onCreated={() => setCreated(true)}
            onFinish={finish}
          />
        </>
      )}
      <form onSubmit={save} hidden={creationMode === 'chat'}>
        <fieldset disabled={busy}>
          <div className="form-row">
            <Field label="系统名称">
              <input
                autoFocus
                required
                value={value.name}
                onChange={(e) => field('name', e.target.value)}
                placeholder="例如：订单数据库"
              />
            </Field>
            <Field label="系统类型">
              <select value={value.kind} onChange={(e) => field('kind', e.target.value)}>
                <option value="http">HTTP API</option>
                <option value="postgres">PostgreSQL</option>
                <option value="mysql">MySQL</option>
                <option value="redis">Redis</option>
              </select>
            </Field>
          </div>
          <div className="form-row">
            <Field label="所属系统">
              <select
                value={value.systemId || ''}
                onChange={(e) =>
                  set((p) => ({
                    ...p,
                    systemId: e.target.value,
                    systemName:
                      data.resources.find((r) => r.systemId === e.target.value)?.systemName ||
                      p.name,
                  }))
                }
              >
                <option value="">新建独立系统</option>
                {Array.from(
                  new Map(
                    data.resources.filter((r) => r.systemId).map((r) => [r.systemId, r]),
                  ).values(),
                ).map((r) => (
                  <option key={r.systemId} value={r.systemId}>
                    {r.systemName || r.name}
                  </option>
                ))}
              </select>
            </Field>
            <Field label="连接环境">
              <select
                value={value.environment || 'production'}
                onChange={(e) => field('environment', e.target.value)}
              >
                <option value="production">生产</option>
                <option value="test">测试</option>
                <option value="development">开发</option>
              </select>
            </Field>
          </div>
          <Field label="系统说明（可选）">
            <textarea
              value={value.description || ''}
              onChange={(e) => field('description', e.target.value)}
              placeholder="系统用途、负责团队、环境与注意事项"
              maxLength={2000}
              rows={3}
            />
          </Field>
          <Field
            label={value.kind === 'http' ? '服务地址' : '连接地址'}
            hint={
              value.kind === 'http'
                ? '填写 HTTPS 地址；本机调试服务可使用 HTTP。'
                : '填写 host:port；IPv6 地址使用 [地址]:端口。'
            }
          >
            <input
              required
              value={value.endpoint}
              onChange={(e) => field('endpoint', e.target.value)}
              placeholder={value.kind === 'http' ? 'https://api.example.com' : 'db.internal:5432'}
            />
          </Field>
          {value.kind !== 'http' && (
            <div className="form-row">
              <Field label={value.kind === 'redis' ? '数据库编号' : '数据库名'}>
                <input
                  value={value.database}
                  onChange={(e) => field('database', e.target.value)}
                  placeholder={value.kind === 'redis' ? '0' : 'orders'}
                />
              </Field>
              <label className="check centered">
                <input
                  type="checkbox"
                  checked={value.tls}
                  onChange={(e) => field('tls', e.target.checked)}
                />
                启用 TLS 并验证证书
              </label>
            </div>
          )}
          {value.kind === 'http' && (
            <Field
              label="接口文档路径（可选）"
              hint="填写服务地址内的 OpenAPI / Swagger JSON 相对路径；留空自动查找常用路径。"
            >
              <input
                value={value.discoveryPath || ''}
                onChange={(e) => field('discoveryPath', e.target.value)}
                placeholder="/openapi.json"
              />
            </Field>
          )}
          <CredentialField
            label="系统凭据"
            data={data}
            value={value.credentialId}
            kinds={value.kind === 'http' ? ['password', 'token'] : ['password']}
            onChange={(v) => field('credentialId', v)}
            onNew={() => createCredential((c) => field('credentialId', c.id))}
          />
          <div className="form-row">
            <Field label="关联 SSH 主机">
              <select value={value.hostId} onChange={(e) => field('hostId', e.target.value)}>
                <option value="">仅供本机使用</option>
                {data.hosts.map((h) => (
                  <option value={h.id} key={h.id}>
                    {h.name}
                  </option>
                ))}
              </select>
            </Field>
            <Field label="访问路径">
              <select value={value.route} onChange={(e) => field('route', e.target.value)}>
                <option value="local">本机直接访问</option>
                <option value="ssh" disabled={!value.hostId}>
                  通过关联主机的 SSH 隧道
                </option>
              </select>
            </Field>
          </div>
          <Field
            label="初始只读授权"
            hint="仅用于初始化内置只读接口。保存后的授权在操作目录中管理。"
          >
            <select
              disabled={value.managed}
              value={value.grant}
              onChange={(e) => field('grant', e.target.value)}
            >
              <option value="ask">每次操作确认</option>
              <option value="session">只读接口逐次确认（兼容旧配置）</option>
              <option value="read">记住此范围，自动执行只读操作</option>
            </select>
          </Field>
          <label className="check">
            <input
              type="checkbox"
              checked={value.allowLocal}
              onChange={(e) => field('allowLocal', e.target.checked)}
            />
            允许本机查看此系统目录（新增时建立内置只读授权）
          </label>
          <div className="info-box">
            <ShieldCheck size={18} />
            <span>
              保存后会在后台读取接口文档或元数据。候选接口需审查后单独授权；调用权限和风险规则在操作目录管理。
            </span>
          </div>
          <ErrorBox error={error} />
          <div className="form-actions">
            <button type="button" className="button" onClick={finish}>
              取消
            </button>
            <Submit busy={busy} label="保存并只读探索" />
          </div>
        </fieldset>
      </form>
    </Modal>
  );
}

export function AIEditor({
  item,
  data,
  onClose,
  onSaved,
  createCredential,
}: { item: AIProfile; data: Catalog; createCredential: CredentialChooser } & Base) {
  const [value, set] = useState({ ...item });
  const [modelsText, setModelsText] = useState((item.models || []).join('\n'));
  const [isDefault, setDefault] = useState(
    data.defaultAIProfileId === item.id || data.aiProfiles.length === 0,
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const local = value.provider === 'codex' || value.provider === 'claude';
  const field = (key: keyof AIProfile, v: string) => set((p) => ({ ...p, [key]: v }));
  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError('');
    try {
      await api.SaveAIProfile(
        {
          ...value,
          models: value.provider === 'model'
            ? [...new Set(modelsText.split(/[\n,，]/).map((model) => model.trim()).filter(Boolean))]
            : [],
          credentialId: local ? '' : value.credentialId,
          url: local ? '' : value.url,
        },
        isDefault,
      );
      onSaved();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal
      title={item.id ? '编辑 AI 配置' : '添加 AI 配置'}
      subtitle="保存后可设为默认，也可以为每台主机单独选择。"
      onClose={() => !busy && onClose()}
    >
      <form onSubmit={save}>
        <fieldset disabled={busy}>
          <Field label="配置名称">
            <input
              autoFocus
              required
              value={value.name}
              onChange={(e) => field('name', e.target.value)}
              placeholder="例如：日常运维助手"
            />
          </Field>
          <Field label="AI 后端">
            <select aria-label="AI 后端" value={value.provider} onChange={(e) => field('provider', e.target.value)}>
              <option value="codex">本机 Codex</option>
              <option value="claude">本机 Claude Code</option>
              <option value="model">兼容 OpenAI API 的模型</option>
              <option value="deepseek">DeepSeek</option>
              <option value="http">HTTP Agent</option>
            </select>
          </Field>
          {local ? (
            <>
              <div className="info-box">复用本机 CLI 的登录状态。请先安装并完成该 CLI 的登录。</div>
              <Field label="CLI 程序路径（可选）" hint="桌面启动时找不到 CLI，可填写其绝对路径。">
                <input
                  value={value.cliPath || ''}
                  onChange={(e) => field('cliPath', e.target.value)}
                  placeholder={
                    value.provider === 'codex'
                      ? '/opt/homebrew/bin/codex'
                      : '/opt/homebrew/bin/claude'
                  }
                />
              </Field>
            </>
          ) : (
            <>
              <Field
                label={value.provider === 'http' ? 'Agent 流式接口地址' : '模型服务地址'}
                hint="支持 HTTP 或 HTTPS 地址，可包含 /v1 等路径。"
              >
                <input
                  required
                  value={value.url}
                  onChange={(e) => field('url', e.target.value)}
                  placeholder={
                    value.provider === 'deepseek'
                      ? 'https://api.deepseek.com'
                      : 'https://api.example.com/v1'
                  }
                />
              </Field>
              <CredentialField
                label="API 凭据"
                data={data}
                value={value.credentialId}
                kinds={['token']}
                onChange={(v) => field('credentialId', v)}
                onNew={() => createCredential((c) => field('credentialId', c.id))}
              />
            </>
          )}
          {value.provider === 'codex' && (
            <label className="check">
              <input type="checkbox" checked={!!value.keepCodexSession}
                onChange={(e) => set((p) => ({ ...p, keepCodexSession: e.target.checked }))} />
              退出后保留 Codex 会话，可在本地终端继续（默认删除；下次连接生效）
            </label>
          )}
          <Field
            label={value.provider === 'model' ? '默认模型名称' : '模型名称'}
            hint={local ? '留空使用 CLI 的默认模型。' : undefined}
          >
            <input
              required={!local && value.provider !== 'http'}
              value={value.model}
              onChange={(e) => field('model', e.target.value)}
              placeholder={local ? '跟随 CLI 默认配置' : '请输入模型名称'}
            />
          </Field>
          {value.provider === 'model' && (
            <Field label="可选模型" hint="每行一个模型名称，也可用逗号分隔。默认模型会自动加入，可在会话中切换。">
              <textarea
                aria-label="可选模型"
                rows={4}
                value={modelsText}
                onChange={(event) => setModelsText(event.target.value)}
                placeholder={'model-a\nmodel-b\nmodel-c'}
              />
            </Field>
          )}
          <label className="check">
            <input
              type="checkbox"
              checked={isDefault}
              onChange={(e) => setDefault(e.target.checked)}
            />
            设为默认 AI 配置
          </label>
          <ErrorBox error={error} />
          <div className="form-actions">
            <button type="button" className="button" onClick={onClose}>
              取消
            </button>
            <Submit busy={busy} />
          </div>
        </fieldset>
      </form>
    </Modal>
  );
}

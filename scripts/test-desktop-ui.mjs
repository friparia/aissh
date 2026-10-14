// UI integration test using an in-memory Wails bridge. No real credentials or
// hosts are touched. Run after frontend/build; set PLAYWRIGHT_MODULE if needed.
import assert from 'node:assert/strict';
import http from 'node:http';
import { readFile, mkdir } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import path from 'node:path';
import { testCommandReview } from './test-command-review.mjs';
import { testWorkspace } from './test-workspace.mjs';
import { testTerminalScroll } from './test-terminal-scroll.mjs';
import { testWidgets } from './test-widgets.mjs';
import { testMonitorResize } from './test-monitor-resize.mjs';
import { testModalEscape } from './test-modal-escape.mjs';
import { testStreamingChat } from './test-streaming-chat.mjs';
import { testMarkdownChat } from './test-markdown-chat.mjs';
import { testModels } from './test-models.mjs';
import { testTerminalTakeover } from './test-terminal-takeover.mjs';
import { testIMEComposer, testWorkbenchIME } from './test-ime-composer.mjs';
import { installMonitoringMock, testMonitoring, testPet, testPetSessionReads } from './test-monitoring.mjs';
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const assets = path.resolve(root, process.env.UI_DIST || 'frontend/dist');
const browsers = await import(process.env.PLAYWRIGHT_MODULE || 'playwright');
const engine = process.env.UI_BROWSER || 'chromium';
const server = http.createServer(async (req, res) => {
  try {
    const requested = decodeURIComponent(new URL(req.url, 'http://local').pathname);
    const file = path.join(assets, requested === '/' ? 'index.html' : requested);
    if (!file.startsWith(assets + path.sep)) throw new Error('path');
    const body = await readFile(file);
    res.setHeader(
      'Content-Type',
      file.endsWith('.wasm') ? 'application/wasm' : file.endsWith('.js') ? 'text/javascript' : file.endsWith('.css') ? 'text/css' : 'text/html',
    );
    res.end(body);
  } catch {
    res.statusCode = 404;
    res.end();
  }
});
await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
const browser = await browsers[engine].launch({
  headless: true,
  ignoreDefaultArgs: process.env.TERMINAL_ONLY ? ['--hide-scrollbars'] : undefined,
  timeout: 20000,
  executablePath:
    process.env.UI_BROWSER_PATH || (engine === 'chromium' ? process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' : undefined),
});
const context = await browser.newContext({
  viewport: { width: 1280, height: 840 },
  deviceScaleFactor: 1,
});
const page = await context.newPage();
const errors = [];
page.on('pageerror', (e) => errors.push(e.message));
await page.addInitScript((aiSelectionOnly) => {
  const data = {
    version: 1,
    revision: 1,
    hosts: [
      {
        id: 'host-one',
        name: '生产应用服务器',
        address: '192.0.2.10',
        port: 22,
        user: 'ops',
        group: '生产环境',
        credentialId: 'cred-one',
        jumpHostId: '',
        aiProfileId: '',
        workspace: '',
      },
      {
        id: 'host-two',
        name: '开发环境',
        address: '192.0.2.20',
        port: 2222,
        user: 'developer',
        group: '开发环境',
        credentialId: 'cred-one',
        jumpHostId: 'host-one',
        aiProfileId: '',
        workspace: '',
      },
    ],
    credentials: [
      {
        id: 'cred-one',
        name: '运维 SSH 密钥',
        kind: 'key',
        username: 'ops',
        identityFile: '~/.ssh/id_ed25519',
        hasSecret: true,
        updatedAt: '',
      },
    ],
    resources: [],
    aiProfiles: [],
    defaultAIProfileId: '',
    conversationHosts: JSON.parse(sessionStorage.getItem('test-conversation-hosts') || '[]'),
    recentConnections: JSON.parse(sessionStorage.getItem('recent-test-connections') || '[]'),
  };
  if (aiSelectionOnly) {
    data.aiProfiles = [
      { id: 'model-default', name: '主机模型', provider: 'model' },
      { id: 'model-custom', name: '自定义服务', provider: 'model' },
    ];
    data.defaultAIProfileId = 'model-default';
    data.hosts[0].aiProfileId = 'model-default';
  }
  data.hosts.push(...JSON.parse(sessionStorage.getItem('test-direct-saved-hosts') || '[]'));
  const listeners = {};
  let sequence = 1;
  const emit = (name, value) => (listeners[name] || []).forEach((fn) => fn(value));
  window.runtime = {
    EventsOn: (name, fn) => {
      (listeners[name] ??= []).push(fn);
      return () => {
        listeners[name] = listeners[name].filter((f) => f !== fn);
      };
    },
  };
  const save = (collection, item) => {
    const value = { ...item, id: item.id || 'test-' + sequence++ };
    const i = collection.findIndex((c) => c.id === value.id);
    if (i < 0) collection.push(value);
    else collection[i] = value;
    return value;
  };
  const sessions = [];
  const conversations = JSON.parse(sessionStorage.getItem('test-ai-conversations') || 'null') || [
    { id: 'shared-id', hostId: 'host-one', title: '排查 nginx 超时', preview: '发现上游响应变慢', messageCount: 2, startedAt: '2026-09-25T08:00:00Z', updatedAt: '2026-09-25T08:10:00Z' },
    { id: 'disk', hostId: 'host-one', title: '检查磁盘空间', preview: '日志占用了较多空间', messageCount: 2, startedAt: '2026-09-24T08:00:00Z', updatedAt: '2026-09-24T08:10:00Z' },
    { id: 'shared-id', hostId: 'host-two', title: '开发服务启动失败', preview: '开发环境缺少配置', messageCount: 2, startedAt: '2026-09-23T08:00:00Z', updatedAt: '2026-09-23T08:10:00Z' },
  ];
  const persistConversations = () => sessionStorage.setItem('test-ai-conversations', JSON.stringify(conversations));
  const updateSession = (id, status) => {
    const session = sessions.find(s => s.id === id);
    if (!session) return;
    session.status = status;
    if (status === 'ended' || status === 'failed') session.endedAt = new Date().toISOString();
    emit('session-updated', { ...session });
    if (session.endedAt) emit('terminal-exit:' + id, '会话已结束');
  };
  window.__test = { data, emit, sessions, conversations, updateSession, writes: [], starts: [], trusted: false, resolutions: [], failStart: false };

  const workbenches = {};
  const publishWorkbench = (id) => {
    const state = workbenches[id];
    if (!state) return;
    state.updatedAt = new Date().toISOString();
    emit('workbench:' + id, structuredClone(state));
  };
  Object.assign(window.__test, { workbenches, publishWorkbench, actions: [] });
  setInterval(() => sessions.filter(s => s.status === 'running').forEach(s => publishWorkbench(s.id)), 1000);

  const normalizeHost = (input) => {
    const host = { ...input, address: input.address.trim(), user: input.user.trim(), port: input.port || 22 };
    const user = host.user || data.credentials.find(c => c.id === host.credentialId)?.username.trim() || '';
    const address = host.address.includes(':') ? `[${host.address}]` : host.address;
    host.name = input.name.trim() || `${user ? user + '@' : ''}${address}:${host.port}`;
    return host;
  };

  window.go = {
    desktop: {
      Service: {
        Snapshot: async () => JSON.parse(JSON.stringify(data)),
        SaveCredential: async ({ credential, secret }) =>
          save(data.credentials, { ...credential, hasSecret: !!secret || credential.hasSecret }),
        SaveHost: async (host) => {
          window.__test.lastHostInput = { ...host };
          return save(data.hosts, normalizeHost(host));
        },
        SaveResource: async (resource) => save(data.resources, resource),
        SaveAIProfile: async (profile, defaultProfile) => {
          const p = save(data.aiProfiles, profile);
          if (defaultProfile) data.defaultAIProfileId = p.id;
          return p;
        },
        Delete: async (kind, id) => {
          if (kind === 'credential' && data.hosts.some((h) => h.credentialId === id))
            throw new Error('凭据仍被主机使用');
          const k = {
            host: 'hosts',
            credential: 'credentials',
            resource: 'resources',
            ai: 'aiProfiles',
          }[kind];
          data[k] = data[k].filter((v) => v.id !== id);
        },
        PickKeyFile: async () => '/Users/example/.ssh/id_ed25519',
        PickWorkspace: async () => '/Users/example/project',
        CleanupSecrets: async () => {},
        CheckHost: async (id) => ({
          hostId: id,
          name: '生产应用服务器',
          address: '192.0.2.10',
          known: window.__test.trusted,
          token: 'trust-token',
          keys: [{ algorithm: 'ssh-ed25519', fingerprint: 'SHA256:example-test-fingerprint' }],
        }),
        TrustHost: async () => {
          window.__test.trusted = true;
        },
        TestHost: async () => {},
        ListSessions: async () => {
          const snapshot = structuredClone(sessions);
          if (window.__test.holdList) {
            window.__test.holdList = false;
            return new Promise(resolve => { window.__test.releaseList = () => resolve(snapshot); });
          }
          return snapshot;
        },
        ClearRecentConnections: async () => {
          data.recentConnections = [];
          sessionStorage.removeItem('recent-test-connections');
        },
        ListConversations: async () => structuredClone(conversations),
        GetConversation: async (hostId, id) => {
          const c = conversations.find(c => c.hostId === hostId && c.id === id);
          if (!c) throw new Error('会话不存在');
          return c.messageCount ? [{ role: 'user', content: c.title, time: c.startedAt }, { role: 'assistant', content: c.preview, time: c.updatedAt }] : [];
        },
        CreateConversation: async (hostId) => {
          if (window.__test.failConversation) throw new Error('测试：无法写入历史');
          const c = { id: 'conversation-' + sequence++, hostId, title: '新对话', preview: '', messageCount: 0, startedAt: new Date().toISOString(), updatedAt: new Date().toISOString() };
          conversations.unshift(c);
          persistConversations();
          return { ...c };
        },
        CreateConversationWithWorkspace: async (hostId, workspace) => {
          const c = await window.go.desktop.Service.CreateConversation(hostId);
          conversations.find(item => item.id === c.id).workspace = workspace;
          persistConversations();
          return { ...c, workspace };
        },
        CreateConversationWithAI: async (hostId, workspace, aiProfileId) => {
          const c = await window.go.desktop.Service.CreateConversationWithWorkspace(hostId, workspace);
          conversations.find(item => item.id === c.id).aiProfileId = aiProfileId;
          persistConversations();
          return { ...c, aiProfileId };
        },
        CreateDirectConversation: async (input, saveHost) => {
          const host = { ...normalizeHost(input), id: 'direct-' + Date.now() + '-' + sequence++ };
          if (saveHost) {
            data.hosts.push(host);
            sessionStorage.setItem('test-direct-saved-hosts', JSON.stringify(data.hosts.filter(h => h.id.startsWith('direct-'))));
          } else {
            data.conversationHosts.push(host);
            sessionStorage.setItem('test-conversation-hosts', JSON.stringify(data.conversationHosts));
          }
          return { host, conversation: await window.go.desktop.Service.CreateConversationWithWorkspace(host.id, host.workspace) };
        },
        CreateDirectConversationWithAI: async (input, saveHost, aiProfileId) => {
          const result = await window.go.desktop.Service.CreateDirectConversation(input, saveHost);
          conversations.find(item => item.id === result.conversation.id).aiProfileId = aiProfileId;
          persistConversations();
          return { ...result, conversation: { ...result.conversation, aiProfileId } };
        },
        OpenConversationSession: async (hostId, id) => {
          const existing = sessions.find(s => s.hostId === hostId && s.conversationId === id && (s.status === 'starting' || s.status === 'running'));
          if (existing) return { ...existing };
          const created = await window.go.desktop.Service.CreateSession(hostId);
          const s = sessions.find(s => s.id === created.id);
          s.conversationId = id;
          return { ...s };
        },
        ReconnectSession: async (id) => {
          const s = sessions.find(s => s.id === id);
          return s.conversationId ? window.go.desktop.Service.OpenConversationSession(s.hostId, s.conversationId) : window.go.desktop.Service.CreateSession(s.hostId);
        },
        CreateSession: async (hostId) => {
          const host = [...data.hosts, ...data.conversationHosts].find(h => h.id === hostId);
          const session = { id: 'session-' + sequence++, hostId, conversationId: '', name: host.name,
            address: host.address, port: host.port, user: host.user, createdAt: new Date().toISOString(), endedAt: '', status: 'starting' };
          sessions.push(session);
          data.recentConnections = [{ hostId, openedAt: session.createdAt }, ...data.recentConnections.filter(r => r.hostId !== hostId)].slice(0, 50);
          sessionStorage.setItem('recent-test-connections', JSON.stringify(data.recentConnections));
          return { ...session };
        },
        StartSession: async (id) => {
          window.__test.starts.push(id);
          if (window.__test.failStart) {
            window.__test.failStart = false;
            updateSession(id, 'failed');
            throw new Error('测试：无法启动终端');
          }
          updateSession(id, 'running');
          const session = sessions.find(s => s.id === id);
          const conversation = conversations.find(c => c.id === session.conversationId && c.hostId === session.hostId);
          const host = [...data.hosts, ...data.conversationHosts].find(h => h.id === session.hostId);
          const profile = data.aiProfiles.find(p => p.id === (conversation?.aiProfileId || host?.aiProfileId || data.defaultAIProfileId));
          const modelOptions = profile?.provider === 'model' ? [...new Set([profile.model, ...(profile.models || [])].filter(Boolean))] : [];
          workbenches[id] = {
            model: conversation?.aiModel || profile?.model || '', modelOptions,
            workspace: conversations.find(c => c.id === session.conversationId && c.hostId === session.hostId)?.workspace ?? [...data.hosts, ...data.conversationHosts].find(h => h.id === session.hostId)?.workspace ?? '',
            workspaceSupported: true, workspaceWritable: false,
            conversationId: session.conversationId, busy: false, status: '', canApprove: false, executing: false, approvalStatus: '',
            messages: [
              { role: 'user', content: '检查 nginx 最近的响应情况', time: new Date().toISOString() },
              { role: 'assistant', content: '服务器负载平稳，内存使用率为 25%。可以先查看 nginx 的错误日志，确认上游响应是否有异常。', time: new Date().toISOString() },
            ],
            panels: [{ id: 'system', title: '系统', kind: 'monitor', content: 'HOST=prod-app-01\nLOAD=0.20 0.15 0.10\nMEM=512/2048 MB (25.0%)\nDISK=8G/40G (20%)\nPROCS=87', status: '实时', error: '', intervalMs: 2000, updatedAt: new Date().toISOString(), pinned: true }],
            updatedAt: new Date().toISOString(),
          };
          publishWorkbench(id);
          setTimeout(() => emit('terminal:' + id, btoa('Connected to test host\r\nops@server:~$ ')), 20);
        },
        GetWorkbench: async (id) => structuredClone(workbenches[id] || null),
        WorkbenchAction: async (id, command) => {
          window.__test.actions.push({ id, ...command });
          const state = workbenches[id];
          if (command.kind === 'terminal-takeover') {
            state.busy = false;
            state.executing = false;
            state.proposal = undefined;
            state.terminalTakeover = true;
            publishWorkbench(id);
          } else if (command.kind === 'model') {
            if (state.busy || state.executing || state.proposal || !state.modelOptions.includes(command.text)) throw new Error('model unavailable');
            state.model = command.text;
            const conversation = conversations.find(c => c.id === state.conversationId && c.hostId === sessions.find(s => s.id === id)?.hostId);
            if (conversation) conversation.aiModel = command.text;
            persistConversations();
            publishWorkbench(id);
          } else if (command.kind === 'workspace') {
            if (command.text === '/missing') throw new Error('无法访问本机工作目录');
            state.workspace = command.text;
            const conversation = conversations.find(c => c.id === state.conversationId && c.hostId === sessions.find(s => s.id === id)?.hostId);
            if (conversation) conversation.workspace = command.text;
            persistConversations();
            publishWorkbench(id);
          } else if (command.kind === 'prompt') {
            state.messages.push({ role: 'user', content: command.text, time: new Date().toISOString() });
            state.busy = true; state.status = '正在分析'; publishWorkbench(id);
            setTimeout(() => {
              state.messages.push({ role: 'assistant_stream', content: '正在读取监控数据…', time: new Date().toISOString() }); publishWorkbench(id);
              setTimeout(() => { state.busy = false; state.messages.at(-1).role = 'assistant'; state.messages.at(-1).content = '检查完成，服务运行正常。'; publishWorkbench(id); }, 200);
            }, 100);
          } else if (command.kind === 'approve' || command.kind === 'reject') {
            if (state.proposal?.id !== command.proposalId) throw new Error('stale proposal');
            state.proposal = undefined; state.canApprove = false; publishWorkbench(id);
          } else if (command.kind === 'monitor-open') {
            state.panels.push({ id: 'desktop-' + command.panelId, title: '进程', kind: 'table', content: 'PID USER COMMAND %CPU %MEM\n123 ops nginx 2.0 1.0', status: '实时', error: '', intervalMs: 3000, updatedAt: new Date().toISOString(), pinned: false }); publishWorkbench(id);
          } else if (command.kind === 'monitor-close') {
            state.panels = state.panels.filter(p => p.id !== command.panelId); publishWorkbench(id);
          }
        },
        WriteTerminal: async (id, text) => {
          window.__test.writes.push(atob(text));
          emit('terminal:' + id, text);
        },
        ResizeTerminal: async () => {},
        CloseSession: async (id) => {
          updateSession(id, 'ended');
          const index = sessions.findIndex(s => s.id === id);
          if (index >= 0) sessions.splice(index, 1);
        },
        ImportSSH: async () => [],
        TestResource: async () => '{"status":"ok"}',
        RunResource: async () => '{"rows":[[1]]}',
        ResolveCredentialRequest: async (id, accepted) => {
          window.__test.resolutions.push({ id, accepted });
          emit('credential-request-ended', id);
        },
      },
    },
  };
}, !!process.env.AI_SELECTION_ONLY);
const out = path.join(root, '.cache/desktop-ui');
await page.addInitScript(installMonitoringMock, !!process.env.PET_ONLY);
await mkdir(out, { recursive: true });
const openSettings = async (name = '主机') => {
  if (!await page.getByRole('navigation', { name: '设置分类' }).isVisible()) await page.getByRole('button', { name: '设置', exact: true }).click();
  await page.getByRole('navigation', { name: '设置分类' }).getByRole('button', { name, exact: true }).click();
};
try {
  await page.goto(`http://127.0.0.1:${server.address().port}`);
  if (process.env.TAKEOVER_ONLY) {
    await testTerminalTakeover(page);
    assert.deepEqual(errors, []);
  } else if (process.env.MODELS_ONLY) {
    await testModels(page, openSettings);
    assert.deepEqual(errors, []);
  } else if (process.env.WORKSPACE_ONLY) {
    await testWorkspace(page, out);
    assert.deepEqual(errors, []);
  } else if (process.env.MODAL_ESCAPE_ONLY) {
    await testModalEscape(page);
    assert.deepEqual(errors, []);
  } else if (process.env.WIDGETS_ONLY) {
    await testWidgets(page, out);
    assert.deepEqual(errors, []);
  } else if (process.env.MONITOR_RESIZE_ONLY) {
    await testMonitorResize(page, out);
    assert.deepEqual(errors, []);
  } else if (process.env.MONITORING_ONLY) {
    await testMonitoring(page, out);
    assert.deepEqual(errors, []);
  } else if (process.env.PET_READ_ONLY) {
    await testPetSessionReads(page);
    assert.deepEqual(errors, []);
  } else if (process.env.PET_ONLY) {
    await testPet(page, out);
    assert.deepEqual(errors, []);
  } else if (process.env.IME_ONLY) {
    await page.locator('.session-select').first().waitFor();
    await page.evaluate(() => { window.__test.trusted = true; });
    await page.getByRole('button', { name: '连接并继续', exact: true }).click();
    await page.locator('.native-workbench:visible .native-chat-messages').getByText('服务器负载平稳', { exact: false }).waitFor();
    await testWorkbenchIME(page);
    assert.deepEqual(errors, []);
    console.log('PASS: IME confirmation preserves drafts; Enter, Shift+Enter and Send work');
  } else if (process.env.MARKDOWN_ONLY) {
    await page.locator('.session-select').first().waitFor();
    await testMarkdownChat(page, out);
    assert.deepEqual(errors, []);
  } else if (process.env.STREAMING_ONLY) {
    await page.locator('.session-select').first().waitFor();
    await page.evaluate(() => { window.__test.trusted = true; });
    await page.getByRole('button', { name: '连接并继续', exact: true }).click();
    await page.locator('.native-workbench:visible .native-chat-messages').getByText('服务器负载平稳', { exact: false }).waitFor();
    await testStreamingChat(page, page.locator('.native-workbench:visible'), out);
    assert.deepEqual(errors, []);
  } else if (process.env.COMMAND_REVIEW_ONLY) {
    await page.locator('.session-select').first().waitFor();
    await testCommandReview(page, out);
    assert.deepEqual(errors, []);
  } else if (process.env.TERMINAL_ONLY) {
    await page.locator('.session-select').first().waitFor();
    await testTerminalScroll(page, out, engine);
    assert.deepEqual(errors, []);
  } else if (process.env.AI_SELECTION_ONLY) {
    await page.evaluate(() => { window.__test.trusted = true; });
    for (const mode of ['existing', 'direct']) {
      for (const profileId of ['builtin-codex', 'model-custom']) {
        await page.getByRole('button', { name: '新建会话', exact: true }).first().click();
        const dialog = page.getByRole('dialog');
        if (mode === 'direct') {
          await dialog.getByRole('button', { name: '直接连接', exact: true }).click();
          await dialog.getByLabel('主机地址', { exact: true }).fill('192.0.2.88');
        }
        await dialog.getByLabel('AI 配置', { exact: true }).selectOption(profileId);
        const workspace = dialog.getByLabel('AI 工作目录（本机）', { exact: true });
        assert.equal(await workspace.isDisabled(), profileId === 'model-custom');
        if (profileId === 'builtin-codex') await workspace.fill('/test/project');
        await dialog.getByRole('button', {
          name: mode === 'direct' ? '连接并新建对话' : '创建对话',
          exact: true,
        }).click();
        await dialog.waitFor({ state: 'hidden' });
        const created = await page.evaluate(() => window.__test.conversations[0]);
        assert.equal(created.aiProfileId, profileId);
        assert.equal(created.workspace, profileId === 'builtin-codex' ? '/test/project' : '');
        assert.equal(created.hostId === 'host-one', mode === 'existing');
        assert.equal(await page.evaluate(() => window.__test.data.hosts[0].aiProfileId), 'model-default');
      }
    }
    await page.reload();
    await page.getByRole('button', { name: '新建会话', exact: true }).first().waitFor();
    const retained = await page.evaluate(() => window.__test.conversations.filter(c => c.aiProfileId));
    assert.equal(retained.length, 4);
    assert.deepEqual(errors, []);
    console.log('PASS: Codex and compatible OpenAI model selection in both connection modes, workspace behavior, host defaults and persisted choices');
  } else {
  await page.locator('.session-select').first().waitFor();
  assert.equal(await page.locator('.session-select').count(), 3);
  assert.equal(await page.locator('.nav-item, .terminal-tabs, .workbench-list, .conversation-list').count(), 0);
  assert.equal(await page.getByRole('button', { name: 'SSH 会话', exact: true }).count(), 0);
  await page.getByLabel('会话历史').getByText('发现上游响应变慢', { exact: true }).waitFor();
  assert.equal(await page.evaluate(() => window.__test.starts.length), 0);
  await page.getByLabel('搜索会话', { exact: true }).fill('磁盘');
  assert.equal(await page.locator('.session-select').count(), 1);
  await page.getByRole('button', { name: '清除会话搜索', exact: true }).click();
  await page.getByRole('button', { name: '收起 生产应用服务器 的会话', exact: true }).click();
  assert.equal(await page.locator('.session-select:visible').count(), 1);
  await page.getByRole('button', { name: '展开 生产应用服务器 的会话', exact: true }).click();
  await page.getByLabel('SSH 与 AI 会话列表').getByRole('button', { name: /开发服务启动失败/ }).click();
  await page.getByLabel('会话历史').getByText('开发环境缺少配置', { exact: true }).waitFor();
  await openSettings();
  assert.equal(await page.getByRole('navigation', { name: '设置分类' }).getByRole('button').count(), 4);
  assert.equal(await page.getByLabel('SSH 与 AI 会话列表').count(), 1);
  await page.screenshot({ path: path.join(out, 'settings-hosts.png') });
  await page.getByRole('button', { name: '返回工作区', exact: false }).click();
  await page.getByLabel('会话历史').getByText('开发环境缺少配置', { exact: true }).waitFor();
  await page.getByLabel('SSH 与 AI 会话列表').getByRole('button', { name: /排查 nginx 超时/ }).click();
  await page.screenshot({ path: path.join(out, 'unified-session-history.png') });
  await page.getByRole('button', { name: '收起会话列表', exact: true }).click();
  assert.equal(await page.getByLabel('SSH 与 AI 会话列表').isVisible(), false);
  await page.getByRole('button', { name: '展开会话列表', exact: true }).click();
  if (process.env.WORKBENCH_ONLY) {
    await page.evaluate(() => { window.__test.trusted = true; });
    await page.getByRole('button', { name: '连接并继续', exact: true }).click();
    await page.locator('.native-workbench:visible .native-chat-messages').getByText('服务器负载平稳', { exact: false }).waitFor();
    const workbench = page.locator('.native-workbench:visible');
    assert.equal(await workbench.locator('.monitor-metric').count(), 4);
    const monitorBox = await workbench.locator('.native-monitor').boundingBox();
    const expandedChat = await workbench.locator('.native-chat').boundingBox();
    const expandedSSH = await workbench.locator('.native-ssh').boundingBox();
    assert.ok(Math.abs(monitorBox.x - expandedChat.x) < 1);
    assert.ok(Math.abs(monitorBox.x + monitorBox.width - expandedSSH.x - expandedSSH.width) < 1);
    assert.ok(monitorBox.y + monitorBox.height <= Math.min(expandedChat.y, expandedSSH.y) + 1, 'monitor must sit above both workspace panes');
    assert.equal(await page.locator('.session-select').count(), 3, 'running conversation duplicated in sidebar');
    assert.equal((await workbench.textContent()).includes('/sidebar'), false);
    assert.equal(await workbench.getByRole('button', { name: '发送', exact: true }).evaluate(el => el.getBoundingClientRect().bottom <= el.closest('main').getBoundingClientRect().bottom), true, 'Composer action clipped');
    await page.screenshot({ path: path.join(out, 'unified-workbench.png') });
    await workbench.getByLabel('向 AI 提问').fill('尚未发送的输入');
    const startsBeforeSettings = await page.evaluate(() => window.__test.starts.length);
    await workbench.getByRole('button', { name: /实时面板/ }).click();
    assert.equal(await workbench.getByRole('button', { name: /实时面板/ }).getAttribute('aria-expanded'), 'false');
    assert.equal(await workbench.locator('.monitor-details').count(), 0);
    assert.match(await workbench.locator('.monitor-summary').textContent(), /负载 0.20.*内存 512/);
    assert.ok((await workbench.locator('.native-chat').boundingBox()).height > expandedChat.height + 80);
    assert.ok((await workbench.locator('.native-ssh').boundingBox()).height > expandedSSH.height + 80);
    assert.equal(await page.evaluate(() => localStorage.getItem('aissh.monitor.expanded')), 'false');
    await page.screenshot({ path: path.join(out, 'unified-workbench-monitor-collapsed.png') });
    await openSettings('凭据');
    await page.getByRole('button', { name: '返回工作区', exact: false }).click();
    assert.equal(await workbench.getByLabel('向 AI 提问').inputValue(), '尚未发送的输入');
    assert.equal(await page.evaluate(() => window.__test.starts.length), startsBeforeSettings);
    assert.equal(await workbench.getByRole('button', { name: /实时面板/ }).getAttribute('aria-expanded'), 'false');
    await workbench.getByRole('button', { name: /实时面板/ }).click();
    await page.getByRole('button', { name: '收起会话列表', exact: true }).click();
    const beforeWrites = await page.evaluate(() => window.__test.writes.length);
    await testIMEComposer(workbench.getByLabel('向 AI 提问'), () => page.evaluate(() => window.__test.actions.filter(action => action.kind === 'prompt').length));
    await workbench.getByLabel('向 AI 提问').fill('请检查服务\n保留详细输出');
    await workbench.getByLabel('向 AI 提问').press('Enter');
    await workbench.getByText('检查完成，服务运行正常。', { exact: true }).waitFor();
    assert.equal(await page.evaluate(() => window.__test.writes.length), beforeWrites, 'AI prompt entered SSH PTY');
    assert.equal(await page.evaluate(() => window.__test.actions.at(-1).text), '请检查服务\n保留详细输出');
    await page.evaluate(() => {
      const id = window.__test.sessions[0].id;
      Object.assign(window.__test.workbenches[id], { proposal: { id: 'p1', command: 'systemctl status nginx', explanation: '检查服务运行状态', expectedEffect: '读取当前服务状态', risk: 'low' }, canApprove: true });
      window.__test.publishWorkbench(id);
    });
    await workbench.getByRole('button', { name: '批准执行', exact: true }).click();
    await workbench.getByLabel('待审批命令').waitFor({ state: 'detached' });
    await testStreamingChat(page, workbench, out);
    assert.equal(await page.evaluate(() => window.__test.actions.at(-1).proposalId), 'p1');
    await workbench.getByLabel('添加监控').selectOption('processes');
    await workbench.locator('.monitor-output').getByText('123 ops nginx', { exact: false }).waitFor();
    await workbench.getByRole('button', { name: '关闭监控 进程', exact: true }).click();
    await workbench.locator('.monitor-metric').first().waitFor();
    await page.evaluate(() => {
      const id = window.__test.sessions[0].id;
      window.__test.workbenches[id].panels[0].error = '连接超时';
      window.__test.publishWorkbench(id);
    });
    await workbench.getByText('采集失败：连接超时', { exact: false }).waitFor();
    assert.equal(await workbench.getByText('512/2048 MB (25.0%)', { exact: true }).count(), 1);
    await workbench.getByRole('button', { name: /实时面板/ }).click();
    assert.equal(await workbench.locator('.monitor-metric').count(), 0);
    await workbench.getByRole('button', { name: /实时面板/ }).click();
    await page.evaluate(() => {
      const id = window.__test.sessions[0].id;
      window.__test.workbenches[id].panels[0].error = '';
      window.__test.workbenches[id].panels[0].updatedAt = new Date(Date.now() - 60000).toISOString();
      window.__test.publishWorkbench(id);
    });
    await workbench.getByText('数据已停止更新', { exact: false }).waitFor();
    await page.setViewportSize({ width: 940, height: 650 });
    await page.emulateMedia({ colorScheme: 'dark' });
    await page.waitForTimeout(300);
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
    const chatBox = await workbench.locator('.native-chat').boundingBox();
    const sshBox = await workbench.locator('.native-ssh').boundingBox();
    assert.ok(chatBox.width > 250 && sshBox.width > 250);
    await page.screenshot({ path: path.join(out, 'native-workbench-compact-dark.png') });
    await page.setViewportSize({ width: 1280, height: 840 });
    await page.emulateMedia({ colorScheme: 'light' });
    await page.getByRole('button', { name: '展开会话列表', exact: true }).click();
    await page.getByLabel('SSH 与 AI 会话列表').getByRole('button', { name: /检查磁盘空间/ }).click();
    await page.getByRole('button', { name: '连接并继续', exact: true }).click();
    await page.waitForFunction(() => window.__test.sessions.length === 2);
    await page.getByRole('button', { name: '收起会话列表', exact: true }).click();
    assert.equal(await page.evaluate(() => window.__test.sessions[0].status), 'running');
    await page.evaluate(() => window.__test.updateSession(window.__test.sessions[1].id, 'ended'));
    await page.locator('.native-workbench:visible').getByText('连接已结束', { exact: true }).waitFor();
    assert.equal(await page.locator('.native-workbench:visible').getByRole('button', { name: '发送', exact: true }).isDisabled(), true);
    assert.deepEqual(errors, []);

    await page.getByRole('button', { name: '展开会话列表', exact: true }).click();
    await page.getByRole('button', { name: /排查 nginx 超时/ }).first().click();
    await page.getByRole('button', { name: '断开连接', exact: true }).click();
    await page.getByRole('button', { name: '保持连接', exact: true }).click();
    await page.getByRole('button', { name: '断开连接', exact: true }).click();
    await page.getByRole('dialog').getByRole('button', { name: '断开连接', exact: true }).click();
    await page.waitForFunction(() => window.__test.sessions.length === 1);
    assert.equal(await page.getByLabel('SSH 与 AI 会话列表').getByRole('button', { name: /排查 nginx 超时/ }).count(), 1, 'closing connection deleted persistent conversation');
    await page.evaluate(() => window.__test.emit('desktop-command', 'settings'));
    await page.getByRole('heading', { name: '设置', exact: true }).waitFor();
    await page.evaluate(() => window.__test.emit('desktop-command', 'workspace'));
    await page.getByRole('heading', { name: '设置', exact: true }).waitFor({ state: 'hidden' });
    assert.deepEqual(errors, []);
    console.log('PASS: unified session sidebar, settings navigation, state preservation, native AI/SSH/monitor workspace, collapse, search, history, streaming, approvals, monitoring and disconnect');
  } else if (process.env.DIALOG_ONLY) {
    for (const viewport of [{ width: 1180, height: 760 }, { width: 940, height: 650 }]) {
      await page.setViewportSize(viewport);
      await page.getByRole('button', { name: '新建会话', exact: true }).first().click();
      const dialog = page.getByRole('dialog');
      await dialog.waitFor();
      await page.screenshot({ path: path.join(out, `new-dialog-${engine}-${viewport.width}-existing.png`) });
      await dialog.getByRole('button', { name: '直接连接', exact: true }).click();
      await dialog.getByLabel('主机地址', { exact: true }).fill('192.0.2.88');
      await dialog.getByLabel('同时加入主机列表').check();
      await dialog.getByLabel('主机名称').fill('布局验证');
      await page.screenshot({ path: path.join(out, `new-dialog-${engine}-${viewport.width}-direct.png`) });
      assert.equal(await dialog.getByRole('button', { name: '连接并新建对话', exact: true }).evaluate(el => { const b = el.getBoundingClientRect(); return b.top >= 0 && b.bottom <= innerHeight; }), true);
      await dialog.getByRole('button', { name: '取消', exact: true }).click();
    }
    assert.deepEqual(errors, []);
    console.log('PASS: new session dialog and direct connection layout');
  } else {
  await openSettings('主机');
  await page.locator('.identity strong').filter({ hasText: '生产应用服务器' }).waitFor();
  await page.screenshot({ path: path.join(out, 'hosts.png') });
  await page.getByRole('button', { name: '通过跳板', exact: true }).click();
  await page.locator('tbody tr').filter({ hasText: '开发环境' }).waitFor();
  assert.equal(await page.locator('tbody tr').count(), 1);
  await page.getByRole('textbox', { name: '搜索配置', exact: true }).fill('192.0.2.10');
  await page.getByText('没有找到匹配的主机', { exact: true }).waitFor();
  await page.getByRole('button', { name: '清除搜索', exact: true }).click();
  await page.getByRole('button', { name: '直接连接', exact: true }).click();
  await page.locator('tbody tr').filter({ hasText: '生产应用服务器' }).waitFor();
  assert.equal(await page.locator('tbody tr').count(), 1);
  await page.getByRole('button', { name: '全部', exact: true }).click();
  await page.getByRole('button', { name: '添加主机', exact: true }).click();
  await page.getByLabel(/^主机名称/).fill('新建测试主机');
  await page.getByLabel('主机地址', { exact: true }).fill('192.0.2.30');
  await page.getByRole('button', { name: '新建', exact: true }).click();
  await page.getByLabel('凭据名称', { exact: true }).fill('测试密码');
  await page.getByLabel(/^用户名/).fill('tester');
  const password = page.locator('dialog').last().locator('input[type=password]');
  await password.fill('ui-test-only-secret');
  assert.equal(await password.getAttribute('type'), 'password');
  await page.screenshot({ path: path.join(out, 'credential-form.png') });
  await page.getByRole('button', { name: '保存', exact: true }).click();
  await page.getByRole('button', { name: '保存主机', exact: true }).click();
  await page.locator('.identity strong').getByText('新建测试主机', { exact: true }).waitFor();
  assert.equal(await page.locator('input[type=password]').count(), 0);
  assert.equal(await page.evaluate(() => Object.values(localStorage).some(value => value.includes('ui-test-only-secret'))), false);
  // The optional name previews the current connection and can be saved empty.
  await page.setViewportSize({ width: 940, height: 650 });
  await page.getByRole('button', { name: '添加主机', exact: true }).click();
  const hostDialog = page.getByRole('dialog');
  const hostName = hostDialog.getByLabel(/^主机名称/);
  assert.equal(await hostDialog.getByLabel('主机地址', { exact: true }).evaluate(el => el === document.activeElement), true);
  assert.equal(await hostName.evaluate(el => el.required), false);
  await hostDialog.getByLabel('主机地址', { exact: true }).fill('192.0.2.31');
  assert.equal(await hostName.getAttribute('placeholder'), '192.0.2.31:22');
  await hostDialog.getByLabel(/^登录凭据/).selectOption({ label: '测试密码' });
  assert.equal(await hostName.getAttribute('placeholder'), 'tester@192.0.2.31:22');
  await hostDialog.getByLabel('SSH 用户名', { exact: true }).fill('deploy');
  await hostDialog.getByLabel('端口', { exact: true }).fill('2222');
  assert.equal(await hostName.getAttribute('placeholder'), 'deploy@192.0.2.31:2222');
  await hostDialog.getByLabel('主机地址', { exact: true }).fill('2001:db8::31');
  assert.equal(await hostName.getAttribute('placeholder'), 'deploy@[2001:db8::31]:2222');
  await hostDialog.getByLabel('主机地址', { exact: true }).fill('192.0.2.31');
  await page.screenshot({ path: path.join(out, 'host-form-optional-name.png') });
  await hostDialog.locator('summary').click();
  await hostDialog.getByLabel(/^分组（可选）/).fill('测试分组');
  assert.equal(await hostDialog.getByRole('button', { name: '保存主机', exact: true }).evaluate(el => {
    const button = el.getBoundingClientRect();
    const modal = el.closest('dialog').getBoundingClientRect();
    return button.top >= modal.top && button.bottom <= modal.bottom && button.bottom <= innerHeight;
  }), true, 'Host save action must remain visible with advanced settings expanded');
  await page.screenshot({ path: path.join(out, 'host-form-advanced.png') });
  await hostDialog.getByRole('button', { name: '保存主机', exact: true }).click();
  await page.getByText('deploy@192.0.2.31:2222', { exact: true }).waitFor();
  assert.equal(await page.evaluate(() => window.__test.lastHostInput.name), '');
  await page.setViewportSize({ width: 1280, height: 840 });
  await page.getByRole('textbox', { name: '搜索配置', exact: true }).fill('192.0.2.30');
  assert.equal(await page.locator('tbody tr').count(), 1);
  await page.getByRole('textbox', { name: '搜索配置', exact: true }).fill('');
  await openSettings('凭据');
  await page.getByRole('button', { name: '删除 运维 SSH 密钥', exact: true }).click();
  await page.getByRole('button', { name: '确认删除', exact: true }).click();
  await page.getByRole('alert').getByText('凭据仍被主机使用').waitFor();
  await page.getByRole('button', { name: '取消', exact: true }).click();
  await page.screenshot({ path: path.join(out, 'credentials.png') });
  await openSettings('系统连接');
  await page.getByRole('button', { name: '添加系统', exact: true }).click();
  await page.getByLabel('系统名称', { exact: true }).fill('内部状态接口');
  await page.getByLabel('服务地址').fill('https://api.example.test');
  await page.getByRole('button', { name: '保存系统连接', exact: true }).click();
  await page.getByText('内部状态接口', { exact: true }).waitFor();
  await page.screenshot({ path: path.join(out, 'resources.png') });
  await page.getByRole('button', { name: '执行', exact: true }).click();
  await page.getByRole('button', { name: '批准并执行', exact: true }).click();
  await page.locator('.result-output').waitFor();
  await page.getByRole('button', { name: '完成', exact: true }).click();
  await openSettings('AI 配置');
  await page.getByRole('button', { name: '添加 AI 配置', exact: true }).first().click();
  await page.getByLabel('配置名称', { exact: true }).fill('本机助手');
  await page.getByRole('button', { name: '保存', exact: true }).click();
  await page.getByText('本机助手', { exact: true }).waitFor();
  await openSettings('主机');
  await page
    .locator('tbody tr')
    .filter({ has: page.locator('.identity strong').filter({ hasText: '生产应用服务器' }) })
    .getByRole('button', { name: '连接', exact: true })
    .click();
  await page.getByText('核对 SSH 主机指纹', { exact: true }).waitFor();
  await page.screenshot({ path: path.join(out, 'host-trust.png') });
  await page.getByRole('button', { name: '已核对，信任并继续', exact: true }).click();
  await page.locator('.terminal-screen').waitFor();
  await page.waitForTimeout(300);
  await page.locator('.xterm-helper-textarea').focus();
  await page.keyboard.type('ls');
  await page.keyboard.press('Enter');
  await page.waitForTimeout(100);
  assert.ok((await page.evaluate(() => window.__test.writes.join(''))).includes('ls'));
  await page.screenshot({ path: path.join(out, 'terminal.png') });
  await page.evaluate(() =>
    window.__test.emit('credential-needed', {
      requestId: 'request-1',
      credentialId: 'cred-one',
      sessionId: 'session',
    }),
  );
  await page.getByText('更新连接凭据', { exact: true }).waitFor();
  await page.locator('dialog input[type=password]').fill('updated-ui-secret');
  await page.getByRole('button', { name: '保存并继续', exact: true }).click();
  await page.waitForTimeout(100);
  assert.deepEqual(await page.evaluate(() => window.__test.resolutions), [
    { id: 'request-1', accepted: true },
  ]);

    const session = await page.evaluate(() => window.__test.sessions[0].id);
    await openSettings('凭据');
    assert.equal(await page.getByRole('button', { name: '设置', exact: true }).getAttribute('aria-pressed'), 'true');
    await page.screenshot({ path: path.join(out, 'settings-credentials.png') });
    await page.getByRole('button', { name: '返回工作区', exact: false }).click();
    assert.equal(await page.evaluate(() => window.__test.starts.filter(id => id === window.__test.sessions[0].id).length), 1);
    await page.evaluate((id) => window.__test.updateSession(id, 'failed'), session);
    await page.getByRole('button', { name: '重新连接', exact: true }).click();
    await page.waitForFunction((old) => !window.__test.sessions.some(s => s.id === old) && window.__test.sessions.some(s => s.status === 'running'), session);
    await page.getByRole('button', { name: '新建会话', exact: true }).first().click();
    await page.getByRole('dialog').getByRole('button', { name: '直接连接', exact: true }).click();
    await page.getByLabel('主机地址', { exact: true }).fill('192.0.2.88');
    await page.getByRole('button', { name: '连接并新建对话', exact: true }).click();
    await page.waitForFunction(() => window.__test.sessions.some(s => s.address === '192.0.2.88' && s.status === 'running'));
    assert.equal(await page.getByLabel('SSH 与 AI 会话列表').locator('.session-select').filter({ hasText: '新对话' }).count(), 1);
    await page.setViewportSize({ width: 940, height: 650 });
    await page.emulateMedia({ colorScheme: 'dark' });
    await page.screenshot({ path: path.join(out, 'unified-workbench-small-dark.png') });
    await openSettings('主机');
    assert.equal(await page.locator('tbody tr').filter({ hasText: '192.0.2.88' }).count(), 0);
    await page.screenshot({ path: path.join(out, 'settings-small-dark.png') });
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
    await page.getByRole('button', { name: '收起会话列表', exact: true }).click();
    await page.reload();
    await page.getByRole('button', { name: '展开会话列表', exact: true }).waitFor();
    assert.equal(await page.getByLabel('SSH 与 AI 会话列表').isVisible(), false);
    assert.deepEqual(errors, []);
    console.log('PASS: settings host/credential/resource/AI workflows, trust, terminal input, credential repair, reconnect, direct sessions, light/dark layout and persisted sidebar state');
  }
  }
} catch (error) {
  await page.screenshot({ path: path.join(out, `failure-${engine}.png`) });
  throw error;
} finally {
  await browser.close();
  await new Promise((resolve) => server.close(resolve));
}

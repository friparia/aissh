import { useEffect, useRef, useState } from 'react';
import {
  ArrowUpRight,
  BookOpen,
  Bell,
  CheckCircle2,
  ChevronRight,
  Database,
  Download,
  KeyRound,
  LoaderCircle,
  PanelLeft,
  Plus,
  RotateCcw,
  Search,
  Server,
  ShieldCheck,
  Sparkles,
  TerminalSquare,
  Trash2,
  X,
  PlugZap,
  Pencil,
  Play,
  Globe,
  Settings,
  Activity,
} from 'lucide-react';
import {
  AIProfile,
  allHosts,
  api,
  Catalog,
  Conversation,
  connected,
  Credential,
  emptyCatalog,
  errorText,
  Host,
  HostCheck,
  newAI,
  newCredential,
  newHost,
  newResource,
  Resource,
  Session,
  sessionLive,
  sessionStatus,
} from './api';
import { AIEditor, CredentialEditor, HostEditor, ResourceEditor } from './forms';
import { Empty, ErrorBox, Modal } from './components';
import { Workbench } from './Workbench';
import { SessionSidebar } from './SessionSidebar';
import { conversationSession, sessionItemKey } from './session-items';
import { useStoredFlag, useStoredStringSet } from './layout';
import { Conversations, conversationKey } from './Conversations';
import { NewConversation, NewConversationInput } from './NewConversation';
import { primaryModifier } from './platform';
import { KnowledgeLibrary } from './Knowledge';
import { ResourceGuide, discoveryStatus } from './ResourceGuide';
import { Monitoring, MonitoringView, useMonitoringState } from './Monitoring';

type SettingsPage = 'hosts' | 'credentials' | 'ai' | 'removedHosts';
type Page = SettingsPage | 'resources' | 'knowledge' | 'conversations' | 'terminal' | 'monitoring';
type Editor =
  | { kind: 'host'; value: Host }
  | { kind: 'resource'; value: Resource }
  | { kind: 'ai'; value: AIProfile };
const kinds: Record<string, string> = {
  password: '账号密码',
  key: 'SSH 私钥',
  agent: 'SSH Agent',
  token: 'API Token',
  http: 'HTTP API',
  postgres: 'PostgreSQL',
  mysql: 'MySQL',
  redis: 'Redis',
};
const settingsPages = [
  { id: 'hosts' as SettingsPage, label: '主机', icon: Server },
  { id: 'credentials' as SettingsPage, label: '凭据', icon: KeyRound },
  { id: 'ai' as SettingsPage, label: 'AI 配置', icon: Sparkles },
  { id: 'removedHosts' as SettingsPage, label: '已移除服务器', icon: RotateCcw },
];

export function App() {
  const [data, setData] = useState<Catalog>(emptyCatalog);
  const connectionHosts = allHosts(data);
  const [page, setPage] = useState<Page>('conversations');
  const [monitorInventoryRequest, setMonitorInventoryRequest] = useState({
    sequence: 0,
    sessionId: '',
    view: 'monitors' as MonitoringView,
  });
  const monitoring = useMonitoringState();
  const alertCount = monitoring.state?.incidents.filter((i) => i.status === 'firing').length || 0;
  const [sidebarVisible, setSidebarVisible] = useStoredFlag('aissh.sidebar.visible', true);
  const [removedSidebarHosts, setRemovedSidebarHosts] = useStoredStringSet(
    'aissh.sidebar.removedHosts',
  );
  const [hiddenSidebarItems, setHiddenSidebarItems] = useStoredStringSet(
    'aissh.sidebar.hiddenSessions',
  );
  const restoreSidebarHost = (id: string) => {
    setRemovedSidebarHosts((previous) => {
      if (!previous.has(id)) return previous;
      const next = new Set(previous);
      next.delete(id);
      return next;
    });
  };
  const [lastSettings, setLastSettings] = useState<SettingsPage>('hosts');
  const isSettings = settingsPages.some((item) => item.id === page);
  const isManagement =
    isSettings || page === 'resources' || page === 'knowledge' || page === 'monitoring';
  const [knowledgeRevision, setKnowledgeRevision] = useState(0);
  const [knowledgeIntent, setKnowledgeIntent] = useState<'new' | Conversation | null>(null);
  const workspacePage = useRef<'conversations' | 'terminal'>('conversations');
  useEffect(() => {
    if (page === 'conversations' || page === 'terminal') workspacePage.current = page;
  }, [page]);
  const [conversationSearch, setConversationSearch] = useState('');
  const conversationSearchRef = useRef<HTMLInputElement>(null);
  const [selectedRow, setSelectedRow] = useState('');
  const searchRef = useRef<HTMLInputElement>(null);
  const [search, setSearch] = useState('');
  const [group, setGroup] = useState('全部主机');
  const [routeFilter, setRouteFilter] = useState<'all' | 'direct' | 'jump'>('all');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [toast, setToast] = useState('');
  const [busy, setBusy] = useState('');
  const [editor, setEditor] = useState<Editor | null>(null);
  const [credentialEditor, setCredentialEditor] = useState<{
    value: Credential;
    done?: (c: Credential) => void;
  } | null>(null);
  const [deletion, setDeletion] = useState<{ kind: string; id: string; name: string } | null>(null);
  const [deleteError, setDeleteError] = useState('');
  const [trust, setTrust] = useState<{ check: HostCheck; resolve: (ok: boolean) => void } | null>(
    null,
  );
  const [trustError, setTrustError] = useState('');
  const [imports, setImports] = useState<Host[] | null>(null);
  const [importSelection, setImportSelection] = useState<Set<number>>(new Set());
  const [result, setResult] = useState<{ name: string; content: string } | null>(null);
  const [resourceGuide, setResourceGuide] = useState('');
  const [sessions, setSessions] = useState<Session[]>([]);
  // Keep session hosts available for listing and restoration after their saved host is deleted.
  const sidebarHosts = [
    ...connectionHosts,
    ...sessions
      .filter(
        (s, index) =>
          !connectionHosts.some((h) => h.id === s.hostId) &&
          !sessions.slice(0, index).some((other) => other.hostId === s.hostId),
      )
      .map((s) => ({
        ...newHost(),
        id: s.hostId,
        name: s.name,
        address: s.address,
        port: s.port,
        user: s.user,
      })),
  ];
  const [conversations, setConversations] = useState<Conversation[]>([]);
  const [conversationHost, setConversationHost] = useState('');
  const [collapsedConversationHosts, setCollapsedConversationHosts] = useState<Set<string>>(
    new Set(),
  );
  const restoreSidebarItem = (key: string, hostId: string, sessionId?: string) => {
    restoreSidebarHost(hostId);
    setHiddenSidebarItems((previous) => {
      const next = new Set(previous);
      next.delete(key);
      if (sessionId) next.delete('session:' + sessionId);
      for (const session of sessions) {
        if (sessionItemKey(session) === key) next.delete('session:' + session.id);
      }
      return next.size === previous.size ? previous : next;
    });
    setCollapsedConversationHosts((previous) => {
      if (!previous.has(hostId)) return previous;
      const next = new Set(previous);
      next.delete(hostId);
      return next;
    });
  };
  useEffect(() => {
    // A plain SSH session can gain a saved conversation after its first AI message.
    // Carry its hidden state over to the persistent conversation key.
    setHiddenSidebarItems((previous) => {
      const next = new Set(previous);
      let changed = false;
      for (const session of sessions) {
        if (session.conversationId && previous.has('session:' + session.id)) {
          next.add(sessionItemKey(session));
          next.delete('session:' + session.id);
          changed = true;
        }
      }
      return changed ? next : previous;
    });
  }, [sessions, setHiddenSidebarItems]);
  const [selectedConversation, setSelectedConversation] = useState('');
  const [newConversationHost, setNewConversationHost] = useState<string | null>(null);
  const [conversationError, setConversationError] = useState('');
  const [newConversationError, setNewConversationError] = useState('');
  const conversationRequest = useRef(0);
  async function refreshConversations() {
    const request = ++conversationRequest.current;
    try {
      const items = await api.ListConversations();
      if (request === conversationRequest.current) {
        setConversations(items);
        setConversationError('');
      }
    } catch (e) {
      if (request === conversationRequest.current) setConversationError(errorText(e));
    }
  }
  useEffect(() => {
    void refreshConversations();
    const timer = setInterval(() => {
      if (!document.hidden) void refreshConversations();
    }, 5000);
    return () => clearInterval(timer);
  }, []);
  useEffect(() => {
    if (conversationHost && !allHosts(data).some((h) => h.id === conversationHost))
      setConversationHost('');
  }, [data.hosts, data.conversationHosts, conversationHost]);
  const sessionChanges = useRef(0);
  const [activeSession, setActiveSession] = useState('');
  useEffect(() => {
    if (!sessions.some((s) => s.id === activeSession)) setActiveSession(sessions[0]?.id || '');
  }, [sessions, activeSession]);
  const [closeSession, setCloseSession] = useState<Session | null>(null);
  const [credentialRequests, setCredentialRequests] = useState<
    { requestId: string; credentialId: string; sessionId: string }[]
  >([]);
  async function reload() {
    setKnowledgeRevision((v) => v + 1);
    setError('');
    const changes = sessionChanges.current;
    try {
      const [snapshot, opened] = await Promise.all([api.Snapshot(), api.ListSessions()]);
      setData(snapshot);
      // A delayed refresh must not restore a closed tab, discard a new one,
      // or overwrite a newer process-exit event.
      if (changes === sessionChanges.current) setSessions(opened);
      await refreshConversations();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setLoading(false);
    }
  }
  useEffect(() => {
    void reload();
  }, []);
  useEffect(() => {
    const offSession = window.runtime?.EventsOn('session-updated', (session: Session) => {
      sessionChanges.current++;
      setSessions((v) => v.map((s) => (s.id === session.id ? session : s)));
    });
    const off = window.runtime?.EventsOn(
      'credential-needed',
      (request: { requestId: string; credentialId: string; sessionId: string }) => {
        setCredentialRequests((v) => [...v, request]);
        void reload();
      },
    );
    const ended = window.runtime?.EventsOn('credential-request-ended', (id: string) =>
      setCredentialRequests((v) => v.filter((r) => r.requestId !== id)),
    );
    return () => {
      offSession?.();
      off?.();
      ended?.();
    };
  }, []);
  useEffect(() => {
    if (toast) {
      const timeout = setTimeout(() => setToast(''), 4500);
      return () => clearTimeout(timeout);
    }
  }, [toast]);
  const saved = () => {
    setEditor(null);
    void reload();
    setToast('配置已保存');
  };
  const createCredential = (done: (c: Credential) => void) =>
    setCredentialEditor({ value: newCredential(), done });
  const credentialSaved = (c: Credential) => {
    credentialEditor?.done?.(c);
    setCredentialEditor(null);
    void reload();
    setToast('凭据已保存到系统凭据库');
  };
  async function ensureTrust(id: string) {
    for (let i = 0; i < 8; i++) {
      const check = await api.CheckHost(id);
      if (check.known) return true;
      const accepted = await new Promise<boolean>((resolve) => {
        setTrustError('');
        setTrust({ check, resolve });
      });
      if (!accepted) return false;
    }
    throw new Error('跳板机层数过多');
  }
  async function hostAction(host: Host, test = false, previous?: Session) {
    if (busy) return;
    setBusy(host.id);
    setError('');
    try {
      if (!(await ensureTrust(host.id))) return;
      if (test) {
        await api.TestHost(host.id);
        setToast('已成功连接 ' + host.name);
      } else {
        const session = previous
          ? await api.ReconnectSession(previous.id)
          : await api.CreateSession(host.id);
        sessionChanges.current++;
        setSessions((v) => [...v.filter((s) => s.id !== session.id), session]);
        setActiveSession(session.id);
        restoreSidebarItem(sessionItemKey(session), session.hostId, session.id);
        setPage('terminal');
        setSearch('');
        if (previous && previous.id !== session.id) await removeSession(previous.id);
        setData(await api.Snapshot());
      }
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy('');
    }
  }
  function requestNewConversation(hostId = conversationHost) {
    setNewConversationError('');
    setNewConversationHost(hostId || connectionHosts[0]?.id || '');
  }
  async function createConversation(input: NewConversationInput) {
    if (busy || (input.kind === 'existing' && !input.hostId)) return;
    let created = false;
    setBusy('new-conversation');
    setNewConversationError('');
    try {
      let conversation: Conversation;
      if (input.kind === 'direct') {
        const result = await api.CreateDirectConversationWithAI(input.host, input.saveHost, input.aiProfileId);
        conversation = result.conversation;
        setData((previous) =>
          input.saveHost
            ? { ...previous, hosts: [...previous.hosts, result.host] }
            : {
                ...previous,
                conversationHosts: [...(previous.conversationHosts || []), result.host],
              },
        );
      } else {
        conversation = await api.CreateConversationWithAI(input.hostId, input.workspace, input.aiProfileId);
      }
      created = true;
      restoreSidebarHost(conversation.hostId);
      conversationRequest.current++;
      setConversations((v) => [conversation, ...v]);
      setSelectedConversation(conversationKey(conversation));
      setCollapsedConversationHosts((previous) => {
        const next = new Set(previous);
        next.delete(conversation.hostId);
        return next;
      });
      setConversationHost(conversation.hostId);
      setNewConversationHost(null);
      goto('conversations');
      if (input.kind === 'direct') await openConversation(conversation);
    } catch (e) {
      if (created) setError(errorText(e));
      else setNewConversationError(errorText(e));
    } finally {
      setBusy('');
    }
  }
  async function continueConversation(conversation: Conversation) {
    if (busy) return;
    setBusy('open-conversation');
    setError('');
    try {
      await openConversation(conversation);
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy('');
    }
  }
  async function openConversation(conversation: Conversation) {
    const active = sessions.find(
      (s) =>
        s.hostId === conversation.hostId && s.conversationId === conversation.id && sessionLive(s),
    );
    if (active) {
      openSession(active);
      return;
    }
    if (!(await ensureTrust(conversation.hostId))) return;
    const session = await api.OpenConversationSession(conversation.hostId, conversation.id);
    sessionChanges.current++;
    setSessions((v) => [...v.filter((s) => s.id !== session.id), session]);
    openSession(session);
    setData(await api.Snapshot());
  }
  async function trustHost() {
    if (!trust) return;
    const previousBusy = busy;
    setBusy('trust');
    try {
      await api.TrustHost(trust.check.token);
      trust.resolve(true);
      setTrust(null);
    } catch (e) {
      setTrustError(errorText(e));
    } finally {
      setBusy(previousBusy);
    }
  }
  async function deleteItem() {
    if (!deletion) return;
    setBusy('delete');
    setDeleteError('');
    try {
      await api.Delete(deletion.kind, deletion.id);
      setDeletion(null);
      await reload();
      setToast('已删除本机配置');
    } catch (e) {
      await reload();
      setDeleteError(errorText(e));
    } finally {
      setBusy('');
    }
  }
  async function importHosts() {
    setBusy('import');
    try {
      const items = await api.ImportSSH();
      setImports(items);
      setImportSelection(new Set(items.map((_, i) => i)));
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy('');
    }
  }
  async function saveImports() {
    if (!imports) return;
    setBusy('import');
    try {
      for (const i of importSelection) {
        await api.SaveHost(imports[i]);
      }
      setImports(null);
      await reload();
      setToast('SSH 主机已导入；请检查凭据和跳板机绑定');
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy('');
    }
  }
  async function resourceAction(resource: Resource) {
    setBusy(resource.id);
    setError('');
    try {
      if (resource.route === 'ssh' && !(await ensureTrust(resource.hostId))) return;
      const text = await api.TestResource(resource.id);
      setResult({ name: resource.name, content: text });
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy('');
    }
  }
  const credentialName = (id: string) =>
    data.credentials.find((c) => c.id === id)?.name || '系统 SSH 配置';
  const hostName = (id: string) => connectionHosts.find((h) => h.id === id)?.name || '本机';
  const matches = (...values: string[]) =>
    values.join(' ').toLowerCase().includes(search.toLowerCase());
  const visibleHosts = data.hosts.filter(
    (h) =>
      matches(h.name, h.address, h.user, h.group) &&
      (group === '全部主机' || h.group === group) &&
      (routeFilter === 'all' || (routeFilter === 'jump' ? !!h.jumpHostId : !h.jumpHostId)),
  );
  const visibleRemovedHosts = sidebarHosts.filter(
    (h) => removedSidebarHosts.has(h.id) && matches(h.name, h.address, h.user, h.group),
  );
  const credentialUses = (id: string) =>
    connectionHosts.filter((h) => h.credentialId === id).length +
    data.resources.filter((r) => r.credentialId === id).length +
    data.aiProfiles.filter((p) => p.credentialId === id).length;
  const currentSession = sessions.find((s) => s.id === activeSession);
  const currentConversation =
    conversations.find((c) => conversationKey(c) === selectedConversation) || conversations[0];
  const activeConversation =
    currentSession &&
    conversations.find(
      (c) => c.hostId === currentSession.hostId && c.id === currentSession.conversationId,
    );
  const summaryConversation = page === 'terminal' ? activeConversation : currentConversation;
  const title = isSettings
    ? '设置'
    : page === 'resources'
      ? '外联系统'
      : page === 'monitoring'
        ? '告警监控'
        : page === 'knowledge'
          ? '本地知识'
          : page === 'terminal'
            ? activeConversation?.title || currentSession?.name || '工作区'
            : currentConversation?.title || 'SSH + AI 工作区';
  const add = () => {
    if (page === 'hosts') setEditor({ kind: 'host', value: newHost() });
    if (page === 'credentials') setCredentialEditor({ value: newCredential() });
    if (page === 'resources') setEditor({ kind: 'resource', value: newResource() });
    if (page === 'ai') setEditor({ kind: 'ai', value: newAI() });
  };
  async function removeSession(id: string) {
    await api.CloseSession(id);
    sessionChanges.current++;
    setSessions((v) => v.filter((s) => s.id !== id));
  }
  const requestClose = (session: Session) => {
    if (sessionLive(session)) setCloseSession(session);
    else void removeSession(session.id).catch((e) => setError(errorText(e)));
  };
  const openSession = (session: Session) => {
    restoreSidebarItem(sessionItemKey(session), session.hostId, session.id);
    setActiveSession(session.id);
    setConversationHost(session.hostId);
    if (session.conversationId)
      setSelectedConversation(
        conversationKey({ hostId: session.hostId, id: session.conversationId }),
      );
    goto('terminal');
  };
  const goto = (value: Page) => {
    setPage(value);
    if (settingsPages.some((item) => item.id === value)) setLastSettings(value as SettingsPage);
    setSearch('');
    setSelectedRow('');
    setRouteFilter('all');
  };
  const openMonitoring = (view: MonitoringView = 'monitors') => {
    setMonitorInventoryRequest((v) => ({ sequence: v.sequence + 1, sessionId: '', view }));
    goto('monitoring');
  };
  const returnToWorkspace = () =>
    goto(workspacePage.current === 'terminal' && currentSession ? 'terminal' : 'conversations');
  const selectConversation = (conversation: Conversation) => {
    setSelectedConversation(conversationKey(conversation));
    setConversationHost(conversation.hostId);
    const session = conversationSession(sessions, conversation);
    restoreSidebarItem(conversationKey(conversation), conversation.hostId, session?.id);
    if (session) openSession(session);
    else goto('conversations');
  };
  useEffect(() => {
    return window.runtime?.EventsOn('desktop-command', (command: string) => {
      // Native menu commands must not dismiss an unfinished sheet or approve it.
      if (document.querySelector('dialog[open]') || busy) return;
      if (command === 'monitoring' || command === 'monitor-inventory') {
        openMonitoring(command === 'monitoring' ? 'incidents' : 'monitors');
      } else if (command === 'settings') {
        goto(lastSettings);
      } else if (
        command === 'workspace' ||
        command === 'page:conversations' ||
        command === 'page:sessions'
      ) {
        returnToWorkspace();
      } else if (command.startsWith('page:')) {
        const destination = command.slice(5) as Page;
        if (['hosts', 'credentials', 'resources', 'knowledge', 'ai'].includes(destination)) {
          goto(destination);
          if (destination === 'hosts') setGroup('全部主机');
        }
      } else if (command === 'new-conversation') {
        requestNewConversation();
      } else if (command === 'new-host') {
        goto('hosts');
        setEditor({ kind: 'host', value: newHost() });
      } else if (command === 'new-credential') {
        goto('credentials');
        setCredentialEditor({ value: newCredential() });
      } else if (command === 'new-resource') {
        goto('resources');
        setEditor({ kind: 'resource', value: newResource() });
      } else if (command === 'new-knowledge') {
        goto('knowledge');
        setKnowledgeIntent('new');
      } else if (command === 'new-ai') {
        goto('ai');
        setEditor({ kind: 'ai', value: newAI() });
      } else if (command === 'sidebar') {
        setSidebarVisible(!sidebarVisible);
      } else if (command === 'search') {
        if (!isManagement) setSidebarVisible(true);
        requestAnimationFrame(() =>
          (isManagement ? searchRef : conversationSearchRef).current?.focus(),
        );
      } else if (command === 'refresh') {
        void reload();
      } else if (command === 'import') {
        goto('hosts');
        void importHosts();
      }
    });
  }, [
    busy,
    page,
    data.hosts,
    data.conversationHosts,
    conversationHost,
    lastSettings,
    sidebarVisible,
    activeSession,
    sessions,
  ]);
  const itemCount =
    page === 'removedHosts'
      ? visibleRemovedHosts.length
      : page === 'hosts'
        ? visibleHosts.length
        : page === 'credentials'
          ? data.credentials.filter((c) => matches(c.name, c.username, kinds[c.kind])).length
          : page === 'resources'
            ? data.resources.filter((r) =>
                matches(r.name, r.kind, kinds[r.kind], r.endpoint, r.description || ''),
              ).length
            : page === 'ai'
              ? data.aiProfiles.filter((p) => matches(p.name, p.provider, p.model)).length
              : page === 'conversations'
                ? conversations.filter(
                    (c) =>
                      (!conversationHost || c.hostId === conversationHost) &&
                      matches(
                        c.title,
                        c.preview,
                        connectionHosts.find((h) => h.id === c.hostId)?.name || '',
                        connectionHosts.find((h) => h.id === c.hostId)?.address || '',
                      ),
                  ).length
                : sessions.length;
  const needed = credentialRequests[0];
  const neededCredential = needed && data.credentials.find((c) => c.id === needed.credentialId);
  const resolveNeeded = (accepted: boolean) => {
    if (!needed) return;
    void api
      .ResolveCredentialRequest(needed.requestId, accepted)
      .catch((e) => setError(errorText(e)));
    setCredentialRequests((v) => v.filter((r) => r.requestId !== needed.requestId));
    void reload();
  };
  return (
    <div className={'shell' + (sidebarVisible ? '' : ' sidebar-hidden')}>
      <aside className="sidebar session-sidebar" aria-label="会话侧栏">
        <div className="sidebar-titlebar">
          <button
            className="icon-button"
            aria-label="收起会话列表"
            title="收起会话列表"
            onClick={() => setSidebarVisible(false)}
          >
            <PanelLeft size={18} />
          </button>
        </div>
        <div className="workspace-identity">
          <span className="workspace-mark">
            <img src="./app-icon.svg" width={38} height={38} alt="" />
          </span>
          <div>
            <strong>
              aissh<span> / </span>
            </strong>
            <small>SSH + AI 会话</small>
          </div>
        </div>
        <SessionSidebar
          hosts={sidebarHosts}
          conversations={conversations}
          sessions={sessions}
          selectedConversation={currentConversation ? conversationKey(currentConversation) : ''}
          activeSession={
            (isManagement ? workspacePage.current : page) === 'terminal' ? activeSession : ''
          }
          search={conversationSearch}
          onSearch={setConversationSearch}
          searchRef={conversationSearchRef}
          collapsedHosts={collapsedConversationHosts}
          onToggleHost={(id) =>
            setCollapsedConversationHosts((previous) => {
              const next = new Set(previous);
              if (next.has(id)) next.delete(id);
              else next.add(id);
              return next;
            })
          }
          busy={!!busy || !connected()}
          onSelect={selectConversation}
          onOpen={openSession}
          onClose={requestClose}
          onNew={requestNewConversation}
          removedHosts={removedSidebarHosts}
          onRemoveHost={(host) => {
            setRemovedSidebarHosts((previous) => new Set(previous).add(host.id));
            setToast('已从列表移除「' + host.name + '」，可在“设置 → 已移除服务器”中恢复');
          }}
          hiddenItems={hiddenSidebarItems}
          onHide={(item) => {
            setHiddenSidebarItems((previous) => new Set(previous).add(item.key));
            setToast('已隐藏「' + item.title + '」，可从会话历史恢复');
          }}
        />
        {conversationError && (
          <div className="sidebar-load-error">
            <ErrorBox error={conversationError} />
            <button className="button small" onClick={() => void refreshConversations()}>
              重试
            </button>
          </div>
        )}
        <div className="session-sidebar-footer">
          <nav aria-label="管理菜单">
            <button
              className={'settings-entry ' + (page === 'monitoring' ? 'selected' : '')}
              aria-current={page === 'monitoring' ? 'page' : undefined}
              onClick={() => openMonitoring()}
            >
              <Activity size={18} />
              <span>告警监控</span>
            </button>
            <button
              className={'settings-entry ' + (page === 'resources' ? 'selected' : '')}
              aria-current={page === 'resources' ? 'page' : undefined}
              onClick={() => goto('resources')}
            >
              <Database size={18} />
              <span>外联系统</span>
            </button>
            <button
              className={'settings-entry ' + (page === 'knowledge' ? 'selected' : '')}
              aria-current={page === 'knowledge' ? 'page' : undefined}
              onClick={() => goto('knowledge')}
            >
              <BookOpen size={18} />
              <span>本地知识</span>
            </button>
            <button
              className={'settings-entry ' + (isSettings ? 'selected' : '')}
              aria-label="设置"
              aria-pressed={isSettings}
              onClick={() => goto(lastSettings)}
            >
              <Settings size={18} />
              <span>设置</span>
              <kbd>{primaryModifier},</kbd>
            </button>
          </nav>
          <small>{sessions.filter(sessionLive).length} 个连接运行中 · 会话保存在本机</small>
        </div>
      </aside>
      <main className={'main ' + (!isManagement ? 'session-main' : 'settings-main')}>
        <header className="topbar">
          {!sidebarVisible && (
            <button
              className="icon-button"
              aria-label="展开会话列表"
              title="展开会话列表"
              onClick={() => setSidebarVisible(true)}
            >
              <PanelLeft size={18} />
            </button>
          )}
          <div className="window-heading">
            <h1>{title}</h1>
            <span>
              {isSettings
                ? '管理主机、凭据与 AI 服务'
                : page === 'resources'
                  ? '管理系统信息、连接与访问授权'
                  : page === 'monitoring'
                    ? '独立告警任务 · 后台检查 · 桌宠提醒'
                    : page === 'knowledge'
                      ? '文件文档 · 会话经验 · 数据表'
                      : page === 'terminal'
                        ? currentSession
                          ? currentSession.name + ' · ' + sessionStatus[currentSession.status]
                          : '未连接'
                        : '对话内容 · SSH · 监控'}
            </span>
          </div>
          <div className="toolbar-actions">
            <button
              className={'icon-button monitoring-launch ' + (alertCount ? 'has-alerts' : '')}
              aria-label={`告警监控${alertCount ? `，${alertCount} 个问题` : ''}`}
              aria-current={page === 'monitoring' ? 'page' : undefined}
              title="告警监控 · 桌宠"
              onClick={() => openMonitoring('incidents')}
            >
              <Bell size={17} />
              {alertCount > 0 && <span>{alertCount}</span>}
            </button>
            {isManagement ? (
              <button className="button" onClick={returnToWorkspace}>
                返回工作区
                <ArrowUpRight size={15} />
              </button>
            ) : currentSession && page === 'terminal' ? (
              <button
                className="button"
                disabled={!!busy}
                onClick={() => requestClose(currentSession)}
              >
                {sessionLive(currentSession) ? '断开连接' : '移除连接'}
              </button>
            ) : null}
            {!isManagement && summaryConversation && (
              <button
                className="button"
                disabled={!!busy || !connected() || !summaryConversation.messageCount}
                onClick={() => {
                  setKnowledgeIntent(summaryConversation);
                  goto('knowledge');
                }}
              >
                <BookOpen size={15} />
                总结到知识库
              </button>
            )}
            {!sidebarVisible && (
              <>
                <button
                  className="icon-button"
                  aria-label="外联系统"
                  title="外联系统"
                  onClick={() => goto('resources')}
                >
                  <Database size={17} />
                </button>
                <button
                  className="icon-button"
                  aria-label="本地知识"
                  title="本地知识"
                  onClick={() => goto('knowledge')}
                >
                  <BookOpen size={17} />
                </button>
                <button
                  className="icon-button"
                  aria-label="新建会话"
                  title="新建会话"
                  onClick={() => requestNewConversation()}
                  disabled={!!busy || !connected()}
                >
                  <Plus size={17} />
                </button>
                <button
                  className="icon-button"
                  aria-label="设置"
                  title="设置"
                  onClick={() => goto(lastSettings)}
                >
                  <Settings size={17} />
                </button>
              </>
            )}
          </div>
        </header>
        {(isSettings || page === 'resources') && (
          <>
            {isSettings && (
              <nav className="settings-tabs" aria-label="设置分类">
                {settingsPages.map((item) => (
                  <button
                    key={item.id}
                    aria-current={page === item.id ? 'page' : undefined}
                    onClick={() => goto(item.id)}
                  >
                    <item.icon size={16} />
                    {item.label}
                  </button>
                ))}
              </nav>
            )}
            <div className="collection-toolbar">
              <label className="search">
                <Search size={15} />
                <input
                  ref={searchRef}
                  aria-label="搜索配置"
                  placeholder={
                    page === 'hosts' || page === 'removedHosts'
                      ? '搜索名称、地址或分组…'
                      : page === 'resources'
                        ? '搜索系统、用途或地址…'
                        : '搜索配置…'
                  }
                  value={search}
                  onChange={(e) => setSearch(e.target.value)}
                />
                {search && (
                  <button
                    className="icon-button"
                    aria-label="清除搜索"
                    onClick={() => setSearch('')}
                  >
                    <X size={12} />
                  </button>
                )}
              </label>
              {page === 'hosts' && (
                <>
                  <select
                    className="host-group-filter"
                    aria-label="主机分组"
                    value={group}
                    onChange={(e) => setGroup(e.target.value)}
                  >
                    <option>全部主机</option>
                    {[...new Set(data.hosts.map((h) => h.group).filter(Boolean))].map((name) => (
                      <option key={name}>{name}</option>
                    ))}
                  </select>
                  <div className="filter-tabs" role="group" aria-label="连接方式">
                    {(
                      [
                        ['all', '全部'],
                        ['direct', '直接连接'],
                        ['jump', '通过跳板'],
                      ] as const
                    ).map(([value, label]) => (
                      <button
                        key={value}
                        aria-pressed={routeFilter === value}
                        onClick={() => setRouteFilter(value)}
                      >
                        {label}
                      </button>
                    ))}
                  </div>
                </>
              )}
              {page !== 'removedHosts' && (
                <div className="settings-actions">
                  {page === 'hosts' && (
                    <button
                      className="button"
                      aria-label="导入 SSH 配置"
                      disabled={!!busy || !connected()}
                      onClick={() => void importHosts()}
                    >
                      <Download size={15} />
                      导入
                    </button>
                  )}
                  <button
                    className="button primary"
                    disabled={!connected()}
                    onClick={add}
                    aria-label={
                      page === 'hosts'
                        ? '添加主机'
                        : page === 'credentials'
                          ? '添加凭据'
                          : page === 'resources'
                            ? '添加系统'
                            : '添加 AI 配置'
                    }
                  >
                    <Plus size={15} />
                    {page === 'hosts'
                      ? '新建主机'
                      : page === 'credentials'
                        ? '新建凭据'
                        : page === 'resources'
                          ? '新建系统'
                          : '新建 AI 配置'}
                  </button>
                </div>
              )}
            </div>
          </>
        )}
        {error && (
          <div className="page-error">
            <ErrorBox error={error} />
            <button className="icon-button" aria-label="关闭错误提示" onClick={() => setError('')}>
              <X size={15} />
            </button>
          </div>
        )}
        <Monitoring
          inventoryRequest={monitorInventoryRequest}
          monitoring={monitoring}
          pageVisible={page === 'monitoring'}
          onOpenPage={() => openMonitoring()}
          sessions={sessions}
          activeSession={activeSession}
          onOpenSession={(id) => {
            const session = sessions.find((s) => s.id === id);
            if (session) openSession(session);
            else setError('该会话已移除，请重新连接对应服务器');
          }}
        />
        <div
          className="page-content"
          style={{ display: isManagement && page !== 'monitoring' ? undefined : 'none' }}
        >
          {page === 'credentials' && !!data.pendingDeletes?.length && (
            <div className="info-box">
              <span>有 {data.pendingDeletes.length} 项已停用的秘密等待从系统凭据库清理。</span>
              <button
                className="button small"
                onClick={() =>
                  void api
                    .CleanupSecrets()
                    .then(() => reload())
                    .catch((e) => setError(errorText(e)))
                }
              >
                清理旧凭据
              </button>
            </div>
          )}
          {loading ? (
            <div className="loading">
              <LoaderCircle className="spin" />
              正在读取本机配置…
            </div>
          ) : (
            <>
              {page === 'knowledge' && (
                <KnowledgeLibrary
                  data={data}
                  conversations={conversations}
                  searchRef={searchRef}
                  revision={knowledgeRevision}
                  intent={knowledgeIntent}
                  onConsumeIntent={() => setKnowledgeIntent(null)}
                  onOpenConversation={(hostId, id) => {
                    const conversation = conversations.find(
                      (c) => c.hostId === hostId && c.id === id,
                    );
                    if (conversation) {
                      setSelectedConversation(conversationKey(conversation));
                      setConversationHost(hostId);
                      goto('conversations');
                    } else setError('来源会话已不存在或历史记录已清理');
                  }}
                />
              )}
              {page === 'hosts' &&
                (visibleHosts.length ? (
                  <div className="table-card">
                    <table>
                      <thead>
                        <tr>
                          <th>主机</th>
                          <th>连接地址</th>
                          <th>登录凭据</th>
                          <th>访问路径</th>
                          <th className="align-right">操作</th>
                        </tr>
                      </thead>
                      <tbody>
                        {visibleHosts.map((h) => (
                          <tr
                            key={h.id}
                            className={selectedRow === h.id ? 'selected-row' : ''}
                            onClick={() => setSelectedRow(h.id)}
                            onDoubleClick={(e) => {
                              if (!(e.target as HTMLElement).closest('button') && !busy)
                                void hostAction(h);
                            }}
                          >
                            <td>
                              <div className="identity">
                                <div className="row-icon">
                                  <Server size={19} />
                                </div>
                                <div>
                                  <strong>{h.name}</strong>
                                  <small>{h.group || '未分组'}</small>
                                </div>
                              </div>
                            </td>
                            <td>
                              <span className="mono">
                                {h.user ? h.user + '@' : ''}
                                {h.address}
                              </span>
                              <small className="cell-small">端口 {h.port}</small>
                            </td>
                            <td>
                              <span className="credential-chip">
                                <KeyRound size={13} />
                                {credentialName(h.credentialId)}
                              </span>
                            </td>
                            <td>
                              <span className="subtle">
                                {h.jumpHostId ? hostName(h.jumpHostId) : '直接连接'}
                              </span>
                            </td>
                            <td>
                              <div className="row-actions">
                                <button
                                  className="button connect"
                                  disabled={!!busy}
                                  onClick={() => void hostAction(h)}
                                >
                                  {busy === h.id ? (
                                    <LoaderCircle size={14} className="spin" />
                                  ) : (
                                    <ArrowUpRight size={15} />
                                  )}
                                  连接
                                </button>
                                <button
                                  className="icon-button"
                                  aria-label={'测试连接 ' + h.name}
                                  title="测试连接"
                                  disabled={!!busy}
                                  onClick={() => void hostAction(h, true)}
                                >
                                  <PlugZap size={16} />
                                </button>
                                <button
                                  className="icon-button"
                                  aria-label={'编辑 ' + h.name}
                                  onClick={() => setEditor({ kind: 'host', value: h })}
                                >
                                  <Pencil size={15} />
                                </button>
                                <button
                                  className="icon-button danger-hover"
                                  aria-label={'删除 ' + h.name}
                                  onClick={() => {
                                    setDeleteError('');
                                    setDeletion({ kind: 'host', id: h.id, name: h.name });
                                  }}
                                >
                                  <Trash2 size={15} />
                                </button>
                              </div>
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                ) : (
                  <Empty
                    icon={<Server size={29} />}
                    title={search || routeFilter !== 'all' ? '没有找到匹配的主机' : '没有主机'}
                    description={
                      search || routeFilter !== 'all'
                        ? '试试其他搜索条件或连接方式。'
                        : '添加主机，或从 SSH 配置导入。'
                    }
                    action={
                      !search &&
                      routeFilter === 'all' && (
                        <button className="button primary" onClick={add} disabled={!connected()}>
                          <Plus size={16} />
                          添加第一台主机
                        </button>
                      )
                    }
                  />
                ))}
              {page === 'removedHosts' &&
                (visibleRemovedHosts.length ? (
                  <div className="table-card">
                    <table aria-label="已移除服务器">
                      <thead>
                        <tr>
                          <th>服务器</th>
                          <th>连接地址</th>
                          <th className="align-right">操作</th>
                        </tr>
                      </thead>
                      <tbody>
                        {visibleRemovedHosts.map((host) => (
                          <tr key={host.id}>
                            <td>
                              <div className="identity">
                                <div className="row-icon">
                                  <Server size={19} />
                                </div>
                                <div>
                                  <strong>{host.name}</strong>
                                  <small>{host.group || '未分组'}</small>
                                </div>
                              </div>
                            </td>
                            <td>
                              <span className="mono">
                                {host.user ? host.user + '@' : ''}
                                {host.address}
                              </span>
                              <small className="cell-small">端口 {host.port}</small>
                            </td>
                            <td>
                              <div className="row-actions">
                                <button
                                  className="button"
                                  aria-label={'恢复 ' + host.name + ' 到会话列表'}
                                  disabled={!!busy}
                                  onClick={() => {
                                    restoreSidebarHost(host.id);
                                    setToast('已恢复「' + host.name + '」到会话列表');
                                  }}
                                >
                                  <RotateCcw size={15} />
                                  恢复到会话列表
                                </button>
                              </div>
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                ) : (
                  <Empty
                    icon={<RotateCcw size={29} />}
                    title={search ? '没有找到匹配的服务器' : '没有已移除的服务器'}
                    description={
                      search
                        ? '试试其他名称、地址或分组。'
                        : '从会话列表移除的服务器会显示在这里，可随时恢复。'
                    }
                  />
                ))}
              {page === 'credentials' &&
                (data.credentials.filter((c) => matches(c.name, c.username, kinds[c.kind]))
                  .length ? (
                  <div className="table-card">
                    <table>
                      <thead>
                        <tr>
                          <th>凭据</th>
                          <th>类型</th>
                          <th>账号 / 密钥</th>
                          <th>使用情况</th>
                          <th className="align-right">操作</th>
                        </tr>
                      </thead>
                      <tbody>
                        {data.credentials
                          .filter((c) => matches(c.name, c.username, kinds[c.kind]))
                          .map((c) => (
                            <tr
                              key={c.id}
                              className={selectedRow === c.id ? 'selected-row' : ''}
                              onClick={() => setSelectedRow(c.id)}
                              onDoubleClick={(e) => {
                                if (!(e.target as HTMLElement).closest('button'))
                                  setCredentialEditor({ value: c });
                              }}
                            >
                              <td>
                                <div className="identity">
                                  <div className="row-icon purple">
                                    <KeyRound size={19} />
                                  </div>
                                  <div>
                                    <strong>{c.name}</strong>
                                    <small>
                                      {c.hasSecret
                                        ? '已保存到系统凭据库'
                                        : c.kind === 'agent'
                                          ? '使用 SSH Agent'
                                          : '私钥文件引用'}
                                    </small>
                                  </div>
                                </div>
                              </td>
                              <td>
                                <span className="pill">{kinds[c.kind]}</span>
                              </td>
                              <td>
                                <span className="mono">
                                  {c.kind === 'key' ? c.identityFile : c.username || '—'}
                                </span>
                              </td>
                              <td>
                                <span className="subtle">{credentialUses(c.id)} 个配置引用</span>
                              </td>
                              <td>
                                <div className="row-actions">
                                  <button
                                    className="button small"
                                    onClick={() => setCredentialEditor({ value: c })}
                                  >
                                    <Pencil size={14} />
                                    编辑
                                  </button>
                                  <button
                                    className="icon-button danger-hover"
                                    aria-label={'删除 ' + c.name}
                                    onClick={() => {
                                      setDeleteError('');
                                      setDeletion({ kind: 'credential', id: c.id, name: c.name });
                                    }}
                                  >
                                    <Trash2 size={15} />
                                  </button>
                                </div>
                              </td>
                            </tr>
                          ))}
                      </tbody>
                    </table>
                  </div>
                ) : (
                  <Empty
                    icon={<KeyRound size={29} />}
                    title="没有凭据"
                    description="保存密码、Token，或引用已有私钥。多个主机和系统可以绑定同一项凭据。"
                    action={
                      <button className="button primary" onClick={add} disabled={!connected()}>
                        <Plus size={16} />
                        添加凭据
                      </button>
                    }
                  />
                ))}
              {page === 'resources' &&
                (data.resources.filter((r) =>
                  matches(r.name, r.kind, kinds[r.kind], r.endpoint, r.description || ''),
                ).length ? (
                  <div className="table-card">
                    <table>
                      <thead>
                        <tr>
                          <th>系统</th>
                          <th>凭据</th>
                          <th>访问与授权</th>
                          <th className="align-right">操作</th>
                        </tr>
                      </thead>
                      <tbody>
                        {data.resources
                          .filter((r) =>
                            matches(r.name, r.kind, kinds[r.kind], r.endpoint, r.description || ''),
                          )
                          .map((r) => (
                            <tr
                              key={r.id}
                              className={selectedRow === r.id ? 'selected-row' : ''}
                              onClick={() => setSelectedRow(r.id)}
                            >
                              <td>
                                <div className="identity">
                                  <div className="row-icon blue">
                                    {r.kind === 'http' ? (
                                      <Globe size={19} />
                                    ) : (
                                      <Database size={19} />
                                    )}
                                  </div>
                                  <div>
                                    <strong>
                                      <span>{r.name}</span>
                                      <span className="inline-kind">{kinds[r.kind]}</span>
                                    </strong>
                                    <small className="mono">{r.endpoint}</small>
                                    <small className="cell-small">{discoveryStatus(r)}</small>
                                    {r.description && (
                                      <small className="system-description" title={r.description}>
                                        {r.description}
                                      </small>
                                    )}
                                  </div>
                                </div>
                              </td>
                              <td>
                                <span className="credential-chip">
                                  <KeyRound size={13} />
                                  {r.credentialId ? credentialName(r.credentialId) : '无需认证'}
                                </span>
                              </td>
                              <td>
                                <span>
                                  {r.route === 'ssh'
                                    ? hostName(r.hostId) + ' · SSH 隧道'
                                    : '本机直接访问'}
                                </span>
                                <small className="cell-small">按接口权限与风险规则执行</small>
                              </td>
                              <td>
                                <div className="row-actions">
                                  <button
                                    className="button small"
                                    disabled={!!busy}
                                    onClick={() => void resourceAction(r)}
                                  >
                                    {busy === r.id ? (
                                      <LoaderCircle size={13} className="spin" />
                                    ) : (
                                      <PlugZap size={13} />
                                    )}
                                    测试连接
                                  </button>
                                  <button
                                    className="button small"
                                    onClick={() => setResourceGuide(r.id)}
                                  >
                                    <BookOpen size={13} />
                                    操作目录
                                  </button>
                                  <button
                                    className="button small"
                                    onClick={() => {
                                      setResourceGuide(r.id);
                                    }}
                                  >
                                    <Play size={12} />
                                    执行
                                  </button>
                                  <button
                                    className="icon-button"
                                    aria-label={'编辑 ' + r.name}
                                    onClick={() => setEditor({ kind: 'resource', value: r })}
                                  >
                                    <Pencil size={14} />
                                  </button>
                                  <button
                                    className="icon-button danger-hover"
                                    aria-label={'删除 ' + r.name}
                                    onClick={() => {
                                      setDeleteError('');
                                      setDeletion({ kind: 'resource', id: r.id, name: r.name });
                                    }}
                                  >
                                    <Trash2 size={14} />
                                  </button>
                                </div>
                              </td>
                            </tr>
                          ))}
                      </tbody>
                    </table>
                  </div>
                ) : (
                  <Empty
                    icon={<Database size={29} />}
                    title="还没有外联系统"
                    description="添加数据库、Redis 或 HTTP API，并选择访问凭据。"
                    action={
                      <button className="button primary" onClick={add} disabled={!connected()}>
                        <Plus size={16} />
                        添加外联系统
                      </button>
                    }
                  />
                ))}
              {page === 'ai' &&
                (data.aiProfiles.filter((p) => matches(p.name, p.provider, p.model)).length ? (
                  <div className="table-card">
                    <table>
                      <thead>
                        <tr>
                          <th>配置</th>
                          <th>后端</th>
                          <th>模型</th>
                          <th>凭据</th>
                          <th className="align-right">操作</th>
                        </tr>
                      </thead>
                      <tbody>
                        {data.aiProfiles
                          .filter((p) => matches(p.name, p.provider, p.model))
                          .map((p) => (
                            <tr
                              key={p.id}
                              className={selectedRow === p.id ? 'selected-row' : ''}
                              onClick={() => setSelectedRow(p.id)}
                            >
                              <td>
                                <div className="identity">
                                  <div className="row-icon purple">
                                    <Sparkles size={19} />
                                  </div>
                                  <div>
                                    <strong>
                                      <span>{p.name}</span>
                                      {data.defaultAIProfileId === p.id && (
                                        <span className="pill default-pill">默认</span>
                                      )}
                                    </strong>
                                    <small>{p.url || '本机 CLI'}</small>
                                  </div>
                                </div>
                              </td>
                              <td>{p.provider}</td>
                              <td>{p.model || '后端默认'}</td>
                              <td>
                                <span className="subtle">
                                  {p.credentialId ? credentialName(p.credentialId) : '沿用后端认证'}
                                </span>
                              </td>
                              <td>
                                <div className="row-actions">
                                  <button
                                    className="button small"
                                    onClick={() => setEditor({ kind: 'ai', value: p })}
                                  >
                                    <Pencil size={13} />
                                    编辑配置
                                  </button>
                                  <button
                                    className="icon-button danger-hover"
                                    aria-label={'删除 ' + p.name}
                                    onClick={() => {
                                      setDeleteError('');
                                      setDeletion({ kind: 'ai', id: p.id, name: p.name });
                                    }}
                                  >
                                    <Trash2 size={14} />
                                  </button>
                                </div>
                              </td>
                            </tr>
                          ))}
                      </tbody>
                    </table>
                  </div>
                ) : (
                  <Empty
                    icon={<Sparkles size={29} />}
                    title="没有 AI 配置"
                    description="使用本机 Codex、Claude Code，或配置模型 API。每台主机都可以选择自己的助手。"
                    action={
                      <button className="button primary" onClick={add} disabled={!connected()}>
                        <Plus size={16} />
                        添加 AI 配置
                      </button>
                    }
                  />
                ))}
            </>
          )}
        </div>
        <div className="workspace" style={{ display: isManagement ? 'none' : 'flex' }}>
          {page === 'conversations' && (
            <div className="workbench-sessions">
              {loading ? (
                <div className="loading">
                  <LoaderCircle className="spin" />
                  正在读取会话…
                </div>
              ) : (
                <Conversations
                  hosts={connectionHosts}
                  current={currentConversation}
                  busy={!!busy || !connected()}
                  onNew={requestNewConversation}
                  onContinue={(c) => void continueConversation(c)}
                />
              )}
            </div>
          )}
          <div
            className="workbench-sessions"
            style={{ display: page === 'terminal' ? 'flex' : 'none' }}
          >
            {sessions.map((s) => (
              <Workbench
                key={s.id}
                title={
                  conversations.find((c) => c.hostId === s.hostId && c.id === s.conversationId)
                    ?.title || 'AI 对话'
                }
                session={s}
                active={page === 'terminal' && activeSession === s.id}
                onOpenMonitoring={() =>
                  setMonitorInventoryRequest((v) => ({
                    sequence: v.sequence + 1,
                    sessionId: s.id,
                    view: 'monitors',
                  }))
                }
                onReconnect={() => {
                  const host = connectionHosts.find((h) => h.id === s.hostId);
                  if (host) void hostAction(host, false, s);
                }}
                canReconnect={!busy && connectionHosts.some((h) => h.id === s.hostId)}
              />
            ))}
            {!sessions.length && (
              <Empty
                icon={<TerminalSquare size={30} />}
                title="选择或新建一个会话"
                description="AI 对话、SSH 和监控会在这里一起打开。"
                action={
                  <button className="button primary" onClick={() => requestNewConversation()}>
                    新建会话
                    <Plus size={16} />
                  </button>
                }
              />
            )}
          </div>
        </div>
        {(isSettings || page === 'resources') && (
          <footer className="statusbar">
            <span>
              {itemCount} 个项目{search && ' · 已筛选'}
            </span>
            <span>
              {page === 'removedHosts'
                ? '仅从会话列表隐藏，主机配置与会话记录仍保留'
                : page === 'hosts'
                  ? '双击主机连接'
                  : page === 'credentials'
                    ? '秘密保存在系统凭据库'
                    : page === 'resources'
                      ? '按配置范围使用凭据'
                      : '每台主机可单独选择 AI 配置'}
            </span>
          </footer>
        )}
      </main>
      {newConversationHost !== null && (
        <NewConversation
          data={data}
          initialHostId={newConversationHost}
          busy={!!busy}
          error={newConversationError}
          onClose={() => setNewConversationHost(null)}
          onCreate={(input) => void createConversation(input)}
          createCredential={createCredential}
        />
      )}
      {toast && (
        <div className="toast" role="status">
          <CheckCircle2 size={18} />
          {toast}
          <button className="icon-button" aria-label="关闭通知" onClick={() => setToast('')}>
            <X size={14} />
          </button>
        </div>
      )}
      {editor?.kind === 'host' && (
        <HostEditor
          item={editor.value}
          data={data}
          onClose={() => setEditor(null)}
          onSaved={saved}
          createCredential={createCredential}
        />
      )}
      {editor?.kind === 'resource' && (
        <ResourceEditor
          item={editor.value}
          data={data}
          onClose={() => setEditor(null)}
          onSaved={saved}
          createCredential={createCredential}
        />
      )}
      {resourceGuide && data.resources.some((r) => r.id === resourceGuide) && (
        <ResourceGuide
          data={data}
          resource={data.resources.find((r) => r.id === resourceGuide)!}
          onClose={() => setResourceGuide('')}
          onExplore={async () => {
            const resource = data.resources.find((r) => r.id === resourceGuide)!;
            if (resource.route === 'ssh' && !(await ensureTrust(resource.hostId))) return;
            await api.StartResourceDiscovery(resource.id);
            setData(await api.Snapshot());
          }}
        />
      )}
      {editor?.kind === 'ai' && (
        <AIEditor
          item={editor.value}
          data={data}
          onClose={() => setEditor(null)}
          onSaved={saved}
          createCredential={(done) =>
            setCredentialEditor({ value: { ...newCredential(), kind: 'token' }, done })
          }
        />
      )}
      {credentialEditor && (
        <CredentialEditor
          item={credentialEditor.value}
          onClose={() => setCredentialEditor(null)}
          onSaved={credentialSaved}
        />
      )}
      {neededCredential && (
        <CredentialEditor
          key={needed.requestId}
          item={neededCredential}
          secretOnly
          onClose={() => resolveNeeded(false)}
          onSaved={() => resolveNeeded(true)}
        />
      )}
      {deletion && (
        <Modal
          title={'删除「' + deletion.name + '」？'}
          subtitle={
            deletion.kind === 'credential'
              ? '这会删除本机保存的凭据，不会撤销服务器上的账号或密码。被其他配置引用的凭据需要先解除绑定。'
              : '这会删除本机连接配置。'
          }
          onClose={() => !busy && setDeletion(null)}
        >
          <div className="modal-body">
            <ErrorBox error={deleteError} />
            <div className="form-actions">
              <button className="button" onClick={() => setDeletion(null)} disabled={!!busy}>
                取消
              </button>
              <button className="button danger" disabled={!!busy} onClick={() => void deleteItem()}>
                确认删除
              </button>
            </div>
          </div>
        </Modal>
      )}
      {trust && (
        <Modal
          title="核对 SSH 主机指纹"
          subtitle={'首次连接 ' + trust.check.name + ' · ' + trust.check.address}
          onClose={() => {
            trust.resolve(false);
            setTrust(null);
          }}
        >
          <div className="modal-body">
            <p className="explanation">
              请通过管理员或可信渠道核对以下指纹。确认后会保存该主机的公钥，后续连接将自动验证。
            </p>
            <div className="fingerprints">
              {trust.check.keys.map((k) => (
                <div key={k.fingerprint}>
                  <span>{k.algorithm}</span>
                  <code>{k.fingerprint}</code>
                </div>
              ))}
            </div>
            <ErrorBox error={trustError} />
            <div className="form-actions">
              <button
                className="button"
                onClick={() => {
                  trust.resolve(false);
                  setTrust(null);
                }}
              >
                取消连接
              </button>
              <button
                className="button primary"
                disabled={busy === 'trust'}
                onClick={() => void trustHost()}
              >
                <ShieldCheck size={16} />
                已核对，信任并继续
              </button>
            </div>
          </div>
        </Modal>
      )}
      {imports && (
        <Modal
          title="导入 SSH 主机"
          subtitle="读取 ~/.ssh/config 中的主机别名。导入后请配置私钥、跳板机等凭据绑定。"
          onClose={() => setImports(null)}
        >
          <div className="modal-body">
            {imports.length ? (
              <div className="import-list">
                {imports.map((h, i) => (
                  <label key={i} className="check import-item">
                    <input
                      type="checkbox"
                      checked={importSelection.has(i)}
                      onChange={(e) =>
                        setImportSelection((v) => {
                          const n = new Set(v);
                          if (e.target.checked) n.add(i);
                          else n.delete(i);
                          return n;
                        })
                      }
                    />
                    <span>
                      <strong>{h.name}</strong>
                      <small>
                        {h.user}@{h.address}:{h.port}
                      </small>
                    </span>
                  </label>
                ))}
              </div>
            ) : (
              <p className="explanation">
                没有找到可导入的主机别名。可以关闭此窗口后手动添加主机。
              </p>
            )}
            <div className="form-actions">
              <button className="button" onClick={() => setImports(null)}>
                取消
              </button>
              <button
                className="button primary"
                disabled={!importSelection.size || busy === 'import'}
                onClick={() => void saveImports()}
              >
                导入 {importSelection.size} 台主机
              </button>
            </div>
          </div>
        </Modal>
      )}
      {result && (
        <Modal
          title={result.name + ' · 执行结果'}
          subtitle="凭据由 aissh 使用，结果经过已知凭据脱敏。"
          wide
          onClose={() => setResult(null)}
        >
          <div className="modal-body">
            <pre className="result-output">{result.content}</pre>
            <div className="form-actions">
              <button className="button primary" onClick={() => setResult(null)}>
                完成
              </button>
            </div>
          </div>
        </Modal>
      )}
      {closeSession && (
        <Modal
          title={'断开 ' + closeSession.name + '？'}
          subtitle="此连接中的远端命令可能正在运行。断开会结束当前 aissh 会话。"
          onClose={() => setCloseSession(null)}
        >
          <div className="modal-body">
            <div className="form-actions">
              <button className="button" onClick={() => setCloseSession(null)}>
                保持连接
              </button>
              <button
                className="button danger"
                disabled={!!busy}
                onClick={async () => {
                  setBusy('close-session');
                  try {
                    await removeSession(closeSession.id);
                    setCloseSession(null);
                  } catch (e) {
                    setError(errorText(e));
                  } finally {
                    setBusy('');
                  }
                }}
              >
                断开连接
              </button>
            </div>
          </div>
        </Modal>
      )}
    </div>
  );
}

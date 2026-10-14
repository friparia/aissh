import type { WidgetArtifact, WidgetData, WidgetMeta, WidgetCandidate } from './widgets/protocol';
import type { SavedWidget } from './widgets/WidgetLibrary';
import type { ExternalBackend } from './externals-api';
import type { AlertPolicy, MonitoringState, PetBackend, MonitorCondition } from './monitoring-api';

export interface Credential {
  id: string;
  name: string;
  kind: 'password' | 'key' | 'token' | 'agent';
  username: string;
  identityFile: string;
  hasSecret: boolean;
  updatedAt: string;
}
export interface Host {
  id: string;
  name: string;
  address: string;
  port: number;
  user: string;
  group: string;
  credentialId: string;
  jumpHostId: string;
  aiProfileId: string;
  workspace: string;
}
export interface Resource {
  systemId?: string;
  systemName?: string;
  environment?: string;
  managed?: boolean;
  id: string;
  name: string;
  kind: 'http' | 'postgres' | 'mysql' | 'redis';
  endpoint: string;
  database: string;
  credentialId: string;
  hostId: string;
  route: 'local' | 'ssh';
  allowLocal: boolean;
  grant: 'ask' | 'session' | 'read';
  tls: boolean;
  description?: string;
  discoveryPath?: string;
  discovery?: ResourceDiscovery;
}
export interface ResourceCapability {
  name: string;
  risk: 'read' | 'write' | 'dangerous' | 'unknown';
  method?: string;
  path?: string;
  parameters?: string;
  evidence: string;
  example?: string;
}
export interface ResourceDiscovery {
  status: 'ready' | 'partial' | 'failed' | 'stale';
  exploredAt: string;
  summary: string;
  notes: string[];
  operations: ResourceCapability[] | null;
}
export interface Knowledge {
  id: string;
  title: string;
  kind: 'document' | 'experience' | 'data';
  content: string;
  tags: string[];
  hostId: string;
  resourceId: string;
  filePath: string;
  source: string;
  conversationId: string;
  sourceUrl: string;
  columns: string[];
  rows: string[][];
  changes?: { time: string; quote: string; values?: Record<string, string> }[];
  createdAt: string;
  updatedAt: string;
}
export const newKnowledge = (kind: Knowledge['kind'] = 'experience'): Knowledge => ({
  id: '',
  title: '',
  kind,
  content: '',
  tags: [],
  hostId: '',
  resourceId: '',
  filePath: '',
  source: '',
  sourceUrl: '',
  conversationId: '',
  columns: [],
  rows: [],
  createdAt: '',
  updatedAt: '',
});
export interface AIProfile {
  models?: string[];
  keepCodexSession?: boolean;
  id: string;
  name: string;
  provider: string;
  url: string;
  model: string;
  credentialId: string;
  cliPath: string;
}
export interface Catalog {
  version: number;
  revision: number;
  credentials: Credential[];
  hosts: Host[];
  conversationHosts?: Host[];
  resources: Resource[];
  aiProfiles: AIProfile[];
  defaultAIProfileId: string;
  pendingDeletes?: string[];
  recentConnections?: { hostId: string; openedAt: string }[];
}
export interface Session {
  id: string;
  hostId: string;
  conversationId: string;
  name: string;
  address: string;
  port: number;
  user: string;
  createdAt: string;
  endedAt: string;
  status: 'starting' | 'running' | 'ended' | 'failed';
}
export interface Conversation {
  aiModel?: string;
  aiProfileId?: string;
  codexThreadId?: string;
  id: string;
  hostId: string;
  title: string;
  preview: string;
  startedAt: string;
  updatedAt: string;
  messageCount: number;
}
export interface ChatMessage {
  role: string;
  content: string;
  time: string;
  proposal?: {
    id?: string;
    command: string;
    explanation: string;
    expectedEffect?: string;
    risk: string;
  };
  activity?: { id: string; kind: string; title: string; status: string; detail?: string };
}
export interface MonitorPanel {
  widget?: WidgetMeta;
  command?: string;
  collecting?: boolean;
  alert?: MonitorCondition;
  id: string;
  title: string;
  kind: string;
  content: string;
  status: string;
  error: string;
  updatedAt: string;
  intervalMs: number;
  pinned: boolean;
}
export interface WorkbenchState {
  terminalTakeover?: boolean;
  systemCollection: boolean;
  bastionCompatibility: boolean;
  model?: string;
  modelOptions?: string[];
  codexThreadId?: string;
  workspace: string;
  workspaceSupported: boolean;
  workspaceWritable: boolean;
  widgetCandidates?: WidgetCandidate[];
  conversationId: string;
  messages: ChatMessage[];
  busy: boolean;
  status: string;
  proposal?: {
    id: string;
    command: string;
    explanation: string;
    expectedEffect: string;
    risk: string;
  };
  canApprove: boolean;
  approvalStatus: string;
  executing: boolean;
  canCollectOutput: boolean;
  panels: MonitorPanel[];
  alertMonitors?: MonitorPanel[];
  updatedAt: string;
}
export interface WorkbenchCommand {
  kind:
    | 'execution-collect'
    | 'terminal-takeover'
    | 'bastion-compatibility'
    | 'system-collection'
    | 'model'
    | 'prompt'
    | 'workspace'
    | 'approve'
    | 'reject'
    | 'monitor-open'
    | 'monitor-close'
    | 'monitor-refresh'
    | 'alert-close'
    | 'alert-refresh'
    | 'widget-cancel'
    | 'widget-ready'
    | 'widget-failed';
  candidateId?: string;
  text?: string;
  proposalId?: string;
  panelId?: string;
}
export const sessionAddress = (s: Pick<Session, 'user' | 'address' | 'port'>) =>
  (s.user ? s.user + '@' : '') +
  (s.address.includes(':') ? '[' + s.address + ']' : s.address) +
  ':' +
  s.port;
export const sessionLive = (s: Session) => s.status === 'starting' || s.status === 'running';
export const sessionStatus = {
  starting: '正在启动',
  running: '运行中',
  ended: '已结束',
  failed: '异常结束',
};
export interface HostCheck {
  hostId: string;
  name: string;
  address: string;
  known: boolean;
  keys: { algorithm: string; fingerprint: string }[];
  token: string;
}
export interface Operation {
  operationId?: string;
  operationRevision?: number;
  arguments?: Record<string, unknown>;
  resourceId: string;
  action: string;
  path?: string;
  query?: string;
  args?: string[];
}
export interface CreationReceipt {
  id: string;
  title: string;
  kind: 'knowledge' | 'system';
  status: string;
  row?: number;
  detail: string;
}
type Backend = ExternalBackend & {
  GetMonitoring(): Promise<MonitoringState>;
  SaveAlertPolicy(policy: AlertPolicy): Promise<void>;
  AcknowledgeAlert(id: string): Promise<void>;
  SnoozeAlerts(minutes: number): Promise<void>;
  SetPetEnabled(enabled: boolean): Promise<void>;
  SetPetStyle(style: AlertPolicy['petStyle']): Promise<void>;
  OpenMonitorSession(id: string): Promise<void>;
  ReadPetSession(id: string, updatedAt: string): Promise<void>;
  AskCatalogAssistant(input: {
    mode: 'knowledge' | 'system';
    conversationId: string;
    prompt: string;
  }): Promise<{ messages: ChatMessage[]; receipts: CreationReceipt[]; error: string }>;
  ListKnowledge(query: string): Promise<Knowledge[]>;
  SaveKnowledge(item: Knowledge): Promise<Knowledge>;
  DeleteKnowledge(id: string, updatedAt: string): Promise<void>;
  ParseKnowledgeTable(text: string): Promise<Knowledge>;
  PickKnowledgeTable(): Promise<Knowledge | null>;
  OpenKnowledgeSource(id: string): Promise<void>;
  PickKnowledgeDocument(): Promise<Knowledge | null>;
  OpenKnowledgeDocument(id: string): Promise<void>;
  SummarizeConversation(hostId: string, id: string): Promise<Knowledge>;
  Snapshot(): Promise<Catalog>;
  SaveCredential(input: {
    credential: Credential;
    secret: string;
    clearSecret: boolean;
  }): Promise<Credential>;
  SaveHost(host: Host): Promise<Host>;
  SaveResource(resource: Resource): Promise<Resource>;
  SaveAIProfile(profile: AIProfile, makeDefault: boolean): Promise<AIProfile>;
  Delete(kind: string, id: string): Promise<void>;
  CleanupSecrets(): Promise<void>;
  PickKeyFile(): Promise<string>;
  PickWorkspace(): Promise<string>;
  ImportSSH(): Promise<Host[]>;
  CheckHost(id: string): Promise<HostCheck>;
  TrustHost(token: string): Promise<void>;
  TestHost(id: string): Promise<void>;
  CreateSession(id: string): Promise<Session>;
  ListConversations(): Promise<Conversation[]>;
  CreateConversation(hostId: string): Promise<Conversation>;
  CreateConversationWithWorkspace(hostId: string, workspace: string): Promise<Conversation>;
  CreateConversationWithAI(
    hostId: string,
    workspace: string,
    aiProfileId: string,
  ): Promise<Conversation>;
  CreateDirectConversationWithAI(
    host: Host,
    saveHost: boolean,
    aiProfileId: string,
  ): Promise<{ host: Host; conversation: Conversation }>;
  CreateDirectConversation(
    host: Host,
    saveHost: boolean,
  ): Promise<{ host: Host; conversation: Conversation }>;
  GetConversation(hostId: string, id: string): Promise<ChatMessage[]>;
  OpenConversationSession(hostId: string, id: string): Promise<Session>;
  ReconnectSession(id: string): Promise<Session>;
  ListSessions(): Promise<Session[]>;
  ClearRecentConnections(): Promise<void>;
  StartSession(id: string, cols: number, rows: number): Promise<void>;
  WriteTerminal(id: string, data: string): Promise<void>;
  ResizeTerminal(id: string, cols: number, rows: number): Promise<void>;
  GetWidgetArtifact(id: string, hash: string): Promise<WidgetArtifact>;
  GetWidgetData(id: string, panelId: string, bindingId: string): Promise<WidgetData>;
  GetSavedWidgets(id: string): Promise<SavedWidget[]>;
  RestoreSavedWidget(id: string, key: string, version: number, panelId: string): Promise<void>;
  RetryWidgetSave(id: string, panelId: string): Promise<void>;
  GetWorkbench(id: string): Promise<WorkbenchState | null>;
  WorkbenchAction(id: string, command: WorkbenchCommand): Promise<void>;
  CloseSession(id: string): Promise<void>;
  RunResource(op: Operation): Promise<string>;
  TestResource(id: string): Promise<string>;
  ExploreResource(id: string): Promise<ResourceDiscovery>;
  ResolveCredentialRequest(id: string, accepted: boolean): Promise<void>;
};
declare global {
  interface Window {
    go?: { desktop: { Service: Backend; PetBridge?: PetBackend } };
    runtime?: {
      EventsOn(name: string, callback: (...args: any[]) => void): () => void;
      BrowserOpenURL?(url: string): void;
    };
  }
}
export const connected = () => !!window.go?.desktop?.Service;
export const api: Backend = new Proxy({} as Backend, {
  get:
    (_, key) =>
    (...args: unknown[]) => {
      const backend = window.go?.desktop?.Service;
      if (!backend)
        return Promise.reject(
          new Error('请从 WRD AI SSH 桌面客户端打开此界面。浏览器预览不连接系统凭据库。'),
        );
      return (backend[key as keyof Backend] as (...args: unknown[]) => unknown)(...args);
    },
});
export const emptyCatalog: Catalog = {
  version: 1,
  revision: 0,
  hosts: [],
  credentials: [],
  resources: [],
  aiProfiles: [],
  defaultAIProfileId: '',
};
export const allHosts = (data: Catalog) => [...data.hosts, ...(data.conversationHosts || [])];
export function defaultHostName(host: Host, data: Catalog): string {
  const address = host.address.trim();
  if (!address) return '';
  const user =
    host.user.trim() ||
    data.credentials.find((credential) => credential.id === host.credentialId)?.username.trim() ||
    '';
  const endpoint = `${address.includes(':') ? `[${address}]` : address}:${host.port || 22}`;
  return user ? `${user}@${endpoint}` : endpoint;
}
export const newHost = (): Host => ({
  id: '',
  name: '',
  address: '',
  port: 22,
  user: '',
  group: '',
  credentialId: '',
  jumpHostId: '',
  aiProfileId: '',
  workspace: '',
});
export const newCredential = (): Credential => ({
  id: '',
  name: '',
  kind: 'password',
  username: '',
  identityFile: '',
  hasSecret: false,
  updatedAt: '',
});
export const newResource = (): Resource => ({
  id: '',
  name: '',
  kind: 'http',
  endpoint: '',
  database: '',
  credentialId: '',
  hostId: '',
  route: 'local',
  allowLocal: true,
  grant: 'ask',
  tls: true,
});
export const newAI = (): AIProfile => ({
  id: '',
  name: '',
  provider: 'codex',
  url: '',
  model: '',
  credentialId: '',
  cliPath: '',
});
export const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e));

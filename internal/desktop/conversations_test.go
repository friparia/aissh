package desktop

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.example.com/ai/wrd-ai-cli/internal/agent"
	"git.example.com/ai/wrd-ai-cli/internal/catalog"
	"git.example.com/ai/wrd-ai-cli/internal/credentialui"
	"git.example.com/ai/wrd-ai-cli/internal/history"
)

func conversationService(t *testing.T) (*Service, catalog.Host, catalog.Host) {
	t.Helper()
	t.Setenv("AISSH_HISTORY_DIR", t.TempDir())
	t.Setenv("AISSH_HISTORY", "1")
	cli := filepath.Join(t.TempDir(), "aissh")
	if err := os.WriteFile(cli, []byte("test executable"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AISSH_CLI_PATH", cli)
	s := New(catalog.New(t.TempDir(), memorySecrets{}))
	a, err := s.SaveHost(catalog.Host{Name: "A", Address: "server-a", Port: 22})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.SaveHost(catalog.Host{Name: "B", Address: "server-b", Port: 22})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { Shutdown(s) })
	return s, a, b
}

func TestConversationsPersistWithoutSSHAndStayBoundToHost(t *testing.T) {
	s, a, b := conversationService(t)
	first, err := s.CreateConversation(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateConversation(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID || first.MessageCount != 0 || len(s.ListSessions()) != 0 {
		t.Fatal("new conversation created a terminal or reused context")
	}
	if _, err := s.GetConversation(b.ID, first.ID); err == nil {
		t.Fatal("cross-host history exposed")
	}
	if _, err := s.OpenConversationSession(b.ID, first.ID); err == nil {
		t.Fatal("cross-host conversation opened")
	}
	if _, err := s.CreateConversation("missing"); err == nil {
		t.Fatal("unknown host accepted")
	}
	if err := s.history.Append("profile:"+a.ID, history.Message{ConversationID: first.ID, Role: "user", Content: "检查 nginx", Time: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	restarted := New(s.store)
	items, err := restarted.ListConversations()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].ID != first.ID || items[0].Title != "检查 nginx" || items[0].MessageCount != 1 {
		t.Fatalf("history after restart: %+v", items)
	}
	if _, err := s.OpenConversationSession(a.ID, "missing"); err == nil {
		t.Fatal("missing conversation opened")
	}
}

func TestConversationReusesLiveSessionAndReconnectsWithContext(t *testing.T) {
	s, a, _ := conversationService(t)
	c, err := s.CreateConversation(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.OpenConversationSession(a.ID, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.OpenConversationSession(a.ID, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != again.ID || len(s.ListSessions()) != 1 {
		t.Fatal("duplicated live workbench")
	}
	s.sessions[first.ID].finish("ended")
	reconnected, err := s.ReconnectSession(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reconnected.ID == first.ID || reconnected.ConversationID != c.ID {
		t.Fatalf("lost context: %+v", reconnected)
	}
	if err := s.CloseSession(reconnected.ID); err != nil {
		t.Fatal(err)
	}
	if messages, err := s.GetConversation(a.ID, c.ID); err != nil || len(messages) == 0 {
		t.Fatal("closing SSH removed history")
	}
	// A CLI may announce its new ID before sending any persistent AI message.
	plain, err := s.CreateSession(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	s.sessions[plain.ID].info.ConversationID = "unsaved-cli-draft"
	s.sessions[plain.ID].finish("ended")
	if _, err := s.ReconnectSession(plain.ID); err != nil {
		t.Fatalf("empty SSH session cannot reconnect: %v", err)
	}
}

func TestConversationHistoryDisabled(t *testing.T) {
	s, a, _ := conversationService(t)
	t.Setenv("AISSH_HISTORY", "off")
	s = New(s.store)
	if _, err := s.CreateConversation(a.ID); err == nil {
		t.Fatal("disabled persistence accepted a conversation")
	}
	if items, err := s.ListConversations(); err != nil || len(items) != 0 {
		t.Fatalf("disabled history: %+v %v", items, err)
	}
}

func TestNewConversationWorkspaceDoesNotChangeHostDefault(t *testing.T) {
	s, host, _ := conversationService(t)
	host.Workspace = t.TempDir()
	if _, err := s.SaveHost(host); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	for _, directory := range []string{workspace, ""} {
		created, err := s.CreateConversationWithWorkspace(host.ID, directory)
		if err != nil {
			t.Fatal(err)
		}
		conversation, err := s.conversation(host.ID, created.ID)
		if directory != "" {
			directory, _ = filepath.EvalSymlinks(directory)
		}
		if err != nil || conversation.Workspace == nil || *conversation.Workspace != directory {
			t.Fatalf("directory not saved: %+v %v", conversation, err)
		}
	}
	data, _ := s.store.Load()
	saved, _ := data.Host(host.ID)
	if saved.Workspace != host.Workspace {
		t.Fatal("conversation changed host default")
	}
	before, _ := s.ListConversations()
	if _, err := s.CreateConversationWithWorkspace(host.ID, filepath.Join(workspace, "missing")); err == nil {
		t.Fatal("invalid directory accepted")
	}
	after, _ := s.ListConversations()
	if len(after) != len(before) {
		t.Fatal("invalid directory created a conversation")
	}
	created, err := s.CreateDirectConversation(catalog.Host{Address: "direct.example", Port: 22, Workspace: workspace}, false)
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := s.conversation(created.Host.ID, created.Conversation.ID)
	resolved, _ := filepath.EvalSymlinks(workspace)
	if err != nil || conversation.Workspace == nil || *conversation.Workspace != resolved {
		t.Fatalf("direct directory lost: %+v %v", conversation, err)
	}
}

func TestDirectConversationCanStayOutsideHostLibrary(t *testing.T) {
	for _, saveHost := range []bool{false, true} {
		t.Run(fmt.Sprint(saveHost), func(t *testing.T) {
			s, original, _ := conversationService(t)
			created, err := s.CreateDirectConversation(catalog.Host{ID: original.ID, Address: " direct.example ", Port: 2222, User: "ops"}, saveHost)
			if err != nil {
				t.Fatal(err)
			}
			if created.Host.ID == original.ID || created.Host.Name != "ops@direct.example:2222" || created.Conversation.HostID != created.Host.ID {
				t.Fatalf("bad target: %+v", created)
			}
			d, err := s.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			if saveHost && (len(d.Hosts) != 3 || len(d.ConversationHosts) != 0) {
				t.Fatal("target not added to saved hosts")
			}
			if !saveHost && (len(d.Hosts) != 2 || len(d.ConversationHosts) != 1) {
				t.Fatal("unsaved target entered host library")
			}
			restarted := New(s.store)
			t.Cleanup(func() { Shutdown(restarted) })
			items, err := restarted.ListConversations()
			if err != nil || len(items) != 1 || items[0].ID != created.Conversation.ID {
				t.Fatalf("history after restart: %+v, %v", items, err)
			}
			opened, err := restarted.OpenConversationSession(created.Host.ID, created.Conversation.ID)
			if err != nil || opened.Address != "direct.example" || opened.User != "ops" || opened.Port != 2222 {
				t.Fatalf("wrong connection: %+v %v", opened, err)
			}
			if !saveHost {
				if _, err := s.SaveHost(created.Host); err != nil {
					t.Fatal(err)
				}
				d, _ = s.Snapshot()
				if len(d.Hosts) != 3 || len(d.ConversationHosts) != 0 {
					t.Fatal("promotion duplicated target")
				}
				if _, err := s.GetConversation(created.Host.ID, created.Conversation.ID); err != nil {
					t.Fatal("promotion lost history", err)
				}
			}
		})
	}
}

func TestDirectConversationValidatesAndRollsBackFailedHistory(t *testing.T) {
	s, _, _ := conversationService(t)
	for _, host := range []catalog.Host{
		{Address: "-oProxyCommand=bad"}, {Address: "server", Port: 70000},
		{Address: "server", CredentialID: "missing"}, {Address: "server", JumpHostID: "missing"},
	} {
		if _, err := s.CreateDirectConversation(host, false); err == nil {
			t.Fatalf("invalid target accepted: %+v", host)
		}
	}
	path := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(path, []byte("occupied"), 0600); err != nil {
		t.Fatal(err)
	}
	s.history = history.New(path)
	for _, save := range []bool{false, true} {
		if _, err := s.CreateDirectConversation(catalog.Host{Address: "server"}, save); err == nil {
			t.Fatal("ignored history failure")
		}
	}
	d, err := s.Snapshot()
	if err != nil || len(d.Hosts) != 2 || len(d.ConversationHosts) != 0 {
		t.Fatalf("failed creation left targets: %+v %v", d, err)
	}
}

func TestCLIConversationSwitchUpdatesDesktopAssociation(t *testing.T) {
	s, a, _ := conversationService(t)
	info, err := s.CreateSession(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	session, _ := s.session(info.ID)
	env, closeChannel, err := s.credentialChannel(session)
	if err != nil {
		t.Fatal(err)
	}
	defer closeChannel()
	for _, value := range env {
		key, value, _ := strings.Cut(value, "=")
		t.Setenv(key, value)
	}
	updates := make(chan SessionInfo, 1)
	s.emit = func(name string, args ...any) {
		if name == "session-updated" {
			updates <- args[0].(SessionInfo)
		}
	}
	credentialui.NotifyConversation("selected-in-cli")
	select {
	case update := <-updates:
		if update.ID != info.ID || update.HostID != a.ID || update.ConversationID != "selected-in-cli" {
			t.Fatalf("incorrect association: %+v", update)
		}
	case <-time.After(time.Second):
		t.Fatal("CLI switch did not reach desktop")
	}
	if s.ListSessions()[0].ConversationID != "selected-in-cli" {
		t.Fatal("session list retained stale conversation")
	}
}

func TestConversationAISelectionPersistsAndOverridesHostDefault(t *testing.T) {
	s, host, _ := conversationService(t)
	first, err := s.SaveAIProfile(catalog.AIProfile{Name: "host model", Provider: "model", URL: "http://models.internal/v1", Model: "first"}, true)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.SaveAIProfile(catalog.AIProfile{Name: "conversation model", Provider: "model", URL: "http://other.internal/v1", Model: "second", Models: []string{"third"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	host.AIProfileID = first.ID
	host.Workspace = t.TempDir()
	if _, err := s.SaveHost(host); err != nil {
		t.Fatal(err)
	}
	for _, selected := range []string{second.ID, catalog.BuiltinCodexProfileID, ""} {
		workspace := host.Workspace
		if selected == second.ID {
			workspace = filepath.Join(host.Workspace, "missing")
		}
		created, err := s.CreateConversationWithAI(host.ID, workspace, selected)
		if err != nil {
			t.Fatal(err)
		}
		expected := selected
		if expected == "" {
			expected = first.ID
		}
		restarted := New(s.store)
		t.Cleanup(func() { Shutdown(restarted) })
		conversation, err := restarted.conversation(host.ID, created.ID)
		if err != nil || conversation.AIProfileID != expected {
			t.Fatalf("conversation=%+v err=%v", conversation, err)
		}
		if expected != catalog.BuiltinCodexProfileID && (conversation.Workspace == nil || *conversation.Workspace != "") {
			t.Fatal("API model retained local workspace")
		}
		opened, err := restarted.OpenConversationSession(host.ID, created.ID)
		if err != nil {
			t.Fatal(err)
		}
		session, err := restarted.session(opened.ID)
		if err != nil || session.aiProfileID != expected {
			t.Fatalf("selected session profile=%q err=%v", session.aiProfileID, err)
		}
		session.finish("ended")
		reconnected, err := restarted.ReconnectSession(opened.ID)
		if err != nil {
			t.Fatal(err)
		}
		session, err = restarted.session(reconnected.ID)
		if err != nil || session.aiProfileID != expected {
			t.Fatal("reconnect changed AI profile")
		}
	}
	data, _ := s.store.Load()
	saved, _ := data.Host(host.ID)
	if saved.AIProfileID != first.ID || saved.Workspace != host.Workspace {
		t.Fatal("new conversation changed host defaults")
	}
	before, _ := s.ListConversations()
	if _, err := s.CreateConversationWithAI(host.ID, "", "missing"); err == nil {
		t.Fatal("unknown AI profile accepted")
	}
	after, _ := s.ListConversations()
	if len(before) != len(after) {
		t.Fatal("invalid AI choice created conversation")
	}
	direct, err := s.CreateDirectConversationWithAI(catalog.Host{Address: "direct.example", Port: 22}, false, second.ID)
	if err != nil || direct.Conversation.AIProfileID != second.ID {
		t.Fatalf("direct=%+v err=%v", direct, err)
	}
	provider, _, err := s.knowledgeProviderWithModel(host.ID, second.ID, "third")
	if err != nil {
		t.Fatal(err)
	}
	if provider.(*agent.DirectModelProvider).CurrentModel() != "third" {
		t.Fatal("conversation summary did not use selected model")
	}
}

func TestConversationCredentialsFollowSelectedAIProfile(t *testing.T) {
	s, host, _ := conversationService(t)
	profiles := make([]catalog.AIProfile, 2)
	credentials := make([]catalog.Credential, 2)
	for i := range profiles {
		credential, err := s.SaveCredential(catalog.CredentialInput{Credential: catalog.Credential{Name: fmt.Sprintf("key-%d", i), Kind: "token"}, Secret: fmt.Sprintf("token-%d", i)})
		if err != nil {
			t.Fatal(err)
		}
		credentials[i] = credential
		profile, err := s.SaveAIProfile(catalog.AIProfile{Name: fmt.Sprintf("model-%d", i), Provider: "model", URL: "http://models.internal/v1", Model: "test", CredentialID: credential.ID}, i == 0)
		if err != nil {
			t.Fatal(err)
		}
		profiles[i] = profile
	}
	if !s.boundCredentialForProfile(host.ID, credentials[1].ID, profiles[1].ID) {
		t.Fatal("selected AI credential was not authorized")
	}
	if s.boundCredentialForProfile(host.ID, credentials[0].ID, profiles[1].ID) {
		t.Fatal("default model credential leaked into selected conversation")
	}
	if s.boundCredentialForProfile(host.ID, credentials[1].ID, catalog.BuiltinCodexProfileID) {
		t.Fatal("builtin Codex requested model API credential")
	}
}

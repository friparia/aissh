package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"git.example.com/ai/wrd-ai-cli/internal/app"
	"git.example.com/ai/wrd-ai-cli/internal/catalog"
	"git.example.com/ai/wrd-ai-cli/internal/credentialui"
	"git.example.com/ai/wrd-ai-cli/internal/redact"
	"git.example.com/ai/wrd-ai-cli/internal/resources"
	"git.example.com/ai/wrd-ai-cli/internal/sshauth"
)

func prepareManaged(config *app.Config) (func(), error) {
	if credentialui.Available() {
		config.DesktopUI = true
		config.ConversationChanged = credentialui.NotifyConversation
	}
	store := catalog.Default()
	filter := redact.New(config.ModelAPIKey, config.AgentToken)
	config.SecretFilter = filter
	data, err := store.Load()
	if err != nil {
		return nil, err
	}
	profileID := config.AIProfileID
	close := func() {}
	if config.ProfileID != "" {
		h, err := data.Host(config.ProfileID)
		if err != nil {
			return nil, err
		}
		if len(config.SSHArgs) > 0 {
			return nil, errors.New("--profile 不能同时覆盖 SSH 地址或连接参数；请在主机配置中修改")
		}
		if config.SSHPath != "ssh" {
			return nil, errors.New("保存凭据的连接使用系统 ssh，不接受 --ssh-path 覆盖")
		}
		if err := trustInTerminal(store, h.ID); err != nil {
			return nil, err
		}
		lease, err := sshauth.Prepare(context.Background(), store, h.ID, "", filter)
		if err != nil {
			return nil, err
		}
		close = lease.Close
		config.SSHArgs = lease.Args
		config.SSHEnvironment = lease.Env
		config.HostLabel = h.Name
		config.HistoryTarget = "profile:" + h.ID
		if h.Workspace != "" {
			config.Workspace = h.Workspace
		}
		if profileID == "" {
			profileID = h.AIProfileID
		}
	}
	if profileID == "" {
		profileID = data.DefaultAIProfileID
	}
	config.AIProfileID = profileID
	if profileID == catalog.BuiltinCodexProfileID {
		config.AgentProvider = "codex"
	} else if profileID != "" {
		p, err := data.AIProfile(profileID)
		if err != nil {
			close()
			return nil, err
		}
		// A saved key is inseparable from its saved endpoint. CLI URL overrides
		// must not redirect that key to another server.
		config.AgentProvider = p.Provider
		config.ModelURL = p.URL
		config.ModelName = p.Model
		if p.Provider == "model" {
			config.ModelOptions = p.ModelChoices()
		}
		config.ModelAPIKey = ""
		config.AgentToken = ""
		config.AgentURL = p.URL
		config.CodexModel = p.Model
		if !config.KeepCodexSessionSet {
			config.KeepCodexSession = p.KeepCodexSession
		}
		config.ClaudeModel = p.Model
		if p.CLIPath != "" {
			if p.Provider == "codex" {
				config.CodexPath = p.CLIPath
			} else if p.Provider == "claude" {
				config.ClaudePath = p.CLIPath
			}
		}
		if p.CredentialID != "" {
			c, err := data.Credential(p.CredentialID)
			if err != nil {
				close()
				return nil, err
			}
			value, err := store.Secret(c)
			if err != nil {
				close()
				return nil, err
			}
			filter.Add(value)
			if p.Provider == "http" {
				config.AgentToken = value
			} else {
				config.ModelAPIKey = value
			}
		}
	}
	config.Resources = resources.New(store, config.ProfileID, "", filter)
	return close, nil
}

func trustInTerminal(store *catalog.Store, id string) error {
	reader := bufio.NewReader(os.Stdin)
	for {
		check, err := sshauth.CheckChain(context.Background(), store, id, "")
		if err != nil {
			return err
		}
		if check.Known {
			return nil
		}
		fmt.Fprintf(os.Stderr, "\n首次连接 %s (%s)，请与管理员核对 SSH 主机指纹：\n", check.Name, check.Address)
		for _, key := range check.Keys {
			fmt.Fprintf(os.Stderr, "  %s  %s\n", key.Algorithm, key.Fingerprint)
		}
		fmt.Fprint(os.Stderr, "核对后输入 yes 保存并继续：")
		line, err := reader.ReadString('\n')
		if err != nil || strings.TrimSpace(line) != "yes" {
			return errors.New("已取消主机信任")
		}
		if err := sshauth.Trust(store, check); err != nil {
			return err
		}
	}
}

// resourceCLI exposes only saved local read grants to local tools. Interactive
// approval remains in the desktop or the current aissh session.
func resourceCLI(args []string) error {
	if len(args) != 1 {
		return errors.New("用法：aissh --resource '{\"resourceId\":\"ID\",\"action\":\"system.describe\"}'")
	}
	op, err := resources.Parse(resources.Prefix + args[0])
	if err != nil {
		return err
	}
	executor := resources.New(catalog.Default(), "", "", nil)
	needs, err := executor.RequiresApproval(op)
	if err != nil {
		return err
	}
	if needs {
		return errors.New("请在桌面端预览并确认此请求，或为此确切接口版本设置本机低风险只读授权")
	}
	result, err := executor.Execute(context.Background(), op, false)
	if err != nil {
		return err
	}
	fmt.Println(result)
	return nil
}

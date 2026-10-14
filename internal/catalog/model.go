// Package catalog owns the shared desktop/CLI configuration. It stores only
// metadata; passwords and tokens live in a separate SecretStore.
package catalog

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

type Credential struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Kind         string `json:"kind"` // password, key, token, agent
	Username     string `json:"username"`
	IdentityFile string `json:"identityFile"`
	SecretRef    string `json:"secretRef,omitempty"`
	HasSecret    bool   `json:"hasSecret"`
	UpdatedAt    string `json:"updatedAt"`
}

type Host struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Address      string `json:"address"`
	Port         int    `json:"port"`
	User         string `json:"user"`
	Group        string `json:"group"`
	CredentialID string `json:"credentialId"`
	JumpHostID   string `json:"jumpHostId"`
	AIProfileID  string `json:"aiProfileId"`
	Workspace    string `json:"workspace"`
}

type Resource struct {
	SystemID      string             `json:"systemId,omitempty"`
	SystemName    string             `json:"systemName,omitempty"`
	Environment   string             `json:"environment,omitempty"`
	Managed       bool               `json:"managed"` // backend-owned policy gate
	ID            string             `json:"id"`
	Name          string             `json:"name"`
	Kind          string             `json:"kind"` // http, postgres, mysql, redis
	Endpoint      string             `json:"endpoint"`
	Database      string             `json:"database"`
	CredentialID  string             `json:"credentialId"`
	HostID        string             `json:"hostId"` // SSH session this resource belongs to
	Route         string             `json:"route"`  // local or ssh
	AllowLocal    bool               `json:"allowLocal"`
	Grant         string             `json:"grant"` // ask, session, read
	TLS           bool               `json:"tls"`
	Description   string             `json:"description,omitempty"`
	DiscoveryPath string             `json:"discoveryPath,omitempty"` // optional OpenAPI JSON path inside Endpoint
	Discovery     *ResourceDiscovery `json:"discovery,omitempty"`
}

// BuiltinCodexProfileID selects local Codex independently of saved defaults.
const BuiltinCodexProfileID = "builtin-codex"

type AIProfile struct {
	KeepCodexSession bool     `json:"keepCodexSession,omitempty"`
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Provider         string   `json:"provider"`
	URL              string   `json:"url"`
	Model            string   `json:"model"`
	Models           []string `json:"models,omitempty"`
	CredentialID     string   `json:"credentialId"`
	CLIPath          string   `json:"cliPath"`
}

// ModelChoices includes the default for profiles saved before multiple models.
func (p AIProfile) ModelChoices() []string {
	choices := []string{}
	for _, model := range append([]string{p.Model}, p.Models...) {
		model = strings.TrimSpace(model)
		if model != "" && !slices.Contains(choices, model) {
			choices = append(choices, model)
		}
	}
	return choices
}

type Data struct {
	Version            int                `json:"version"`
	Revision           uint64             `json:"revision"`
	Credentials        []Credential       `json:"credentials"`
	Hosts              []Host             `json:"hosts"`
	ConversationHosts  []Host             `json:"conversationHosts,omitempty"`
	Resources          []Resource         `json:"resources"`
	AIProfiles         []AIProfile        `json:"aiProfiles"`
	DefaultAIProfileID string             `json:"defaultAIProfileId"`
	PendingDeletes     []string           `json:"pendingDeletes,omitempty"`
	RecentConnections  []RecentConnection `json:"recentConnections,omitempty"`
}

// RecentConnection retains only a host reference and the last opening time.
// Terminal contents, AI conversations and secrets are never stored here.
type RecentConnection struct {
	HostID   string `json:"hostId"`
	OpenedAt string `json:"openedAt"`
}

func emptyData() Data {
	return Data{Version: 1, Credentials: []Credential{}, Hosts: []Host{}, Resources: []Resource{}, AIProfiles: []AIProfile{}}
}
func DefaultDir() string {
	if dir := os.Getenv("AISSH_CONFIG_DIR"); dir != "" {
		return dir
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "aissh")
}
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

var validID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
var validAddress = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.:%_-]*$`)

func ValidateID(id string) error {
	if !validID.MatchString(id) {
		return errors.New("无效的配置 ID")
	}
	return nil
}
func textField(s string) bool { return len(s) <= 1024 && !strings.ContainsAny(s, "\x00\r\n") }

func (d Data) Credential(id string) (Credential, error) {
	for _, c := range d.Credentials {
		if c.ID == id {
			return c, nil
		}
	}
	return Credential{}, errors.New("凭据不存在，请在桌面端配置")
}
func (d Data) Host(id string) (Host, error) {
	for _, h := range d.AllHosts() {
		if h.ID == id {
			return h, nil
		}
	}
	return Host{}, errors.New("主机配置不存在")
}

// AllHosts includes connection targets retained by conversations without
// adding them to the user's saved host library.
func (d Data) AllHosts() []Host {
	result := make([]Host, 0, len(d.Hosts)+len(d.ConversationHosts))
	result = append(result, d.Hosts...)
	return append(result, d.ConversationHosts...)
}
func (d Data) Resource(id string) (Resource, error) {
	for _, r := range d.Resources {
		if r.ID == id {
			return r, nil
		}
	}
	return Resource{}, errors.New("系统连接不存在")
}
func (d Data) AIProfile(id string) (AIProfile, error) {
	for _, p := range d.AIProfiles {
		if p.ID == id {
			return p, nil
		}
	}
	return AIProfile{}, errors.New("AI 配置不存在")
}

func (d Data) validate() error {
	ids := map[string]bool{}
	check := func(id, name string) error {
		if err := ValidateID(id); err != nil {
			return err
		}
		if ids[id] {
			return errors.New("配置 ID 重复")
		}
		ids[id] = true
		if strings.TrimSpace(name) == "" || !textField(name) {
			return errors.New("名称不能为空，也不能包含换行")
		}
		return nil
	}
	for _, c := range d.Credentials {
		if err := check(c.ID, c.Name); err != nil {
			return err
		}
		if !textField(c.Username) || !textField(c.IdentityFile) {
			return errors.New("账号或密钥路径包含无效字符")
		}
		switch c.Kind {
		case "password", "token":
			if !c.HasSecret || c.SecretRef == "" {
				return errors.New("请填写密码或 Token")
			}
		case "key":
			if c.IdentityFile == "" {
				return errors.New("请选择私钥文件")
			}
		case "agent":
			if c.HasSecret {
				return errors.New("SSH Agent 不保存密码")
			}
		default:
			return errors.New("不支持的凭据类型")
		}
		if c.SecretRef != "" {
			if err := ValidateID(c.SecretRef); err != nil {
				return err
			}
		}
	}
	for _, h := range d.AllHosts() {
		if err := check(h.ID, h.Name); err != nil {
			return err
		}
		if !validAddress.MatchString(h.Address) || strings.HasPrefix(h.Address, "-") {
			return errors.New("请输入有效的主机名或 IP 地址")
		}
		if h.Port < 1 || h.Port > 65535 || !textField(h.User) || strings.ContainsAny(h.User, " @\t\"\\") || strings.HasPrefix(h.User, "-") {
			return errors.New("SSH 端口或用户名无效")
		}
		if !textField(h.Group) || !textField(h.Workspace) {
			return errors.New("分组或工作区无效")
		}
		if h.CredentialID != "" {
			c, err := d.Credential(h.CredentialID)
			if err != nil {
				return err
			}
			if c.Kind == "token" {
				return errors.New("SSH 登录不能使用 API Token")
			}
		}
		if h.AIProfileID != "" {
			if _, err := d.AIProfile(h.AIProfileID); err != nil {
				return err
			}
		}
		seen := map[string]bool{h.ID: true}
		next := h.JumpHostID
		for next != "" {
			if seen[next] {
				return errors.New("跳板机配置不能循环引用")
			}
			seen[next] = true
			jump, err := d.Host(next)
			if err != nil {
				return err
			}
			next = jump.JumpHostID
			if len(seen) > 8 {
				return errors.New("跳板机最多支持 7 层")
			}
		}
	}
	for _, r := range d.Resources {
		if err := check(r.ID, r.Name); err != nil {
			return err
		}
		if r.SystemID != "" {
			if err := ValidateID(r.SystemID); err != nil {
				return err
			}
		}
		if !textField(r.SystemName) || !textField(r.Environment) {
			return errors.New("系统名称或环境无效")
		}
		if len(r.Description) > 8192 || strings.ContainsRune(r.Description, 0) {
			return errors.New("系统说明不能超过 8 KB 或包含无效字符")
		}
		if !textField(r.DiscoveryPath) {
			return errors.New("接口文档路径无效")
		}
		if r.Kind != "http" && r.Kind != "postgres" && r.Kind != "mysql" && r.Kind != "redis" {
			return errors.New("不支持的系统类型")
		}
		if r.Route != "local" && r.Route != "ssh" {
			return errors.New("请选择本机直连或 SSH 隧道")
		}
		if r.Grant != "ask" && r.Grant != "session" && r.Grant != "read" {
			return errors.New("无效的凭据授权范围")
		}
		if r.HostID != "" {
			if _, err := d.Host(r.HostID); err != nil {
				return err
			}
		} else if r.Route == "ssh" {
			return errors.New("SSH 隧道需要选择关联主机")
		}
		if r.CredentialID != "" {
			c, err := d.Credential(r.CredentialID)
			if err != nil {
				return err
			}
			if c.Kind != "password" && c.Kind != "token" {
				return errors.New("系统连接需要密码或 Token 凭据")
			}
		}
		if !textField(r.Database) {
			return errors.New("数据库名无效")
		}
		if _, err := ResourceURL(r); err != nil {
			return err
		}
	}
	for _, p := range d.AIProfiles {
		if err := check(p.ID, p.Name); err != nil {
			return err
		}
		switch p.Provider {
		case "codex", "claude":
			if p.CredentialID != "" {
				return errors.New("本机 CLI 使用其自身登录；API Key 请使用直连模型模式")
			}
		case "model", "deepseek", "http":
			if _, err := AIServiceURL(p.URL); err != nil {
				return err
			}
		default:
			return errors.New("不支持的 AI 后端")
		}
		if !textField(p.Model) || !textField(p.CLIPath) || (p.CLIPath != "" && !filepath.IsAbs(p.CLIPath)) {
			return errors.New("模型名称无效，或 CLI 路径不是绝对路径")
		}
		if len(p.Models) > 64 || (len(p.Models) > 0 && p.Provider != "model") {
			return errors.New("仅兼容 OpenAI API 配置支持模型列表，最多 64 个模型")
		}
		for _, model := range p.Models {
			if strings.TrimSpace(model) == "" || !textField(model) {
				return errors.New("模型列表包含无效名称")
			}
		}
		if p.CredentialID != "" {
			c, err := d.Credential(p.CredentialID)
			if err != nil {
				return err
			}
			if c.Kind != "token" {
				return errors.New("AI 服务需要 Token 类型凭据")
			}
		}
	}
	if d.DefaultAIProfileID != "" {
		if _, err := d.AIProfile(d.DefaultAIProfileID); err != nil {
			return err
		}
	}
	return nil
}

func ServiceURL(value string) (*url.URL, error) {
	return serviceURL(value, false)
}

// AIServiceURL permits explicitly configured model and agent HTTP endpoints.
func AIServiceURL(value string) (*url.URL, error) {
	return serviceURL(value, true)
}

func serviceURL(value string, allowRemoteHTTP bool) (*url.URL, error) {
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, errors.New("请输入不含账号密码、查询参数的 HTTP(S) 地址")
	}
	if !allowRemoteHTTP && u.Scheme == "http" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1" {
		return nil, errors.New("非本机 HTTP 服务必须使用 HTTPS")
	}
	if u.Port() != "" {
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return nil, errors.New("服务端口必须在 1–65535 之间")
		}
	}
	return u, nil
}
func ResourceURL(r Resource) (*url.URL, error) {
	if r.Kind == "http" {
		return ServiceURL(r.Endpoint)
	}
	u, err := url.Parse("tcp://" + r.Endpoint)
	if err != nil || u.Hostname() == "" || u.Port() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("请输入 host:port 格式的系统地址")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New("系统端口必须在 1–65535 之间")
	}
	return u, nil
}

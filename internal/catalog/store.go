package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"git.example.com/ai/wrd-ai-cli/internal/credentialui"
	"github.com/gofrs/flock"
)

type Store struct {
	Dir     string
	Secrets SecretStore
}

func New(dir string, secrets SecretStore) *Store {
	if secrets == nil {
		secrets = OSSecrets{}
	}
	return &Store{Dir: dir, Secrets: secrets}
}
func Default() *Store         { return New(DefaultDir(), nil) }
func (s *Store) path() string { return filepath.Join(s.Dir, "connections.json") }
func (s *Store) Load() (Data, error) {
	data := emptyData()
	if s.Dir == "" {
		return data, errors.New("无法定位本机配置目录")
	}
	f, err := os.Open(s.path())
	if errors.Is(err, os.ErrNotExist) {
		return data, nil
	}
	if err != nil {
		return data, err
	}
	defer f.Close()
	decoder := json.NewDecoder(io.LimitReader(f, 4*1024*1024))
	if err := decoder.Decode(&data); err != nil {
		return data, errors.New("连接配置损坏；请从备份恢复 connections.json")
	}
	if data.Version != 1 && data.Version != 2 {
		return data, errors.New("不支持此连接配置版本，请升级 aissh")
	}
	if err := data.validate(); err != nil {
		return data, fmt.Errorf("连接配置无效：%w", err)
	}
	return data, nil
}
func (s *Store) mutate(fn func(*Data) error) error {
	return s.mutateData(fn, true)
}

func (s *Store) mutateData(fn func(*Data) error, bumpRevision bool) error {
	if s.Dir == "" {
		return errors.New("无法定位本机配置目录")
	}
	if err := os.MkdirAll(s.Dir, 0700); err != nil {
		return err
	}
	lock := flock.New(filepath.Join(s.Dir, "connections.lock"))
	if err := lock.Lock(); err != nil {
		return err
	}
	defer lock.Unlock()
	d, err := s.Load()
	if err != nil {
		return err
	}
	if err := fn(&d); err != nil {
		return err
	}
	if err := d.validate(); err != nil {
		return err
	}
	if bumpRevision {
		d.Revision++
	}
	f, err := os.CreateTemp(s.Dir, ".connections-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := f.Chmod(0600); err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(d); err != nil {
		return err
	}
	if info, err := f.Stat(); err != nil {
		return err
	} else if info.Size() > 4*1024*1024 {
		return errors.New("连接配置与操作目录超过 4 MB，请清理不再使用的系统")
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), s.path())
}

type CredentialInput struct {
	Credential  Credential `json:"credential"`
	Secret      string     `json:"secret"`
	ClearSecret bool       `json:"clearSecret"`
}

func (s *Store) SaveCredential(in CredentialInput) (Credential, error) {
	c := in.Credential
	c.Name = strings.TrimSpace(c.Name)
	if c.ID == "" {
		c.ID = NewID()
	}
	var newRef, oldRef string
	err := s.mutate(func(d *Data) error {
		old, _ := d.Credential(c.ID)
		oldRef = old.SecretRef
		c.SecretRef, c.HasSecret = old.SecretRef, old.HasSecret
		if in.ClearSecret || c.Kind == "agent" {
			c.SecretRef = ""
			c.HasSecret = false
		}
		if in.Secret != "" {
			c.SecretRef = NewID()
			c.HasSecret = true
			newRef = c.SecretRef
		}
		c.UpdatedAt = now()
		if oldRef != "" && oldRef != c.SecretRef {
			d.PendingDeletes = append(d.PendingDeletes, oldRef)
		}
		index := slices.IndexFunc(d.Credentials, func(v Credential) bool { return v.ID == c.ID })
		if index < 0 {
			d.Credentials = append(d.Credentials, c)
		} else {
			d.Credentials[index] = c
		}
		if err := d.validate(); err != nil {
			return err
		}
		if newRef != "" {
			return s.Secrets.Set(newRef, in.Secret)
		}
		return nil
	})
	if err != nil {
		if newRef != "" {
			_ = s.Secrets.Delete(newRef)
		}
		return Credential{}, err
	}
	// Metadata is committed before the old version is removed. A failed cleanup
	// leaves an unreachable old secret, never a broken live reference.
	if oldRef != "" && oldRef != c.SecretRef {
		_ = s.CleanupSecrets()
	}
	return c, nil
}
func (d Data) normalizeHost(h Host) Host {
	if h.Port == 0 {
		h.Port = 22
	}
	h.Name, h.Address = strings.TrimSpace(h.Name), strings.TrimSpace(h.Address)
	h.User = strings.TrimSpace(h.User)
	if h.Name == "" {
		user := h.User
		if user == "" && h.CredentialID != "" {
			credential, _ := d.Credential(h.CredentialID)
			user = strings.TrimSpace(credential.Username)
		}
		h.Name = net.JoinHostPort(h.Address, strconv.Itoa(h.Port))
		if user != "" {
			h.Name = user + "@" + h.Name
		}
	}
	return h
}

func (s *Store) SaveHost(h Host) (Host, error) {
	if h.ID == "" {
		h.ID = NewID()
	}
	err := s.mutate(func(d *Data) error {
		h = d.normalizeHost(h)
		i := slices.IndexFunc(d.Hosts, func(v Host) bool { return v.ID == h.ID })
		if i < 0 {
			d.Hosts = append(d.Hosts, h)
		} else {
			d.Hosts[i] = h
		}
		d.ConversationHosts = slices.DeleteFunc(d.ConversationHosts, func(v Host) bool { return v.ID == h.ID })
		return nil
	})
	return h, err
}

// CreateConversationHost always creates a new target. Frontend-supplied IDs
// cannot overwrite a saved host or redirect an existing conversation.
func (s *Store) CreateConversationHost(h Host, saveHost bool) (Host, error) {
	h.ID = NewID()
	if saveHost {
		return s.SaveHost(h)
	}
	err := s.mutate(func(d *Data) error {
		h = d.normalizeHost(h)
		d.ConversationHosts = append(d.ConversationHosts, h)
		return nil
	})
	return h, err
}
func (s *Store) SaveResource(r Resource) (Resource, error) {
	if r.ID == "" {
		r.ID = NewID()
	}
	if r.Route == "" {
		r.Route = "local"
	}
	if r.Grant == "" {
		r.Grant = "ask"
	}
	err := s.mutate(func(d *Data) error {
		i := slices.IndexFunc(d.Resources, func(v Resource) bool { return v.ID == r.ID })
		r.Managed = i >= 0 && d.Resources[i].Managed
		if r.SystemID == "" {
			r.SystemID = r.ID
		}
		if r.SystemName == "" {
			r.SystemName = r.Name
		}
		if r.Environment == "" {
			r.Environment = "production"
		}
		// Discovery is backend-owned; a form cannot forge operation classifications.
		r.Discovery = nil
		if i >= 0 && ResourceBinding(d.Resources[i], *d) == ResourceBinding(r, *d) {
			r.Discovery = d.Resources[i].Discovery
		}
		if i < 0 {
			d.Resources = append(d.Resources, r)
		} else {
			d.Resources[i] = r
		}
		return nil
	})
	return r, err
}
func (s *Store) SaveAIProfile(p AIProfile, makeDefault bool) (AIProfile, error) {
	if p.Provider == "model" && len(p.Models) > 0 {
		p.Model = strings.TrimSpace(p.Model)
		p.Models = p.ModelChoices()
		if p.Model == "" && len(p.Models) > 0 {
			p.Model = p.Models[0]
		}
	}
	if p.ID == "" {
		p.ID = NewID()
	}
	err := s.mutate(func(d *Data) error {
		i := slices.IndexFunc(d.AIProfiles, func(v AIProfile) bool { return v.ID == p.ID })
		if i < 0 {
			d.AIProfiles = append(d.AIProfiles, p)
		} else {
			d.AIProfiles[i] = p
		}
		if makeDefault {
			d.DefaultAIProfileID = p.ID
		} else if d.DefaultAIProfileID == p.ID {
			d.DefaultAIProfileID = ""
		}
		return nil
	})
	return p, err
}
func (s *Store) Delete(kind, id string) error {
	var secretRef string
	err := s.mutate(func(d *Data) error {
		switch kind {
		case "credential":
			c, err := d.Credential(id)
			if err != nil {
				return err
			}
			secretRef = c.SecretRef
			for _, h := range d.AllHosts() {
				if h.CredentialID == id {
					return fmt.Errorf("凭据仍被主机「%s」使用", h.Name)
				}
			}
			for _, r := range d.Resources {
				if r.CredentialID == id {
					return fmt.Errorf("凭据仍被系统「%s」使用", r.Name)
				}
			}
			for _, p := range d.AIProfiles {
				if p.CredentialID == id {
					return fmt.Errorf("凭据仍被 AI 配置「%s」使用", p.Name)
				}
			}
			if secretRef != "" {
				d.PendingDeletes = append(d.PendingDeletes, secretRef)
			}
			d.Credentials = slices.DeleteFunc(d.Credentials, func(v Credential) bool { return v.ID == id })
		case "host":
			for _, h := range d.AllHosts() {
				if h.JumpHostID == id {
					return fmt.Errorf("主机仍是「%s」的跳板机", h.Name)
				}
			}
			for _, r := range d.Resources {
				if r.HostID == id {
					return fmt.Errorf("主机仍关联「%s」", r.Name)
				}
			}
			d.Hosts = slices.DeleteFunc(d.Hosts, func(v Host) bool { return v.ID == id })
			d.ConversationHosts = slices.DeleteFunc(d.ConversationHosts, func(v Host) bool { return v.ID == id })
			d.RecentConnections = slices.DeleteFunc(d.RecentConnections, func(v RecentConnection) bool { return v.HostID == id })
		case "resource":
			d.Resources = slices.DeleteFunc(d.Resources, func(v Resource) bool { return v.ID == id })
		case "ai":
			for _, h := range d.AllHosts() {
				if h.AIProfileID == id {
					return fmt.Errorf("AI 配置仍被「%s」使用", h.Name)
				}
			}
			d.AIProfiles = slices.DeleteFunc(d.AIProfiles, func(v AIProfile) bool { return v.ID == id })
			if d.DefaultAIProfileID == id {
				d.DefaultAIProfileID = ""
			}
		default:
			return errors.New("未知的配置类型")
		}
		return nil
	})
	if err != nil {
		return err
	}
	if secretRef != "" {
		return s.CleanupSecrets()
	}
	return nil
}

func (s *Store) RecordConnection(hostID string) error {
	return s.mutateData(func(d *Data) error {
		if _, err := d.Host(hostID); err != nil {
			return err
		}
		d.RecentConnections = slices.DeleteFunc(d.RecentConnections, func(v RecentConnection) bool { return v.HostID == hostID })
		d.RecentConnections = append([]RecentConnection{{HostID: hostID, OpenedAt: now()}}, d.RecentConnections...)
		if len(d.RecentConnections) > 50 {
			d.RecentConnections = d.RecentConnections[:50]
		}
		return nil
	}, false) // Connection history must not invalidate resource authorization.
}

func (s *Store) ClearRecentConnections() error {
	return s.mutateData(func(d *Data) error {
		d.RecentConnections = nil
		return nil
	}, false)
}
func (s *Store) Secret(c Credential) (string, error) {
	if !c.HasSecret || c.SecretRef == "" {
		return "", errors.New("此凭据没有保存秘密，请在桌面端更新")
	}
	value, err := s.Secrets.Get(c.SecretRef)
	if err == nil || !credentialui.Available() {
		return value, err
	}
	if err := credentialui.Request(c.ID); err != nil {
		return "", err
	}
	d, err := s.Load()
	if err != nil {
		return "", err
	}
	updated, err := d.Credential(c.ID)
	if err != nil {
		return "", err
	}
	if updated.Kind != c.Kind || updated.Username != c.Username || updated.IdentityFile != c.IdentityFile {
		return "", errors.New("凭据账号或类型已变化，请重新发起操作")
	}
	return s.Secrets.Get(updated.SecretRef)
}

// CleanupSecrets is an idempotent journal: metadata is committed before old
// secret versions are deleted. A locked keyring leaves a retryable queue, and a
// failed metadata write cannot destroy a still-live credential.
func (s *Store) CleanupSecrets() error {
	failed := false
	err := s.mutate(func(d *Data) error {
		pending := []string{}
		for _, ref := range d.PendingDeletes {
			live := slices.ContainsFunc(d.Credentials, func(c Credential) bool { return c.SecretRef == ref })
			if live {
				continue
			}
			if err := s.Secrets.Delete(ref); err != nil {
				pending = append(pending, ref)
				failed = true
			}
		}
		d.PendingDeletes = pending
		return nil
	})
	if err != nil {
		return err
	}
	if failed {
		return errors.New("配置已更新，部分旧凭据等待清理；请解锁系统凭据库后点击「清理旧凭据」")
	}
	return nil
}

// EnableExternalPolicies commits a version gate only after the independent
// operation catalog exists. Older clients refuse version 2 instead of bypassing it.
func (s *Store) EnableExternalPolicies(id string) error {
	return s.mutate(func(d *Data) error {
		i := slices.IndexFunc(d.Resources, func(r Resource) bool { return r.ID == id })
		if i < 0 {
			return errors.New("系统连接不存在")
		}
		if d.Version == 1 {
			b, err := json.MarshalIndent(d, "", "  ")
			if err != nil {
				return err
			}
			f, err := os.OpenFile(filepath.Join(s.Dir, "connections.v1.backup.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			if err == nil {
				_, err = f.Write(b)
				if err == nil {
					err = f.Sync()
				}
				f.Close()
				if err != nil {
					return err
				}
			}
		}
		d.Version = 2
		d.Resources[i].Managed = true
		d.Resources[i].Discovery = nil
		if d.Resources[i].SystemID == "" {
			d.Resources[i].SystemID = id
		}
		if d.Resources[i].SystemName == "" {
			d.Resources[i].SystemName = d.Resources[i].Name
		}
		if d.Resources[i].Environment == "" {
			d.Resources[i].Environment = "production"
		}
		return nil
	})
}

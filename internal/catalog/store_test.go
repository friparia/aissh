package catalog

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type memorySecrets struct {
	mu         sync.Mutex
	values     map[string]string
	fail       bool
	failDelete bool
}

func (m *memorySecrets) Get(k string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.values[k]
	if !ok {
		return "", errors.New("not found")
	}
	return v, nil
}
func (m *memorySecrets) Set(k, v string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return errors.New("locked")
	}
	m.values[k] = v
	return nil
}
func (m *memorySecrets) Delete(k string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failDelete {
		return errors.New("locked")
	}
	delete(m.values, k)
	return nil
}

func TestFailedOldSecretDeletionCanBeRetried(t *testing.T) {
	s, m := testStore(t)
	c, err := s.SaveCredential(CredentialInput{Credential: Credential{Name: "test", Kind: "token"}, Secret: "old-secret"})
	if err != nil {
		t.Fatal(err)
	}
	oldRef := c.SecretRef
	m.failDelete = true
	c, err = s.SaveCredential(CredentialInput{Credential: c, Secret: "new-secret"})
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.Load()
	if err != nil || len(d.PendingDeletes) != 1 || d.PendingDeletes[0] != oldRef {
		t.Fatalf("cleanup journal lost: %v", err)
	}
	if v, err := s.Secret(c); err != nil || v != "new-secret" {
		t.Fatal("live secret lost during cleanup failure")
	}
	m.failDelete = false
	if err := s.CleanupSecrets(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Get(oldRef); err == nil {
		t.Fatal("old version not deleted")
	}
	d, _ = s.Load()
	if len(d.PendingDeletes) != 0 {
		t.Fatal("successful cleanup still pending")
	}
}
func testStore(t *testing.T) (*Store, *memorySecrets) {
	t.Helper()
	m := &memorySecrets{values: map[string]string{}}
	return New(t.TempDir(), m), m
}
func TestSecretsStayOutOfCatalogAndRotate(t *testing.T) {
	s, m := testStore(t)
	c, err := s.SaveCredential(CredentialInput{Credential: Credential{Name: "生产", Kind: "password", Username: "ops"}, Secret: "one-真实-secret"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Dir, "connections.json")
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "one-真实-secret") {
		t.Fatal("secret persisted")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	first := c.SecretRef
	c, err = s.SaveCredential(CredentialInput{Credential: c})
	if err != nil || c.SecretRef != first {
		t.Fatal("blank edit must keep value", err)
	}
	c, err = s.SaveCredential(CredentialInput{Credential: c, Secret: "two-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if c.SecretRef == first {
		t.Fatal("rotation reused old reference")
	}
	if _, err := m.Get(first); err == nil {
		t.Fatal("old secret retained")
	}
	m.fail = true
	_, err = s.SaveCredential(CredentialInput{Credential: c, Secret: "three-secret"})
	if err == nil {
		t.Fatal("locked keyring accepted")
	}
	d, _ := s.Load()
	saved, _ := d.Credential(c.ID)
	if saved.SecretRef != c.SecretRef {
		t.Fatal("failed update changed live reference")
	}
	v, _ := s.Secret(saved)
	if v != "two-secret" {
		t.Fatal("failed rotation lost secret")
	}
}
func TestReferencesAndJumpCycles(t *testing.T) {
	s, _ := testStore(t)
	c, err := s.SaveCredential(CredentialInput{Credential: Credential{Name: "账号", Kind: "password"}, Secret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.SaveHost(Host{Name: "主机", Address: "example.test", CredentialID: c.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("credential", c.ID); err == nil {
		t.Fatal("deleted referenced credential")
	}
	j, err := s.SaveHost(Host{Name: "跳板", Address: "jump.test", JumpHostID: h.ID})
	if err != nil {
		t.Fatal(err)
	}
	h.JumpHostID = j.ID
	if _, err := s.SaveHost(h); err == nil {
		t.Fatal("accepted cycle")
	}
	if err := s.Delete("host", h.ID); err == nil {
		t.Fatal("deleted referenced host")
	}
	h.JumpHostID = ""
	h.Address = "example.test\nProxyCommand evil"
	if _, err := s.SaveHost(h); err == nil {
		t.Fatal("accepted SSH config injection")
	}
}
func TestConcurrentIndependentSaves(t *testing.T) {
	s, _ := testStore(t)
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := New(s.Dir, s.Secrets).SaveHost(Host{Name: "host", Address: "localhost"})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	d, err := s.Load()
	if err != nil || len(d.Hosts) != 12 || d.Revision != 12 {
		t.Fatalf("lost updates: count=%d rev=%d err=%v", len(d.Hosts), d.Revision, err)
	}
}
func TestURLValidation(t *testing.T) {
	for _, url := range []string{"http://remote.test", "https://u:p@remote.test", "https://remote.test/?token=secret", "https://remote.test/#secret", "file:///etc/passwd"} {
		if _, err := ServiceURL(url); err == nil {
			t.Fatalf("accepted %s", url)
		}
	}
	for _, url := range []string{"https://remote.test/v1", "http://127.0.0.1:8123"} {
		if _, err := ServiceURL(url); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAIProfilesAllowRemoteHTTP(t *testing.T) {
	for _, provider := range []string{"model", "deepseek", "http"} {
		for _, endpoint := range []string{"http://models.internal:8000/v1", "http://192.168.1.100:8000/v1", "http://[2001:db8::1]:8000/v1", "https://models.example/v1"} {
			t.Run(provider+"/"+endpoint, func(t *testing.T) {
				store := New(t.TempDir(), &memorySecrets{values: make(map[string]string)})
				saved, err := store.SaveAIProfile(AIProfile{Name: "custom", Provider: provider, URL: endpoint, Model: "custom-model"}, true)
				if err != nil {
					t.Fatal(err)
				}
				data, err := store.Load()
				if err != nil {
					t.Fatal(err)
				}
				profile, err := data.AIProfile(saved.ID)
				if err != nil || profile.URL != endpoint || data.DefaultAIProfileID != saved.ID {
					t.Fatalf("profile=%+v err=%v", profile, err)
				}
			})
		}
	}
	for _, endpoint := range []string{"http://u:p@remote.test", "http://remote.test/?token=secret", "http://remote.test/#secret", "http://remote.test:0", "http://remote.test:65536", "file:///etc/passwd", "http:///v1"} {
		if _, err := AIServiceURL(endpoint); err == nil {
			t.Fatalf("accepted invalid AI endpoint %s", endpoint)
		}
	}
}

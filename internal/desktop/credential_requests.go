package desktop

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"time"

	"git.example.com/ai/wrd-ai-cli/internal/catalog"
	"git.example.com/ai/wrd-ai-cli/internal/credentialui"
)

type credentialRequest struct {
	CredentialID string `json:"credentialId"`
	RequestID    string `json:"requestId"`
	SessionID    string `json:"sessionId"`
}

func (s *Service) credentialChannel(session *terminalSession) ([]string, func(), error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, nil, err
	}
	token := catalog.NewID()
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 3 * time.Minute, MaxHeaderBytes: 4096}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, "denied", http.StatusForbidden)
			return
		}
		if r.URL.Path == "/conversation" {
			var input struct {
				ConversationID string `json:"conversationId"`
			}
			if json.NewDecoder(io.LimitReader(r.Body, 2048)).Decode(&input) != nil || input.ConversationID == "" || len(input.ConversationID) > 256 {
				http.Error(w, "invalid conversation", http.StatusBadRequest)
				return
			}
			session.mu.Lock()
			if session.closed {
				session.mu.Unlock()
				http.Error(w, "session closed", http.StatusGone)
				return
			}
			session.info.ConversationID = input.ConversationID
			info := session.info
			session.mu.Unlock()
			s.emit("session-updated", info)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path == "/workbench" {
			s.exchangeWorkbench(session, w, r)
			return
		}
		if r.URL.Path != "/credential" {
			http.NotFound(w, r)
			return
		}
		var input struct {
			CredentialID string `json:"credentialId"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 2048)).Decode(&input) != nil || !s.boundCredentialForProfile(session.hostID, input.CredentialID, session.aiProfileID) {
			http.Error(w, "unbound credential", http.StatusForbidden)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 170*time.Second)
		defer cancel()
		if err := s.requestCredential(ctx, session.id, input.CredentialID); err != nil {
			http.Error(w, "cancelled", http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	go func() { _ = server.Serve(listener) }()
	return []string{credentialui.AddressEnv + "=" + listener.Addr().String(), credentialui.CapabilityEnv + "=" + token}, func() { _ = server.Close() }, nil
}
func (s *Service) boundCredential(hostID, id string) bool {
	return s.boundCredentialForProfile(hostID, id, "")
}

func (s *Service) boundCredentialForProfile(hostID, id, aiProfileID string) bool {
	d, err := s.store.Load()
	if err != nil || id == "" {
		return false
	}
	h, err := d.Host(hostID)
	if err != nil {
		return false
	}
	pID := aiProfileID
	if pID == "" {
		pID = h.AIProfileID
	}
	if pID == "" {
		pID = d.DefaultAIProfileID
	}
	if p, err := d.AIProfile(pID); err == nil && p.CredentialID == id {
		return true
	}
	for _, r := range d.Resources {
		if r.HostID == hostID && r.CredentialID == id {
			return true
		}
	}
	for {
		if h.CredentialID == id {
			return true
		}
		if h.JumpHostID == "" {
			break
		}
		h, err = d.Host(h.JumpHostID)
		if err != nil {
			break
		}
	}
	return false
}
func (s *Service) requestCredential(ctx context.Context, sessionID, id string) error {
	request := credentialRequest{CredentialID: id, RequestID: catalog.NewID(), SessionID: sessionID}
	ch := make(chan bool, 1)
	s.mu.Lock()
	s.credentialRequests[request.RequestID] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.credentialRequests, request.RequestID)
		s.mu.Unlock()
		s.emit("credential-request-ended", request.RequestID)
	}()
	s.emit("credential-needed", request)
	select {
	case accepted := <-ch:
		if accepted {
			return nil
		}
		return errors.New("用户取消了凭据更新")
	case <-ctx.Done():
		return ctx.Err()
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}
func (s *Service) ResolveCredentialRequest(id string, accepted bool) error {
	s.mu.Lock()
	ch := s.credentialRequests[id]
	s.mu.Unlock()
	if ch == nil {
		return errors.New("凭据请求已结束")
	}
	select {
	case ch <- accepted:
		return nil
	default:
		return errors.New("凭据请求已处理")
	}
}

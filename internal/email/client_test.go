package email

import (
	"context"
	"testing"
	"time"

	"github.com/xiedada05/furry-drama-be-neo/internal/config"
)

func newTestClient() *Client {
	cfg := &config.Config{}
	cfg.JWT.Secret = "my-secret"
	cfg.IsDev = true
	cfg.Server.FrontendURL = "http://localhost:3000"
	return NewClient(cfg, nil)
}

// TestSendNotConfigured 未配置 SMTP → Send 返回 false。
func TestSendNotConfigured(t *testing.T) {
	c := newTestClient()
	c.cfg.Email.Host = ""
	c.cfg.Email.User = ""
	c.cfg.Email.Pass = ""
	if ok := c.Send(context.Background(), "a@b.com", "s", "h"); ok {
		t.Fatal("Send should return false when SMTP not configured")
	}
}

// TestTargetRateLimit 目标邮箱限流：第 max+1 封被拒。
func TestTargetRateLimit(t *testing.T) {
	tr := NewTargetRate(3, time.Hour)
	if !tr.Allow("a@b.com") || !tr.Allow("a@b.com") || !tr.Allow("a@b.com") {
		t.Fatal("first 3 should be allowed")
	}
	if tr.Allow("a@b.com") {
		t.Fatal("4th should be rejected")
	}
	if !tr.Allow("other@b.com") {
		t.Fatal("different target should be allowed")
	}
}

// TestSendUsesInjectedMailer Send 在配置齐全且注入 fake sendMail 时返回 true。
func TestSendUsesInjectedMailer(t *testing.T) {
	c := newTestClient()
	c.cfg.Email.Host = "smtp.example.com"
	c.cfg.Email.User = "u"
	c.cfg.Email.Pass = "p"
	c.cfg.Email.Port = 465
	var gotSubject, gotTo string
	c.SetSendMail(func(host string, port int, user, pass, fromName, to, subject, html string) (bool, error) {
		gotTo = to
		gotSubject = subject
		return true, nil
	})
	if ok := c.Send(context.Background(), "user@example.com", "Test 主题", "<p>hi</p>"); !ok {
		t.Fatal("Send should succeed with injected mailer")
	}
	if gotTo != "user@example.com" || gotSubject != "Test 主题" {
		t.Fatalf("to=%q subject=%q", gotTo, gotSubject)
	}
}

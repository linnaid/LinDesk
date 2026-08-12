package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"lindesk/internal/domain"
)

func TestLoginAndAuthenticateDemoUser(t *testing.T) {
	service := NewService(DemoTenants(time.Now().UTC()), DemoUsers(time.Now().UTC()), DemoRoles(), DemoMemberships(time.Now().UTC()), func() time.Time {
		return time.Date(2026, time.August, 6, 10, 0, 0, 0, time.UTC)
	})

	session, err := service.Login(context.Background(), LoginCommand{
		Email:    "supervisor@lindesk.local",
		Password: "password123",
	})
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if session.Token == "" {
		t.Fatalf("token is empty")
	}
	if strings.HasPrefix(session.Token, "demo_token_") {
		t.Fatalf("token = %q, want random access token", session.Token)
	}
	if _, ok := service.sessions[session.Token]; ok {
		t.Fatalf("session map stores raw token, want hashed token key")
	}
	if _, ok := service.sessions[TokenHash(session.Token)]; !ok {
		t.Fatalf("session map missing hashed token key")
	}
	if session.Actor.User.ID != "user_supervisor_001" {
		t.Fatalf("UserID = %q", session.Actor.User.ID)
	}
	if session.Actor.Tenant.ID != "tenant_demo" {
		t.Fatalf("TenantID = %q", session.Actor.Tenant.ID)
	}

	authenticated, err := service.Authenticate(context.Background(), session.Token)
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if authenticated.User.ID != "user_supervisor_001" {
		t.Fatalf("Authenticate UserID = %q", authenticated.User.ID)
	}
}

func TestLoginGeneratesDifferentTokensForSameUser(t *testing.T) {
	service := NewDemoService()
	first, err := service.Login(context.Background(), LoginCommand{
		Email:    "supervisor@lindesk.local",
		Password: "password123",
	})
	if err != nil {
		t.Fatalf("first Login() error = %v", err)
	}
	second, err := service.Login(context.Background(), LoginCommand{
		Email:    "supervisor@lindesk.local",
		Password: "password123",
	})
	if err != nil {
		t.Fatalf("second Login() error = %v", err)
	}
	if first.Token == second.Token {
		t.Fatalf("tokens are equal, want unique random tokens")
	}
}

func TestAuthenticateRejectsExpiredSession(t *testing.T) {
	now := time.Date(2026, time.August, 6, 10, 0, 0, 0, time.UTC)
	service := NewService(DemoTenants(now), DemoUsers(now), DemoRoles(), DemoMemberships(now), func() time.Time {
		return now
	})
	session, err := service.Login(context.Background(), LoginCommand{
		Email:    "supervisor@lindesk.local",
		Password: "password123",
	})
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	service.now = func() time.Time { return now.Add(9 * time.Hour) }

	_, err = service.Authenticate(context.Background(), session.Token)
	if !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("Authenticate() error = %v, want %v", err, ErrSessionExpired)
	}
}

func TestLogoutRevokesSession(t *testing.T) {
	service := NewDemoService()
	session, err := service.Login(context.Background(), LoginCommand{
		Email:    "supervisor@lindesk.local",
		Password: "password123",
	})
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}

	if err := service.Logout(context.Background(), session.Token); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	_, err = service.Authenticate(context.Background(), session.Token)
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("Authenticate() error = %v, want %v", err, ErrInvalidToken)
	}
}

func TestRequirePermission(t *testing.T) {
	actor := domain.Actor{
		Roles: []domain.Role{
			{
				Code: domain.RoleSupervisor,
				Permissions: []domain.Permission{
					domain.PermissionRefundRequestReview,
				},
			},
		},
	}

	if err := RequirePermission(actor, domain.PermissionRefundRequestReview); err != nil {
		t.Fatalf("RequirePermission() error = %v", err)
	}
	if err := RequirePermission(actor, domain.PermissionRefundTransactionWrite); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("RequirePermission() error = %v, want %v", err, ErrPermissionDenied)
	}
}

func TestLoginRejectsInvalidCredentials(t *testing.T) {
	service := NewDemoService()
	_, err := service.Login(context.Background(), LoginCommand{
		Email:    "supervisor@lindesk.local",
		Password: "wrong-password",
	})
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Login() error = %v, want %v", err, ErrInvalidCredentials)
	}
}

package auth

import (
	"context"
	"errors"
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

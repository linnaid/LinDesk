package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"lindesk/internal/domain"
)

func TestPersistentServicePersistsHashedSession(t *testing.T) {
	now := time.Date(2026, time.August, 16, 8, 0, 0, 0, time.UTC)
	repository := newFakeAuthRepository(now)
	service := NewPersistentService(repository, func() time.Time { return now })

	session, err := service.Login(context.Background(), LoginCommand{
		Email:    "CS@LINDESK.LOCAL",
		Password: "password123",
	})
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if session.Token == "" {
		t.Fatalf("Login() token is empty")
	}
	if session.ExpiresAt != now.Add(sessionTTL) {
		t.Fatalf("ExpiresAt = %s, want %s", session.ExpiresAt, now.Add(sessionTTL))
	}
	if _, ok := repository.sessions[session.Token]; ok {
		t.Fatalf("repository stores raw access token")
	}
	record, ok := repository.sessions[TokenHash(session.Token)]
	if !ok {
		t.Fatalf("repository missing hashed session")
	}
	if record.UserID != repository.user.ID || record.TenantID != repository.actor.Tenant.ID {
		t.Fatalf("persisted session = %+v", record)
	}
}

func TestPersistentServiceAuthenticateReloadsCurrentPermissions(t *testing.T) {
	now := time.Date(2026, time.August, 16, 8, 0, 0, 0, time.UTC)
	repository := newFakeAuthRepository(now)
	service := NewPersistentService(repository, func() time.Time { return now })

	session, err := service.Login(context.Background(), LoginCommand{
		Email:    repository.user.Email,
		Password: "password123",
	})
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}

	repository.actor.Roles[0].Permissions = []domain.Permission{domain.PermissionRefundRequestReview}
	actor, err := service.Authenticate(context.Background(), session.Token)
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if !actor.HasPermission(domain.PermissionRefundRequestReview) {
		t.Fatalf("Authenticate() did not load current permissions")
	}
	if actor.HasPermission(domain.PermissionRefundRequestCreate) {
		t.Fatalf("Authenticate() returned stale permissions")
	}
}

func TestPersistentServiceLogoutRevokesSession(t *testing.T) {
	now := time.Date(2026, time.August, 16, 8, 0, 0, 0, time.UTC)
	repository := newFakeAuthRepository(now)
	service := NewPersistentService(repository, func() time.Time { return now })

	session, err := service.Login(context.Background(), LoginCommand{
		Email:    repository.user.Email,
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

func TestPersistentServiceRejectsExpiredSession(t *testing.T) {
	now := time.Date(2026, time.August, 16, 8, 0, 0, 0, time.UTC)
	currentTime := now
	repository := newFakeAuthRepository(now)
	service := NewPersistentService(repository, func() time.Time { return currentTime })

	session, err := service.Login(context.Background(), LoginCommand{
		Email:    repository.user.Email,
		Password: "password123",
	})
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	currentTime = now.Add(sessionTTL)

	_, err = service.Authenticate(context.Background(), session.Token)
	if !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("Authenticate() error = %v, want %v", err, ErrSessionExpired)
	}
}

type fakeAuthRepository struct {
	user     domain.User
	actor    domain.Actor
	sessions map[string]SessionRecord
}

func newFakeAuthRepository(now time.Time) *fakeAuthRepository {
	user := domain.User{
		ID:           "user_cs_001",
		Name:         "客服一号",
		Email:        "cs@lindesk.local",
		PasswordHash: demoPasswordHash,
		Status:       domain.UserStatusActive,
		CreatedAt:    now,
	}
	return &fakeAuthRepository{
		user: user,
		actor: domain.Actor{
			Tenant: domain.Tenant{ID: "tenant_demo", Name: "LinDesk Demo 电商", Status: domain.TenantStatusActive, CreatedAt: now},
			User:   user,
			Roles: []domain.Role{{
				Code:        domain.RoleCustomerService,
				Name:        "客服专员",
				Permissions: []domain.Permission{domain.PermissionRefundRequestCreate},
			}},
		},
		sessions: make(map[string]SessionRecord),
	}
}

func (repository *fakeAuthRepository) FindUserByEmail(_ context.Context, email string) (domain.User, error) {
	if email != repository.user.Email {
		return domain.User{}, errUserNotFound
	}
	return repository.user, nil
}

func (repository *fakeAuthRepository) ResolveActor(_ context.Context, userID, tenantID string) (domain.Actor, error) {
	if userID != repository.actor.User.ID {
		return domain.Actor{}, ErrNoActiveMembership
	}
	if tenantID != "" && tenantID != repository.actor.Tenant.ID {
		return domain.Actor{}, ErrTenantNotFound
	}
	return repository.actor, nil
}

func (repository *fakeAuthRepository) CreateSession(_ context.Context, session SessionRecord) error {
	repository.sessions[session.TokenHash] = session
	return nil
}

func (repository *fakeAuthRepository) FindSessionByTokenHash(_ context.Context, tokenHash string) (SessionRecord, error) {
	session, ok := repository.sessions[tokenHash]
	if !ok {
		return SessionRecord{}, errSessionNotFound
	}
	return session, nil
}

func (repository *fakeAuthRepository) RevokeSession(_ context.Context, tokenHash string, revokedAt time.Time) error {
	session, ok := repository.sessions[tokenHash]
	if !ok {
		return nil
	}
	session.RevokedAt = &revokedAt
	repository.sessions[tokenHash] = session
	return nil
}

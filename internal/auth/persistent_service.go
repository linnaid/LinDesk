package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"lindesk/internal/domain"
)

const sessionTTL = 8 * time.Hour

var (
	errUserNotFound    = errors.New("auth user not found")
	errSessionNotFound = errors.New("auth session not found")
)

type SessionRecord struct {
	ID        string
	TokenHash string
	TenantID  string
	UserID    string
	ExpiresAt time.Time
	RevokedAt *time.Time
	CreatedAt time.Time
}

type Repository interface {
	FindUserByEmail(context.Context, string) (domain.User, error)
	ResolveActor(context.Context, string, string) (domain.Actor, error)
	CreateSession(context.Context, SessionRecord) error
	FindSessionByTokenHash(context.Context, string) (SessionRecord, error)
	RevokeSession(context.Context, string, time.Time) error
}

type PersistentService struct {
	repository Repository
	now        func() time.Time
}

var _ Authenticator = (*PersistentService)(nil)

func NewPersistentService(repository Repository, now func() time.Time) *PersistentService {
	if repository == nil {
		panic("auth repository is required")
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}

	return &PersistentService{repository: repository, now: now}
}

func (service *PersistentService) Login(ctx context.Context, command LoginCommand) (Session, error) {
	command.Email = strings.TrimSpace(strings.ToLower(command.Email))
	command.Password = strings.TrimSpace(command.Password)
	command.TenantID = strings.TrimSpace(command.TenantID)

	if command.Email == "" {
		return Session{}, ErrEmailRequired
	}
	if command.Password == "" {
		return Session{}, ErrPasswordRequired
	}

	user, err := service.repository.FindUserByEmail(ctx, command.Email)
	if errors.Is(err, errUserNotFound) {
		return Session{}, ErrInvalidCredentials
	}
	if err != nil {
		return Session{}, fmt.Errorf("find auth user: %w", err)
	}
	if user.Status != domain.UserStatusActive || !VerifyPassword(user.PasswordHash, command.Password) {
		return Session{}, ErrInvalidCredentials
	}

	// 确认 Actor
	actor, err := service.repository.ResolveActor(ctx, user.ID, command.TenantID)
	if err != nil {
		return Session{}, err
	}

	token, err := NewAccessToken()
	if err != nil {
		return Session{}, err
	}
	now := service.now().UTC()
	expiresAt := now.Add(sessionTTL)
	tokenHash := TokenHash(token)
	record := SessionRecord{
		ID:        sessionID(tokenHash),
		TokenHash: tokenHash,
		TenantID:  actor.Tenant.ID,
		UserID:    actor.User.ID,
		ExpiresAt: expiresAt,
		CreatedAt: now,
	}
	if err := service.repository.CreateSession(ctx, record); err != nil {
		return Session{}, fmt.Errorf("create auth session: %w", err)
	}

	return Session{Token: token, Actor: actor, ExpiresAt: expiresAt}, nil
}

// 验证客户端的 Token/Session 是否有效，并确定当前的用户
func (service *PersistentService) Authenticate(ctx context.Context, token string) (domain.Actor, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return domain.Actor{}, ErrTokenRequired
	}

	record, err := service.repository.FindSessionByTokenHash(ctx, TokenHash(token))
	if errors.Is(err, errSessionNotFound) {
		return domain.Actor{}, ErrInvalidToken
	}
	if err != nil {
		return domain.Actor{}, fmt.Errorf("find auth session: %w", err)
	}
	if record.RevokedAt != nil {
		return domain.Actor{}, ErrInvalidToken
	}
	if !service.now().UTC().Before(record.ExpiresAt) {
		return domain.Actor{}, ErrSessionExpired
	}

	actor, err := service.repository.ResolveActor(ctx, record.UserID, record.TenantID)
	if errors.Is(err, ErrTenantNotFound) || errors.Is(err, ErrNoActiveMembership) {
		return domain.Actor{}, ErrInvalidToken
	}
	if err != nil {
		return domain.Actor{}, err
	}

	return actor, nil
}

func (service *PersistentService) Logout(ctx context.Context, token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return ErrTokenRequired
	}

	if err := service.repository.RevokeSession(ctx, TokenHash(token), service.now().UTC()); err != nil {
		return fmt.Errorf("revoke auth session: %w", err)
	}

	return nil
}

func sessionID(tokenHash string) string {
	value := strings.TrimPrefix(tokenHash, "sha256:")
	return "session_" + value
}

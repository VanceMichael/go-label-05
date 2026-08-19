package auth

import (
	"context"
	"errors"
	"fmt"
	"go-base/internal/domain"
	"time"
)

type Repository interface {
	FindUser(context.Context, string, string) (domain.User, error)
	CreateSession(context.Context, domain.Session) error
	FindSession(context.Context, string) (domain.Session, domain.User, error)
	RevokeSession(context.Context, string, time.Time) error
}
type Service struct {
	Repo Repository
	TTL  time.Duration
	Now  func() time.Time
}

func (s Service) Login(ctx context.Context, tenant, email, password string) (domain.User, string, error) {
	u, err := s.Repo.FindUser(ctx, tenant, domain.NormalizeEmail(email))
	if err != nil || u.Disabled || !CheckPassword(u.PasswordDigest, password) {
		return domain.User{}, "", ErrCredentials
	}
	plain, digest, err := NewToken()
	if err != nil {
		return domain.User{}, "", fmt.Errorf("create session: %w", err)
	}
	now := s.Now()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if err := s.Repo.CreateSession(ctx, domain.Session{ID: "sess-" + digest[:16], UserID: u.ID, TenantID: u.TenantID, TokenDigest: digest, ExpiresAt: now.Add(s.TTL)}); err != nil {
		return domain.User{}, "", err
	}
	return u, plain, nil
}
func (s Service) Authenticate(ctx context.Context, token string) (domain.User, domain.Session, error) {
	if token == "" {
		return domain.User{}, domain.Session{}, domain.ErrUnauthorized
	}
	sess, user, err := s.Repo.FindSession(ctx, DigestToken(token))
	if err != nil {
		return domain.User{}, domain.Session{}, err
	}
	now := s.Now()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if user.Disabled {
		return domain.User{}, domain.Session{}, domain.ErrUnauthorized
	}
	if sess.RevokedAt != nil || !sess.ExpiresAt.After(now) {
		return domain.User{}, domain.Session{}, domain.ErrExpired
	}
	return user, sess, nil
}
func (s Service) Logout(ctx context.Context, sess domain.Session) error {
	now := s.Now()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return s.Repo.RevokeSession(ctx, sess.ID, now)
}
func RequireRole(user domain.User, roles ...domain.Role) error {
	for _, role := range roles {
		if user.Role == role {
			return nil
		}
	}
	return errors.Join(domain.ErrForbidden, fmt.Errorf("role %s is not allowed", user.Role))
}

package store

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"go-base/internal/domain"
	"time"
)

type AuthRepository struct{ DB *Database }

func (r AuthRepository) FindUser(ctx context.Context, tenant, email string) (domain.User, error) {
	var u domain.User
	err := r.DB.Pool.QueryRow(ctx, "SELECT id,tenant_id,email,password_digest,role,disabled FROM users WHERE tenant_id=$1 AND email=$2", tenant, email).Scan(&u.ID, &u.TenantID, &u.Email, &u.PasswordDigest, &u.Role, &u.Disabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, domain.ErrNotFound
	}
	return u, err
}
func (r AuthRepository) CreateSession(ctx context.Context, s domain.Session) error {
	_, err := r.DB.Pool.Exec(ctx, "INSERT INTO sessions(id,user_id,tenant_id,token_digest,expires_at) VALUES($1,$2,$3,$4,$5)", s.ID, s.UserID, s.TenantID, s.TokenDigest, s.ExpiresAt)
	return err
}
func (r AuthRepository) FindSession(ctx context.Context, digest string) (domain.Session, domain.User, error) {
	var s domain.Session
	var u domain.User
	err := r.DB.Pool.QueryRow(ctx, `SELECT s.id,s.user_id,s.tenant_id,s.token_digest,s.expires_at,s.revoked_at,u.id,u.tenant_id,u.email,u.password_digest,u.role,u.disabled FROM sessions s JOIN users u ON u.id=s.user_id AND u.tenant_id=s.tenant_id WHERE s.token_digest=$1`, digest).Scan(&s.ID, &s.UserID, &s.TenantID, &s.TokenDigest, &s.ExpiresAt, &s.RevokedAt, &u.ID, &u.TenantID, &u.Email, &u.PasswordDigest, &u.Role, &u.Disabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, u, domain.ErrUnauthorized
	}
	return s, u, err
}
func (r AuthRepository) RevokeSession(ctx context.Context, id string, at time.Time) error {
	tag, err := r.DB.Pool.Exec(ctx, "UPDATE sessions SET revoked_at=$2 WHERE id=$1 AND revoked_at IS NULL", id, at)
	if err == nil && tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return err
}

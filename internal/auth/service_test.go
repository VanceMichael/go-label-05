package auth

import (
	"context"
	"go-base/internal/domain"
	"testing"
	"time"
)

type fakeRepo struct {
	user    domain.User
	sess    domain.Session
	token   string
	revoked bool
}

func (f *fakeRepo) FindUser(_ context.Context, _, _ string) (domain.User, error) { return f.user, nil }
func (f *fakeRepo) CreateSession(_ context.Context, s domain.Session) error      { f.sess = s; return nil }
func (f *fakeRepo) FindSession(_ context.Context, _ string) (domain.Session, domain.User, error) {
	return f.sess, f.user, nil
}
func (f *fakeRepo) RevokeSession(_ context.Context, _ string, at time.Time) error {
	f.revoked = true
	f.sess.RevokedAt = &at
	return nil
}
func TestLoginAuthenticateLogout(t *testing.T) {
	digest, err := HashPassword("pw")
	if err != nil {
		t.Fatal(err)
	}
	r := &fakeRepo{user: domain.User{ID: "u", TenantID: "t", Email: "a@b", PasswordDigest: digest, Role: domain.RoleOperator}}
	s := Service{Repo: r, TTL: time.Hour, Now: func() time.Time { return time.Unix(100, 0) }}
	u, tok, e := s.Login(context.Background(), "t", "a@b", "pw")
	if e != nil || u.ID != "u" {
		t.Fatal(e)
	}
	r.sess.TokenDigest = DigestToken(tok)
	got, _, e := s.Authenticate(context.Background(), tok)
	if e != nil || got.ID != "u" {
		t.Fatal(e)
	}
	if e = s.Logout(context.Background(), r.sess); e != nil || !r.revoked {
		t.Fatal(e)
	}
}

func TestDisabledUserCannotReuseExistingSession(t *testing.T) {
	now := time.Unix(100, 0)
	r := &fakeRepo{
		user: domain.User{ID: "u", Disabled: true},
		sess: domain.Session{ExpiresAt: now.Add(time.Hour)},
	}
	s := Service{Repo: r, Now: func() time.Time { return now }}
	if _, _, err := s.Authenticate(context.Background(), "token"); err != domain.ErrUnauthorized {
		t.Fatalf("Authenticate() error = %v", err)
	}
}
func TestExpiredSession(t *testing.T) {
	now := time.Unix(100, 0)
	r := &fakeRepo{user: domain.User{ID: "u"}, sess: domain.Session{ExpiresAt: now.Add(-time.Second)}}
	s := Service{Repo: r, Now: func() time.Time { return now }}
	if _, _, e := s.Authenticate(context.Background(), "x"); e != domain.ErrExpired {
		t.Fatalf("%v", e)
	}
}

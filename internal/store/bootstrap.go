package store

import (
	"context"
	"go-base/internal/auth"
	"go-base/internal/domain"
)

func (d *Database) Bootstrap(ctx context.Context) error {
	users := []struct {
		id, email, password string
		role                domain.Role
	}{{"mgr-1", "manager@herd.local", "manager-pass", domain.RoleManager}, {"op-1", "operator@herd.local", "operator-pass", domain.RoleOperator}, {"env-1", "environment@herd.local", "environment-pass", domain.RoleEnvironment}}
	for _, u := range users {
		digest, err := auth.HashPassword(u.password)
		if err != nil {
			return err
		}
		if _, err := d.Pool.Exec(ctx, `INSERT INTO users(id,tenant_id,email,password_digest,role) VALUES($1,'demo',$2,$3,$4)
			ON CONFLICT(id) DO UPDATE SET email=excluded.email,role=excluded.role,password_digest=excluded.password_digest
			WHERE users.password_digest NOT LIKE '$2%';`, u.id, u.email, digest, u.role); err != nil {
			return err
		}
	}
	if _, err := d.Pool.Exec(ctx, `INSERT INTO barns(id,tenant_id,name,capacity,status) VALUES('barn-a','demo','North Barn',1200,'active') ON CONFLICT(id) DO NOTHING`); err != nil {
		return err
	}
	if _, err := d.Pool.Exec(ctx, `INSERT INTO animal_groups(id,tenant_id,barn_id,name,headcount,status) VALUES('group-a','demo','barn-a','Lactating A',1000,'active') ON CONFLICT(id) DO NOTHING`); err != nil {
		return err
	}
	_, err := d.Pool.Exec(ctx, `INSERT INTO feed_inventory(tenant_id,feed_code,available_kg,reserved_kg) VALUES('demo','TMR-01',100000,0) ON CONFLICT(tenant_id,feed_code) DO NOTHING`)
	return err
}

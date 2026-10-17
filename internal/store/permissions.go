package store

import (
	"context"
	"errors"
	"slices"
)

// GrantableRoles are the roles route permissions can be granted to (admin always passes).
var GrantableRoles = []string{RoleGuest, RoleMember, RoleVIP}

// SeedRoutePermissions writes default grants for routes that have never been seeded.
func (s *Store) SeedRoutePermissions(ctx context.Context, defaults map[string][]string) error {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := Now()
	for route, roles := range defaults {
		res, err := tx.ExecContext(ctx, `INSERT INTO route_seeds(route, seeded_at) VALUES(?, ?) ON CONFLICT DO NOTHING`, route, now)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue
		}
		for _, role := range roles {
			if _, err := tx.ExecContext(ctx, `INSERT INTO role_permissions(role, route) VALUES(?, ?) ON CONFLICT DO NOTHING`, role, route); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// RolePermissions returns role -> set of granted routes.
func (s *Store) RolePermissions(ctx context.Context) (map[string]map[string]bool, error) {
	rows, err := s.W.QueryContext(ctx, `SELECT role, route FROM role_permissions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]bool{}
	for _, r := range GrantableRoles {
		out[r] = map[string]bool{}
	}
	for rows.Next() {
		var role, route string
		if err := rows.Scan(&role, &route); err != nil {
			return nil, err
		}
		if out[role] != nil {
			out[role][route] = true
		}
	}
	return out, rows.Err()
}

// SetRouteRoles replaces the roles granted a route.
func (s *Store) SetRouteRoles(ctx context.Context, route string, roles []string) error {
	for _, r := range roles {
		if !slices.Contains(GrantableRoles, r) {
			return errors.New("无效角色: " + r)
		}
	}
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM role_permissions WHERE route = ?`, route); err != nil {
		return err
	}
	for _, r := range roles {
		if _, err := tx.ExecContext(ctx, `INSERT INTO role_permissions(role, route) VALUES(?, ?)`, r, route); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO route_seeds(route, seeded_at) VALUES(?, ?) ON CONFLICT DO NOTHING`, route, Now()); err != nil {
		return err
	}
	return tx.Commit()
}

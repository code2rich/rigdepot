// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package access

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/code2rich/rigdepot/internal/server/dbx"
)

// User is an account.
type User struct {
	ID            string
	Username      string
	DisplayName   string
	Email         string // lower case; empty when unknown
	EmailVerified bool
	Status        Status
	Admin         bool   // platform administrator
	Job           string // decides defaults only, never permissions
	FocusStages   []string
	Locale        string // empty follows the browser
	CreatedAt     time.Time
	Memberships   []Membership // by product name
}

// Ref returns the short form of u.
func (u User) Ref() UserRef {
	return UserRef{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName}
}

// UserRef is how a person is shown next to something else.
type UserRef struct {
	ID, Username, DisplayName string
}

// Membership is someone's role in one product.
type Membership struct {
	Product ProductRef
	Role    Role
}

// ProductRef is how a product is shown next to something else.
type ProductRef struct {
	ID, Code, Name string
}

const userColumns = `u.id, u.username, u.display_name, u.email, u.email_verified, u.status, u.is_admin, u.job_title, u.stages, u.locale, u.created_at`

func scanUser(sc interface{ Scan(...any) error }) (User, error) {
	var u User
	var stages string
	var created int64
	if err := sc.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.EmailVerified, &u.Status, &u.Admin, &u.Job, &stages, &u.Locale, &created); err != nil {
		return u, err
	}
	u.CreatedAt = dbx.Time(created)
	u.FocusStages = []string{}
	if err := json.Unmarshal([]byte(stages), &u.FocusStages); err != nil {
		return u, fmt.Errorf("access: user %s: stages: %w", u.ID, err)
	}
	return u, nil
}

// getUser reads one account with its memberships; ErrNotFound when there is
// none.
func getUser(ctx context.Context, q dbx.Querier, id string) (User, error) {
	u, err := scanUser(q.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users u WHERE u.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	if err != nil {
		return u, err
	}
	ms, err := membershipsOf(ctx, q, []string{u.ID})
	u.Memberships = ms[u.ID]
	return u, err
}

// membershipsOf returns the memberships of each of the users, by product
// name.
func membershipsOf(ctx context.Context, q dbx.Querier, userIDs []string) (map[string][]Membership, error) {
	out := make(map[string][]Membership, len(userIDs))
	for _, id := range userIDs {
		out[id] = []Membership{}
	}
	if len(userIDs) == 0 {
		return out, nil
	}
	args := make([]any, len(userIDs))
	for i, id := range userIDs {
		args[i] = id
	}
	rows, err := q.QueryContext(ctx, `SELECT m.user_id, p.id, p.code, p.name, m.role
		FROM memberships m JOIN products p ON p.id = m.product_id
		WHERE m.user_id IN (`+placeholders(len(userIDs))+`)
		ORDER BY p.name, p.code`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var uid string
		var m Membership
		if err := rows.Scan(&uid, &m.Product.ID, &m.Product.Code, &m.Product.Name, &m.Role); err != nil {
			return nil, err
		}
		out[uid] = append(out[uid], m)
	}
	return out, rows.Err()
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

// Me returns the actor's own account. Accounts that are not activated yet
// can read it too.
func (s *Service) Me(ctx context.Context, a Actor) (User, error) {
	if err := s.read(ctx, a, ActionUserRead, TargetAccount(a.UserID)); err != nil {
		return User{}, err
	}
	return getUser(ctx, s.db, a.UserID)
}

// GetUser returns one account. Platform administrators and product owners
// may read every account, everyone else only their own.
func (s *Service) GetUser(ctx context.Context, a Actor, id string) (User, error) {
	if fe := checkID("user_id", id); fe != nil {
		return User{}, invalid(*fe)
	}
	if err := s.read(ctx, a, ActionUserRead, TargetAccount(id)); err != nil {
		return User{}, err
	}
	return getUser(ctx, s.db, id)
}

// UserFilter narrows ListUsers.
type UserFilter struct {
	Query  string // matches username, display name and e-mail address
	Status Status // empty for all
}

// ListUsers returns the accounts by display name. Platform administrators
// and product owners (who look for people to add) may list them.
func (s *Service) ListUsers(ctx context.Context, a Actor, f UserFilter) ([]User, error) {
	if err := s.read(ctx, a, ActionUserRead, TargetPlatform()); err != nil {
		return nil, err
	}
	if len(f.Query) > 100 {
		return nil, invalid(FieldError{"q", "too_long"})
	}
	switch f.Status {
	case "", StatusActive, StatusPending, StatusDisabled:
	default:
		return nil, invalid(FieldError{"status", "enum"})
	}
	query := `SELECT ` + userColumns + ` FROM users u WHERE 1 = 1`
	var args []any
	if f.Status != "" {
		query += ` AND u.status = ?`
		args = append(args, string(f.Status))
	}
	if f.Query != "" {
		like := "%" + escapeLike(f.Query) + "%"
		query += ` AND (LOWER(u.username) LIKE LOWER(?) ESCAPE '\' OR LOWER(u.display_name) LIKE LOWER(?) ESCAPE '\' OR LOWER(u.email) LIKE LOWER(?) ESCAPE '\')`
		args = append(args, like, like, like)
	}
	query += ` ORDER BY LOWER(u.display_name), u.username`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	var users []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		users = append(users, u)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	ids := make([]string, len(users))
	for i, u := range users {
		ids[i] = u.ID
	}
	ms, err := membershipsOf(ctx, s.db, ids)
	if err != nil {
		return nil, err
	}
	for i := range users {
		users[i].Memberships = ms[users[i].ID]
	}
	return users, nil
}

func escapeLike(v string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(v)
}

// ProfileChange is what people change about themselves. Nil fields stay.
type ProfileChange struct {
	DisplayName *string
	Job         *string
	FocusStages *[]string
	Locale      *string
}

// UpdateProfile changes the actor's own profile. Job and focus stages decide
// what the home page shows first and which notifications arrive; they grant
// nothing.
func (s *Service) UpdateProfile(ctx context.Context, a Actor, c ProfileChange) (User, error) {
	var fields []*FieldError
	if c.DisplayName != nil {
		fields = append(fields, checkText("display_name", *c.DisplayName, 1, 64))
	}
	if c.Job != nil {
		fields = append(fields, checkText("job", *c.Job, 0, 32))
	}
	if c.Locale != nil {
		fields = append(fields, checkText("locale", *c.Locale, 0, 35))
	}
	if c.FocusStages != nil {
		if len(*c.FocusStages) > 16 {
			fields = append(fields, &FieldError{"focus_stages", "too_many"})
		}
		for i, st := range *c.FocusStages {
			if !stagePattern.MatchString(st) {
				fields = append(fields, &FieldError{fmt.Sprintf("focus_stages[%d]", i), "pattern"})
			}
		}
	}
	if err := collect(fields...); err != nil {
		return User{}, err
	}
	var out User
	err := s.write(ctx, a, func(op *writeOp) error {
		u, err := getUser(ctx, op.tx, a.UserID)
		if err != nil {
			return err
		}
		if err := op.check(ActionUserProfile, TargetAccount(a.UserID), userObject(u)); err != nil {
			return err
		}
		change := map[string]any{}
		if c.DisplayName != nil && *c.DisplayName != u.DisplayName {
			change["display_name"] = map[string]any{"from": u.DisplayName, "to": *c.DisplayName}
			u.DisplayName = *c.DisplayName
		}
		if c.Job != nil && *c.Job != u.Job {
			change["job"] = map[string]any{"from": u.Job, "to": *c.Job}
			u.Job = *c.Job
		}
		if c.Locale != nil && *c.Locale != u.Locale {
			change["locale"] = map[string]any{"from": u.Locale, "to": *c.Locale}
			u.Locale = *c.Locale
		}
		if c.FocusStages != nil {
			stages := dedupe(*c.FocusStages)
			if strings.Join(stages, ",") != strings.Join(u.FocusStages, ",") {
				change["focus_stages"] = map[string]any{"from": u.FocusStages, "to": stages}
				u.FocusStages = stages
			}
		}
		out = u
		if len(change) == 0 {
			return nil
		}
		stages, _ := json.Marshal(u.FocusStages)
		if _, err := op.tx.ExecContext(ctx, `UPDATE users SET display_name = ?, job_title = ?, stages = ?, locale = ?, updated_at = ? WHERE id = ?`,
			u.DisplayName, u.Job, string(stages), u.Locale, s.millis(), u.ID); err != nil {
			return err
		}
		return op.record(ActionUserProfile, userObject(u), change)
	})
	return out, err
}

func dedupe(v []string) []string {
	out := make([]string, 0, len(v))
	seen := map[string]bool{}
	for _, x := range v {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func userObject(u User) object {
	label := u.DisplayName
	if label == "" {
		label = u.Username
	}
	return object{kind: "user", id: u.ID, label: label}
}

// UserChange is what platform administrators change about an account. Nil
// fields stay.
type UserChange struct {
	Status *Status // StatusActive or StatusDisabled
	Admin  *bool
}

// adminsLock serialises every change that can take away the last platform
// administrator.
const adminsLock = "access.admins"

// UpdateUser disables or restores an account, or grants or takes platform
// administration. The platform always keeps at least one active
// administrator (ErrLastAdmin), also when two administrators take each
// other's rights at the same moment: the check and the change are in one
// transaction holding adminsLock.
//
// Disabling takes effect with the account's next request, because every
// request reads the account's status.
func (s *Service) UpdateUser(ctx context.Context, a Actor, id string, c UserChange) (User, error) {
	if fe := checkID("user_id", id); fe != nil {
		return User{}, invalid(*fe)
	}
	if c.Status != nil && *c.Status != StatusActive && *c.Status != StatusDisabled {
		return User{}, invalid(FieldError{"status", "enum"})
	}
	var out User
	var events []Event
	err := s.write(ctx, a, func(op *writeOp) error {
		if err := op.tx.Lock(ctx, adminsLock); err != nil {
			return err
		}
		u, err := getUser(ctx, op.tx, id)
		if errors.Is(err, ErrNotFound) {
			// Not knowing an account is no reason to tell who may not
			// change it: check against the platform first.
			if err := op.check(ActionUserDisable, TargetAccount(id), object{kind: "user", id: id, label: id}); err != nil {
				return err
			}
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		obj := userObject(u)
		was := u
		if c.Status != nil && *c.Status != u.Status {
			action := ActionUserEnable
			if *c.Status == StatusDisabled {
				action = ActionUserDisable
			}
			if err := op.check(action, TargetAccount(id), obj); err != nil {
				return err
			}
			u.Status = *c.Status
		}
		if c.Admin != nil && *c.Admin != u.Admin {
			action := ActionUserAdminGrant
			if !*c.Admin {
				action = ActionUserAdminDrop
			}
			if err := op.check(action, TargetAccount(id), obj); err != nil {
				return err
			}
			u.Admin = *c.Admin
		}
		if u.Status == was.Status && u.Admin == was.Admin {
			// Nothing changes; still only for those who may change it.
			if err := op.check(ActionUserDisable, TargetAccount(id), obj); err != nil {
				return err
			}
			out = u
			return nil
		}
		if was.Admin && was.Status == StatusActive && !(u.Admin && u.Status == StatusActive) {
			var others int
			if err := op.tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE is_admin = TRUE AND status = 'active' AND id <> ?`, id).Scan(&others); err != nil {
				return err
			}
			if others == 0 {
				return ErrLastAdmin
			}
		}
		now := s.millis()
		var disabledAt any
		if u.Status == StatusDisabled {
			disabledAt = now
		}
		if _, err := op.tx.ExecContext(ctx, `UPDATE users SET status = ?, is_admin = ?, disabled_at = ?, updated_at = ? WHERE id = ?`,
			string(u.Status), u.Admin, disabledAt, now, id); err != nil {
			return err
		}
		if u.Status != was.Status {
			action := ActionUserEnable
			if u.Status == StatusDisabled {
				action = ActionUserDisable
			}
			if err := op.record(action, obj, map[string]any{"status": map[string]any{"from": was.Status, "to": u.Status}}); err != nil {
				return err
			}
			events = append(events, UserStatusChanged{UserID: id, Old: was.Status, New: u.Status})
		}
		if u.Admin != was.Admin {
			action := ActionUserAdminGrant
			if !u.Admin {
				action = ActionUserAdminDrop
			}
			if err := op.record(action, obj, map[string]any{"platform_admin": map[string]any{"from": was.Admin, "to": u.Admin}}); err != nil {
				return err
			}
			events = append(events, AdminChanged{UserID: id, Admin: u.Admin})
		}
		for _, e := range events {
			op.emit(e)
		}
		out = u
		return nil
	})
	return out, err
}

// NewUser is an account to create.
type NewUser struct {
	Username      string
	DisplayName   string
	Email         string
	EmailVerified bool
	Status        Status // StatusPending for people who still have to ask to join; StatusActive otherwise
	Admin         bool
	Locale        string
}

// CreateUser creates an account inside the caller's transaction, for the
// sign-in code (package authn): the first administrator in the setup
// wizard, someone signing in through an identity provider for the first
// time. a is usually System. The audit entry is written in tx; a denial is
// returned but not recorded, since tx is the caller's.
func (s *Service) CreateUser(ctx context.Context, tx *dbx.Tx, a Actor, n NewUser) (User, error) {
	op := &writeOp{s: s, ctx: ctx, tx: tx, actor: a}
	if err := op.check(ActionUserCreate, TargetPlatform(), object{kind: "user", label: n.Username}); err != nil {
		return User{}, err
	}
	return s.insertUser(op, n)
}

func (s *Service) insertUser(op *writeOp, n NewUser) (User, error) {
	ctx := op.ctx
	n.Username = strings.ToLower(n.Username)
	fields := []*FieldError{checkText("display_name", n.DisplayName, 0, 64), checkText("locale", n.Locale, 0, 35)}
	if !usernamePattern.MatchString(n.Username) {
		fields = append(fields, &FieldError{"username", "pattern"})
	}
	if n.Email != "" {
		e, ok := normalEmail(n.Email)
		if !ok {
			fields = append(fields, &FieldError{"email", "format"})
		}
		n.Email = e
	}
	if n.Status != StatusActive && n.Status != StatusPending {
		fields = append(fields, &FieldError{"status", "enum"})
	}
	if err := collect(fields...); err != nil {
		return User{}, err
	}
	var taken int
	if err := op.tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE username = ?`, n.Username).Scan(&taken); err != nil {
		return User{}, err
	}
	if taken > 0 {
		return User{}, ErrUsernameTaken.with(map[string]any{"username": n.Username})
	}
	now := op.s.now()
	u := User{
		ID: dbx.NewID(), Username: n.Username, DisplayName: n.DisplayName, Email: n.Email,
		EmailVerified: n.EmailVerified, Status: n.Status, Admin: n.Admin, Locale: n.Locale,
		FocusStages: []string{}, CreatedAt: dbx.Time(dbx.Millis(now)), Memberships: []Membership{},
	}
	if _, err := op.tx.ExecContext(ctx, `INSERT INTO users (id, username, display_name, email, email_verified, status, is_admin, locale, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.ID, u.Username, u.DisplayName, u.Email, u.EmailVerified, string(u.Status), u.Admin, u.Locale, dbx.Millis(now), dbx.Millis(now)); err != nil {
		if dbx.IsUniqueViolation(err) {
			return User{}, ErrUsernameTaken.with(map[string]any{"username": n.Username})
		}
		return User{}, err
	}
	change := map[string]any{"username": u.Username, "status": u.Status}
	if u.Admin {
		change["platform_admin"] = true
	}
	return u, op.record(ActionUserCreate, userObject(u), change)
}

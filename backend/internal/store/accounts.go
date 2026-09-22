package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// ---- users ----

const userCols = `id, username, password_hash, must_change, created_at, disabled_at, last_login, totp_secret, totp_enabled_at, totp_recovery`

// totpRecoveryJSON encodes/decodes User.TOTPRecovery as a JSON array; a blank or unparsable column (an
// older row, or one never touched) reads back as no codes rather than an error.
func totpRecoveryJSON(codes []string) string {
	if len(codes) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(codes)
	return string(b)
}
func parseTOTPRecovery(s string) []string {
	var codes []string
	_ = json.Unmarshal([]byte(s), &codes)
	return codes
}

func scanUser(r scanner) (User, error) {
	var u User
	var mc int
	var created int64
	var dis, last, enabledAt sql.NullInt64
	var recovery string
	if err := r.Scan(&u.ID, &u.Username, &u.PasswordHash, &mc, &created, &dis, &last, &u.TOTPSecret, &enabledAt, &recovery); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return User{}, ErrNotFound
		}
		return User{}, err
	}
	u.MustChange, u.CreatedAt, u.DisabledAt, u.LastLogin = mc != 0, fromMS(created), fromNullMS(dis), fromNullMS(last)
	u.TOTPEnabledAt = fromNullMS(enabledAt)
	u.TOTPRecovery = parseTOTPRecovery(recovery)
	return u, nil
}

func (s *SQLite) CreateUser(ctx context.Context, u User) error {
	// org_id and role are legacy columns of the single-organisation schema; they stay empty.
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO users(id, org_id, username, role, password_hash, must_change, created_at) VALUES(?,'',?,'',?,?,?)`,
		u.ID, u.Username, u.PasswordHash, b2i(u.MustChange), ms(u.CreatedAt))
	if isUnique(err) {
		return ErrExists
	}
	return err
}

// SetTOTP is documented on the Store interface.
func (s *SQLite) SetTOTP(ctx context.Context, id, secret string, enabledAt *time.Time, recovery []string) error {
	var v any
	if enabledAt != nil {
		v = ms(*enabledAt)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE users SET totp_secret=?, totp_enabled_at=?, totp_recovery=? WHERE id=?`, secret, v, totpRecoveryJSON(recovery), id)
	if err != nil {
		return err
	}
	return needFound(res)
}

func (s *SQLite) GetUser(ctx context.Context, id string) (User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id=?`, id))
}

func (s *SQLite) GetUserByName(ctx context.Context, username string) (User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE lower(username)=lower(?)`, username))
}

func (s *SQLite) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

func (s *SQLite) DeleteUser(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{`DELETE FROM sessions WHERE user_id=?`, `DELETE FROM memberships WHERE user_id=?`, `DELETE FROM users WHERE id=?`} {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLite) SetPassword(ctx context.Context, id, hash string, mustChange bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE users SET password_hash=?, must_change=? WHERE id=?`, hash, b2i(mustChange), id)
	if err != nil {
		return err
	}
	return needFound(res)
}

func (s *SQLite) SetDisabled(ctx context.Context, id string, at *time.Time) error {
	var v any
	if at != nil {
		v = ms(*at)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE users SET disabled_at=? WHERE id=?`, v, id)
	if err != nil {
		return err
	}
	return needFound(res)
}

func (s *SQLite) MarkLogin(ctx context.Context, id string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET last_login=? WHERE id=?`, ms(now), id)
	return err
}

// ---- organisations and memberships ----

func (s *SQLite) CreateOrg(ctx context.Context, o Org, ownerID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO orgs(id, name, created_at, created_by) VALUES(?,?,?,?)`, o.ID, o.Name, ms(o.CreatedAt), o.CreatedBy); err != nil {
		if isUnique(err) {
			return ErrExists
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO memberships(org_id, user_id, role, created_at, added_by) VALUES(?,?,'owner',?,'')`, o.ID, ownerID, ms(o.CreatedAt)); err != nil {
		return err
	}
	return tx.Commit()
}

func scanOrg(r scanner) (Org, error) {
	var o Org
	var at int64
	if err := r.Scan(&o.ID, &o.Name, &at, &o.CreatedBy); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Org{}, ErrNotFound
		}
		return Org{}, err
	}
	o.CreatedAt = fromMS(at)
	return o, nil
}

func (s *SQLite) GetOrg(ctx context.Context, id string) (Org, error) {
	return scanOrg(s.db.QueryRowContext(ctx, `SELECT id, name, created_at, created_by FROM orgs WHERE id=?`, id))
}

func (s *SQLite) ListOrgs(ctx context.Context) ([]Org, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, created_at, created_by FROM orgs ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Org
	for rows.Next() {
		o, err := scanOrg(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *SQLite) RenameOrg(ctx context.Context, id, name string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE orgs SET name=? WHERE id=?`, name, id)
	if err != nil {
		return err
	}
	return needFound(res)
}

func (s *SQLite) DeleteOrg(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// The audit trail stays: it is the record of who created and deleted the organisation.
	for _, q := range []string{
		`DELETE FROM snapshots WHERE agent_id IN (SELECT id FROM agents WHERE org_id=?)`,
		`DELETE FROM snapshots WHERE agent_id IN (SELECT id || '#flows' FROM agents WHERE org_id=?)`,
		`DELETE FROM snapshots WHERE agent_id IN (SELECT id || '#consent' FROM agents WHERE org_id=?)`,
		`DELETE FROM agents WHERE org_id=?`,
		`DELETE FROM tokens WHERE org_id=?`,
		`DELETE FROM workspace WHERE org_id=?`,
		`DELETE FROM history WHERE org_id=?`,
		`DELETE FROM events WHERE org_id=?`,
		`DELETE FROM settings WHERE org_id=?`,
		`DELETE FROM tombstones WHERE org_id=?`,
		`DELETE FROM identities WHERE org_id=?`,
		`DELETE FROM model_state WHERE org_id=?`,
		`DELETE FROM orgs WHERE id=?`, // memberships and invites go with it
	} {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLite) ListMyOrgs(ctx context.Context, userID string) ([]MyOrg, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT o.id, o.name, o.created_at, o.created_by, m.role FROM memberships m JOIN orgs o ON o.id=m.org_id WHERE m.user_id=? ORDER BY o.created_at, o.id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MyOrg
	for rows.Next() {
		var m MyOrg
		var at int64
		if err := rows.Scan(&m.ID, &m.Name, &at, &m.CreatedBy, &m.Role); err != nil {
			return nil, err
		}
		m.CreatedAt = fromMS(at)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *SQLite) GetMembership(ctx context.Context, org, userID string) (Membership, error) {
	var m Membership
	var at int64
	err := s.db.QueryRowContext(ctx, `SELECT org_id, user_id, role, created_at, added_by FROM memberships WHERE org_id=? AND user_id=?`, org, userID).
		Scan(&m.OrgID, &m.UserID, &m.Role, &at, &m.AddedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return Membership{}, ErrNotFound
	}
	m.CreatedAt = fromMS(at)
	return m, err
}

// ListMembers never reads the password hash: a listing has no use for it, and a value that is never loaded
// cannot leak through a serialiser, a log line or a later change to the caller. Member.PasswordHash is empty.
func (s *SQLite) ListMembers(ctx context.Context, org string) ([]Member, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT u.id, u.username, u.must_change, u.created_at, u.disabled_at, u.last_login, m.role, m.created_at
		 FROM memberships m JOIN users u ON u.id=m.user_id WHERE m.org_id=? ORDER BY lower(u.username)`, org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Member
	for rows.Next() {
		var m Member
		var mc int
		var created, joined int64
		var dis, last sql.NullInt64
		if err := rows.Scan(&m.ID, &m.Username, &mc, &created, &dis, &last, &m.Role, &joined); err != nil {
			return nil, err
		}
		m.MustChange, m.CreatedAt, m.DisabledAt, m.LastLogin, m.JoinedAt = mc != 0, fromMS(created), fromNullMS(dis), fromNullMS(last), fromMS(joined)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *SQLite) AddMember(ctx context.Context, m Membership) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO memberships(org_id, user_id, role, created_at, added_by) VALUES(?,?,?,?,?)`, m.OrgID, m.UserID, m.Role, ms(m.CreatedAt), m.AddedBy)
	if isUnique(err) {
		return ErrExists
	}
	return err
}

func (s *SQLite) SetMemberRole(ctx context.Context, org, userID, role string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE memberships SET role=? WHERE org_id=? AND user_id=?`, role, org, userID)
	if err != nil {
		return err
	}
	return needFound(res)
}

func (s *SQLite) RemoveMember(ctx context.Context, org, userID string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM memberships WHERE org_id=? AND user_id=?`, org, userID)
	if err != nil {
		return err
	}
	return needFound(res)
}

func (s *SQLite) CountOwners(ctx context.Context, org string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memberships m JOIN users u ON u.id=m.user_id WHERE m.org_id=? AND m.role='owner' AND u.disabled_at IS NULL`, org).Scan(&n)
	return n, err
}

// ---- invites ----

func (s *SQLite) CreateInvite(ctx context.Context, inv Invite, hash []byte) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO invites(id, org_id, hash, role, label, created_by, created_at, expires_at) VALUES(?,?,?,?,?,?,?,?)`,
		inv.ID, inv.OrgID, hash, inv.Role, inv.Label, inv.CreatedBy, ms(inv.CreatedAt), ms(inv.ExpiresAt))
	return err
}

const inviteCols = `id, org_id, role, label, created_by, created_at, expires_at, used_at, used_by`

func scanInvite(r scanner) (Invite, error) {
	var inv Invite
	var c, e int64
	var u sql.NullInt64
	if err := r.Scan(&inv.ID, &inv.OrgID, &inv.Role, &inv.Label, &inv.CreatedBy, &c, &e, &u, &inv.UsedBy); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Invite{}, ErrNotFound
		}
		return Invite{}, err
	}
	inv.CreatedAt, inv.ExpiresAt, inv.UsedAt = fromMS(c), fromMS(e), fromNullMS(u)
	return inv, nil
}

func (s *SQLite) ListInvites(ctx context.Context, org string) ([]Invite, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+inviteCols+` FROM invites WHERE org_id=? ORDER BY created_at DESC`, org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Invite
	for rows.Next() {
		inv, err := scanInvite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

func (s *SQLite) RevokeInvite(ctx context.Context, org, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM invites WHERE org_id=? AND id=? AND used_at IS NULL`, org, id)
	if err != nil {
		return err
	}
	return needFound(res)
}

func (s *SQLite) PeekInvite(ctx context.Context, hash []byte, now time.Time) (Invite, error) {
	inv, err := scanInvite(s.db.QueryRowContext(ctx, `SELECT `+inviteCols+` FROM invites WHERE hash=? AND used_at IS NULL AND expires_at>?`, hash, ms(now)))
	if errors.Is(err, ErrNotFound) {
		return Invite{}, ErrTokenInvalid
	}
	return inv, err
}

func (s *SQLite) UseInvite(ctx context.Context, hash []byte, userID string, now time.Time) (Invite, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Invite{}, err
	}
	defer tx.Rollback()
	inv, err := scanInvite(tx.QueryRowContext(ctx, `SELECT `+inviteCols+` FROM invites WHERE hash=? AND used_at IS NULL AND expires_at>?`, hash, ms(now)))
	if errors.Is(err, ErrNotFound) {
		return Invite{}, ErrTokenInvalid
	}
	if err != nil {
		return Invite{}, err
	}
	var have int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM memberships WHERE org_id=? AND user_id=?`, inv.OrgID, userID).Scan(&have); err != nil {
		return Invite{}, err
	}
	if have > 0 {
		return inv, ErrExists
	}
	res, err := tx.ExecContext(ctx, `UPDATE invites SET used_at=?, used_by=? WHERE id=? AND used_at IS NULL`, ms(now), userID, inv.ID)
	if err != nil {
		return Invite{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return Invite{}, ErrTokenInvalid
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO memberships(org_id, user_id, role, created_at, added_by) VALUES(?,?,?,?,?)`, inv.OrgID, userID, inv.Role, ms(now), inv.CreatedBy); err != nil {
		return Invite{}, err
	}
	inv.UsedAt, inv.UsedBy = &now, userID
	return inv, tx.Commit()
}

// ---- sessions ----

func (s *SQLite) CreateSession(ctx context.Context, hash []byte, userID, ip string, now, expires time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions(hash, user_id, created_at, last_used, expires_at, ip) VALUES(?,?,?,?,?,?)`,
		hash, userID, ms(now), ms(now), ms(expires), ip)
	return err
}

func (s *SQLite) LookupSession(ctx context.Context, hash []byte) (Session, User, error) {
	var se Session
	var created, used, exp int64
	var uid string
	err := s.db.QueryRowContext(ctx, `SELECT user_id, created_at, last_used, expires_at, ip FROM sessions WHERE hash=?`, hash).Scan(&uid, &created, &used, &exp, &se.IP)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, User{}, ErrNotFound
	}
	if err != nil {
		return Session{}, User{}, err
	}
	se.UserID, se.CreatedAt, se.LastUsed, se.ExpiresAt = uid, fromMS(created), fromMS(used), fromMS(exp)
	u, err := s.GetUser(ctx, uid)
	return se, u, err
}

func (s *SQLite) TouchSession(ctx context.Context, hash []byte, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET last_used=? WHERE hash=?`, ms(now), hash)
	return err
}

func (s *SQLite) DeleteSession(ctx context.Context, hash []byte) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE hash=?`, hash)
	return err
}

func (s *SQLite) DeleteUserSessions(ctx context.Context, userID string, except []byte) error {
	if len(except) == 0 {
		_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=?`, userID)
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=? AND hash<>?`, userID, except)
	return err
}

func (s *SQLite) PurgeSessions(ctx context.Context, olderThan time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at<? OR last_used<?`, ms(olderThan), ms(olderThan.Add(-24*time.Hour)))
	return err
}

// ---- workspace ----

func (s *SQLite) GetWorkspace(ctx context.Context, org string) (Workspace, error) {
	w := Workspace{OrgID: org}
	var at int64
	err := s.db.QueryRowContext(ctx, `SELECT rev, data, updated_at, updated_by, note FROM workspace WHERE org_id=?`, org).Scan(&w.Rev, &w.Data, &at, &w.UpdatedBy, &w.Note)
	if errors.Is(err, sql.ErrNoRows) {
		return w, nil
	}
	if err != nil {
		return Workspace{}, err
	}
	w.UpdatedAt = fromMS(at)
	return w, nil
}

func (s *SQLite) PutWorkspace(ctx context.Context, org string, expectRev int64, data []byte, by string, now time.Time) (Workspace, error) {
	var res sql.Result
	var err error
	if expectRev == 0 {
		res, err = s.db.ExecContext(ctx, `INSERT INTO workspace(org_id, rev, data, updated_at, updated_by, note) VALUES(?,1,?,?,?,'') ON CONFLICT(org_id) DO NOTHING`, org, data, ms(now), by)
	} else {
		res, err = s.db.ExecContext(ctx, `UPDATE workspace SET rev=rev+1, data=?, updated_at=?, updated_by=?, note='' WHERE org_id=? AND rev=?`, data, ms(now), by, org, expectRev)
	}
	if err != nil {
		return Workspace{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		cur, err := s.GetWorkspace(ctx, org)
		if err != nil {
			return Workspace{}, err
		}
		return cur, ErrConflict
	}
	return Workspace{OrgID: org, Rev: expectRev + 1, Data: data, UpdatedAt: now.UTC(), UpdatedBy: by}, nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func needFound(res sql.Result) error {
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrNotFound
	}
	return nil
}

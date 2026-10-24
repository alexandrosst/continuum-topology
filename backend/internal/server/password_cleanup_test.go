package server

import (
	"context"
	"errors"
	"testing"

	"ikhnos/internal/accounts"
	"ikhnos/internal/store"
)

// sessionsStuck is a store that cannot end an account's sessions (a busy database).
type sessionsStuck struct{ store.Store }

func (sessionsStuck) DeleteUserSessions(context.Context, string, []byte) error {
	return errors.New("database is locked")
}

// A password change is the answer to "someone else may have the password". When the sessions that were signed in under the old one
// could not be ended, the person is told, so they do not believe the account is closed to whoever held it.
func TestAPasswordChangeThatCouldNotEndTheOtherSessionsSaysSo(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "jamie", accounts.RoleViewer)
	real := a.base.Accounts.Store
	a.base.Accounts.Store = sessionsStuck{real}
	defer func() { a.base.Accounts.Store = real }()
	r := a.do("POST", "/api/v1/auth/password", map[string]string{"current": goodPW, "new": "another long pass 7!"}, withCookie(cookie))
	if r.Code == 200 {
		t.Fatalf("the change was reported as done although the other sessions could not be ended: %s", r.Body.String())
	}
}

// The same for the recovery from the command line, which is what follows a suspected compromise.
func TestOfflineRecoveryThatCouldNotEndTheSessionsSaysSo(t *testing.T) {
	a := newAdminRig(t)
	a.user(t, "jamie", accounts.RoleViewer)
	real := a.base.Accounts.Store
	a.base.Accounts.Store = sessionsStuck{real}
	defer func() { a.base.Accounts.Store = real }()
	if _, err := a.base.Accounts.RecoverPassword(a.ctx, "jamie"); err == nil {
		t.Fatal("the recovery was reported as done although the sessions could not be ended")
	}
}

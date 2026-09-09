package auth_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/podium/podium/internal/auth"
	"github.com/podium/podium/internal/storage"
)

// helpers ----------------------------------------------------------------

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "podium.db")
	db, err := storage.Open(context.Background(), path, storage.Options{})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newSvc(t *testing.T) *auth.Service {
	t.Helper()
	return auth.NewService(newTestDB(t))
}

// uniqueUsername returns a username unique to the test so parallel runs do
// not collide on the users.username UNIQUE constraint.
func uniqueUsername(t *testing.T, prefix string) string {
	t.Helper()
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// password tests ---------------------------------------------------------

// TestPasswordHashRoundtrip verifies Hash produces a bcrypt hash that
// Verify accepts, and that Verify rejects a wrong password. Cost is fixed at
// 10 per PLAN.md risk #3 — see also TestPasswordCostLocked.
func TestPasswordHashRoundtrip(t *testing.T) {
	t.Parallel()
	hash, err := auth.HashPassword("hunter2-correct")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if hash == "hunter2-correct" {
		t.Fatal("HashPassword returned plaintext")
	}
	if err := auth.VerifyPassword(hash, "hunter2-correct"); err != nil {
		t.Fatalf("VerifyPassword on correct: %v", err)
	}
	if err := auth.VerifyPassword(hash, "hunter2-wrong"); err == nil {
		t.Fatal("VerifyPassword on wrong password should fail")
	}
}

// TestPasswordCostLocked verifies the bcrypt cost stays at 10. If this ever
// changes to 12+ the signup latency budget in PLAN.md risk #3 will be busted;
// if it drops to 8 the hashes become brute-forceable in seconds.
func TestPasswordCostLocked(t *testing.T) {
	t.Parallel()
	hash, err := auth.HashPassword("pw")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	cost, err := auth.CostOf(hash)
	if err != nil {
		t.Fatalf("CostOf: %v", err)
	}
	if cost != 10 {
		t.Fatalf("expected bcrypt cost 10, got %d", cost)
	}
}

// signup / status tests --------------------------------------------------

// TestSignupCreatesPendingUser verifies that Signup inserts a PENDING user
// (spec.md §6) and Signup succeeds even when an admin has not approved them.
func TestSignupCreatesPendingUser(t *testing.T) {
	t.Parallel()
	svc := newSvc(t)

	user, err := svc.Signup(context.Background(), auth.SignupInput{
		Username: uniqueUsername(t, "alice"),
		Email:    "alice@example.com",
		Password: "sup3r-secret",
	})
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}
	if user.Status != auth.StatusPending {
		t.Fatalf("expected status PENDING, got %q", user.Status)
	}
	if user.Role != auth.RoleUser {
		t.Fatalf("expected role USER, got %q", user.Role)
	}
	if user.ID == 0 {
		t.Fatal("expected non-zero user id")
	}
}

// TestSignupRejectsWeakPassword enforces the minimum length rule from
// AGENTS.md §41 ("validate input"). Anything under 8 chars is rejected.
func TestSignupRejectsWeakPassword(t *testing.T) {
	t.Parallel()
	svc := newSvc(t)

	_, err := svc.Signup(context.Background(), auth.SignupInput{
		Username: uniqueUsername(t, "weak"),
		Email:    "weak@example.com",
		Password: "short",
	})
	if err == nil {
		t.Fatal("Signup with short password should fail")
	}
	if !errors.Is(err, auth.ErrInvalidPassword) {
		t.Fatalf("expected ErrInvalidPassword, got %v", err)
	}
}

// TestSignupRejectsDuplicateUsername verifies the users.username UNIQUE
// constraint surfaces as a typed error.
func TestSignupRejectsDuplicateUsername(t *testing.T) {
	t.Parallel()
	svc := newSvc(t)

	in := auth.SignupInput{
		Username: uniqueUsername(t, "dup"),
		Email:    "dup@example.com",
		Password: "goodpassword",
	}
	if _, err := svc.Signup(context.Background(), in); err != nil {
		t.Fatalf("Signup (1st): %v", err)
	}
	in.Email = "dup2@example.com"
	_, err := svc.Signup(context.Background(), in)
	if !errors.Is(err, auth.ErrUsernameTaken) {
		t.Fatalf("expected ErrUsernameTaken, got %v", err)
	}
}

// login tests ------------------------------------------------------------

// TestLoginPendingIsBlocked is the load-bearing auth test: a freshly signed
// up PENDING user must not be able to log in (spec.md §7 + DECISIONS.md F).
func TestLoginPendingIsBlocked(t *testing.T) {
	t.Parallel()
	svc := newSvc(t)

	in := auth.SignupInput{
		Username: uniqueUsername(t, "pending"),
		Email:    "pending@example.com",
		Password: "goodpassword",
	}
	if _, err := svc.Signup(context.Background(), in); err != nil {
		t.Fatalf("Signup: %v", err)
	}

	_, err := svc.Login(context.Background(), in.Username, in.Password)
	if !errors.Is(err, auth.ErrLoginBlocked) {
		t.Fatalf("expected ErrLoginBlocked, got %v", err)
	}
}

// TestLoginApprovedSucceeds covers the happy path: admin approves the user,
// then login succeeds and returns a session token.
func TestLoginApprovedSucceeds(t *testing.T) {
	t.Parallel()
	svc := newSvc(t)

	in := auth.SignupInput{
		Username: uniqueUsername(t, "approved"),
		Email:    "approved@example.com",
		Password: "goodpassword",
	}
	user, err := svc.Signup(context.Background(), in)
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}
	if err := svc.ApproveUser(context.Background(), user.ID); err != nil {
		t.Fatalf("ApproveUser: %v", err)
	}

	session, err := svc.Login(context.Background(), in.Username, in.Password)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if session.Token == "" {
		t.Fatal("expected non-empty session token")
	}
	if session.User.ID != user.ID {
		t.Fatalf("session user %d != signup user %d", session.User.ID, user.ID)
	}
	if session.ExpiresAt.Before(time.Now()) {
		t.Fatal("session already expired")
	}
}

// TestLoginWrongPasswordFails verifies that bcrypt mismatch returns the
// generic ErrInvalidCredentials (not a typed "wrong password" — don't leak
// which field failed, AGENTS.md §41).
func TestLoginWrongPasswordFails(t *testing.T) {
	t.Parallel()
	svc := newSvc(t)

	in := auth.SignupInput{
		Username: uniqueUsername(t, "wrong"),
		Email:    "wrong@example.com",
		Password: "goodpassword",
	}
	user, err := svc.Signup(context.Background(), in)
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}
	_ = svc.ApproveUser(context.Background(), user.ID)

	_, err = svc.Login(context.Background(), in.Username, "totally-wrong")
	if !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials, got %v", err)
	}
}

// TestLoginDisabledIsBlocked: a user who was APPROVED but has been DISABLED
// must not be able to log in (DECISIONS.md F).
func TestLoginDisabledIsBlocked(t *testing.T) {
	t.Parallel()
	svc := newSvc(t)

	in := auth.SignupInput{
		Username: uniqueUsername(t, "disabled"),
		Email:    "disabled@example.com",
		Password: "goodpassword",
	}
	user, err := svc.Signup(context.Background(), in)
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}
	_ = svc.ApproveUser(context.Background(), user.ID)
	if err := svc.SetUserStatus(context.Background(), user.ID, auth.StatusDisabled); err != nil {
		t.Fatalf("SetUserStatus: %v", err)
	}

	_, err = svc.Login(context.Background(), in.Username, in.Password)
	if !errors.Is(err, auth.ErrLoginBlocked) {
		t.Fatalf("expected ErrLoginBlocked, got %v", err)
	}
}

// TestLoginRejectedIsBlocked: per DECISIONS.md F, REJECTED is terminal and
// must not log in.
func TestLoginRejectedIsBlocked(t *testing.T) {
	t.Parallel()
	svc := newSvc(t)

	in := auth.SignupInput{
		Username: uniqueUsername(t, "rej"),
		Email:    "rej@example.com",
		Password: "goodpassword",
	}
	user, err := svc.Signup(context.Background(), in)
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}
	if err := svc.SetUserStatus(context.Background(), user.ID, auth.StatusRejected); err != nil {
		t.Fatalf("SetUserStatus: %v", err)
	}

	_, err = svc.Login(context.Background(), in.Username, in.Password)
	if !errors.Is(err, auth.ErrLoginBlocked) {
		t.Fatalf("expected ErrLoginBlocked, got %v", err)
	}
}

// session tests ----------------------------------------------------------

// TestSessionLoad verifies a session can be loaded by its token and that
// loading an unknown token returns ErrSessionNotFound.
func TestSessionLoad(t *testing.T) {
	t.Parallel()
	svc := newSvc(t)

	in := auth.SignupInput{
		Username: uniqueUsername(t, "load"),
		Email:    "load@example.com",
		Password: "goodpassword",
	}
	user, err := svc.Signup(context.Background(), in)
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}
	_ = svc.ApproveUser(context.Background(), user.ID)

	s, err := svc.Login(context.Background(), in.Username, in.Password)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	loaded, err := svc.LoadSession(context.Background(), s.Token)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if loaded.User.ID != user.ID {
		t.Fatalf("loaded user %d != expected %d", loaded.User.ID, user.ID)
	}

	_, err = svc.LoadSession(context.Background(), "nope-not-a-token")
	if !errors.Is(err, auth.ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound, got %v", err)
	}
}

// TestSessionLogoutInvalidatesToken: after Logout the token must no longer
// resolve via LoadSession. (AGENTS.md §6 — sessions must be revocable.)
func TestSessionLogoutInvalidatesToken(t *testing.T) {
	t.Parallel()
	svc := newSvc(t)

	in := auth.SignupInput{
		Username: uniqueUsername(t, "logout"),
		Email:    "logout@example.com",
		Password: "goodpassword",
	}
	user, err := svc.Signup(context.Background(), in)
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}
	_ = svc.ApproveUser(context.Background(), user.ID)

	s, err := svc.Login(context.Background(), in.Username, in.Password)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if err := svc.Logout(context.Background(), s.Token); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, err := svc.LoadSession(context.Background(), s.Token); !errors.Is(err, auth.ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound after logout, got %v", err)
	}
}

// SeedAdmin tests --------------------------------------------------------

// TestSeedAdminUpserts verifies that calling SeedAdmin twice with the same
// env-var credentials does NOT create a duplicate admin row. Per spec.md §6
// the default admin must exist on every boot; per AGENTS.md §7 the
// initialization must be safe to run more than once.
func TestSeedAdminUpserts(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	svc := auth.NewService(db)

	if err := auth.SeedAdmin(context.Background(), db, "root", "root-password"); err != nil {
		t.Fatalf("SeedAdmin (1st): %v", err)
	}
	if err := auth.SeedAdmin(context.Background(), db, "root", "root-password"); err != nil {
		t.Fatalf("SeedAdmin (2nd): %v", err)
	}

	session, err := svc.Login(context.Background(), "root", "root-password")
	if err != nil {
		t.Fatalf("Login as seeded admin: %v", err)
	}
	if session.User.Role != auth.RoleAdmin {
		t.Fatalf("expected role ADMIN, got %q", session.User.Role)
	}
	if session.User.Status != auth.StatusApproved {
		t.Fatalf("expected status APPROVED, got %q", session.User.Status)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE username = 'root'`).Scan(&n); err != nil {
		t.Fatalf("count admins: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected exactly 1 admin row, got %d", n)
	}
}

// TestSeedAdminSkippedWhenEnvEmpty: if PODIUM_ADMIN_USERNAME is unset,
// SeedAdmin is a no-op (spec.md §6 says env vars are the configuration
// source, not required at all costs).
func TestSeedAdminSkippedWhenEnvEmpty(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	if err := auth.SeedAdmin(context.Background(), db, "", ""); err != nil {
		t.Fatalf("SeedAdmin with empty env: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected zero users, got %d", n)
	}
}

// TestSeedAdminUpdatesPasswordWhenUsernameMatches: if the operator changes
// PODIUM_ADMIN_PASSWORD between boots, the admin row's hash should rotate.
// Per spec.md §6 the env vars are the source of truth.
func TestSeedAdminUpdatesPasswordWhenUsernameMatches(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	svc := auth.NewService(db)

	if err := auth.SeedAdmin(context.Background(), db, "root", "old-password"); err != nil {
		t.Fatalf("SeedAdmin (1st): %v", err)
	}
	if _, err := svc.Login(context.Background(), "root", "old-password"); err != nil {
		t.Fatalf("Login with old password should succeed before rotation: %v", err)
	}

	if err := auth.SeedAdmin(context.Background(), db, "root", "new-password"); err != nil {
		t.Fatalf("SeedAdmin (2nd, rotated): %v", err)
	}
	if _, err := svc.Login(context.Background(), "root", "old-password"); err == nil {
		t.Fatal("old password should no longer work after rotation")
	}
	if _, err := svc.Login(context.Background(), "root", "new-password"); err != nil {
		t.Fatalf("Login with new password: %v", err)
	}
}

// session cookie roundtrip ----------------------------------------------

// TestSessionCookieRoundtrip verifies the cookie helper: write → read the
// token comes back unchanged. This is the contract the api middleware will
// rely on (M1.4) so it lands here.
func TestSessionCookieRoundtrip(t *testing.T) {
	t.Parallel()
	w := httptest.NewRecorder()
	auth.SetSessionCookie(w, "abc.def.ghi")

	r, err := http.NewRequest("GET", "/", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	for _, c := range w.Result().Cookies() {
		r.AddCookie(c)
	}
	got, ok := auth.CookieFromRequest(r)
	if !ok {
		t.Fatal("expected to find session cookie")
	}
	if got != "abc.def.ghi" {
		t.Fatalf("cookie value: got %q want %q", got, "abc.def.ghi")
	}
}

// TestSessionCookieMissing verifies CookieFromRequest returns ok=false for
// requests without the cookie, not an error.
func TestSessionCookieMissing(t *testing.T) {
	t.Parallel()
	r, _ := http.NewRequest("GET", "/", nil)
	_, ok := auth.CookieFromRequest(r)
	if ok {
		t.Fatal("expected no session cookie")
	}
}

// TestSetSessionCookieAttributes pins the security attributes of the cookie
// (HttpOnly, SameSite=Lax, Path=/) per AGENTS.md §6 and spec.md §7. If these
// regress, sessions become XSS-stealable or cross-site-forged.
func TestSetSessionCookieAttributes(t *testing.T) {
	t.Parallel()
	w := httptest.NewRecorder()
	auth.SetSessionCookie(w, "tok")
	c := w.Result().Cookies()
	if len(c) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(c))
	}
	got := c[0]
	if got.Name != auth.SessionCookieName {
		t.Errorf("Name: got %q want %q", got.Name, auth.SessionCookieName)
	}
	if !got.HttpOnly {
		t.Error("expected HttpOnly=true")
	}
	if got.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite: got %v want Lax", got.SameSite)
	}
	if got.Path != "/" {
		t.Errorf("Path: got %q want /", got.Path)
	}
}

// token format -----------------------------------------------------------

// TestSessionTokenFormat: the token must be URL-safe and at least 32 bytes
// of entropy (DECISIONS.md F: "32-byte crypto/rand token, base64url").
func TestSessionTokenFormat(t *testing.T) {
	t.Parallel()
	tok, err := auth.NewSessionToken()
	if err != nil {
		t.Fatalf("NewSessionToken: %v", err)
	}
	if len(tok) < 43 { // 32 bytes → 43 base64url chars (no padding)
		t.Fatalf("token too short: %d chars", len(tok))
	}
	for _, r := range tok {
		if strings.ContainsRune("+/=", r) {
			t.Fatalf("token contains non-url-safe char %q in %q", r, tok)
		}
	}
}

// DeleteUser tests -------------------------------------------------------

// TestDeleteUserRemovesRow verifies a successful hard-delete removes the
// user row. We then re-signup with the same username — if the row were
// still present the UNIQUE constraint would fail.
func TestDeleteUserRemovesRow(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	svc := auth.NewService(db)

	in := auth.SignupInput{
		Username: uniqueUsername(t, "del"),
		Email:    "del@example.com",
		Password: "goodpassword",
	}
	user, err := svc.Signup(context.Background(), in)
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}
	// Seed a separate caller row so callerID (its id) is guaranteed !=
	// targetID (user.ID). SQLite autoincrement gives the first inserted
	// user id 1, and user.ID is also 1 in a fresh DB — so callerID must be
	// a distinct row.
	if err := auth.SeedAdmin(context.Background(), db, "caller-admin", "goodpassword"); err != nil {
		t.Fatalf("SeedAdmin: %v", err)
	}
	caller, err := svc.ListUsers(context.Background())
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	var callerID int64
	for _, u := range caller {
		if u.Username == "caller-admin" {
			callerID = u.ID
			break
		}
	}
	if callerID == 0 {
		t.Fatal("seeded admin not found")
	}
	if callerID == user.ID {
		t.Fatalf("test setup invariant: callerID (%d) must differ from user.ID (%d)", callerID, user.ID)
	}
	if err := svc.DeleteUser(context.Background(), callerID, user.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if _, err := svc.Signup(context.Background(), in); err != nil {
		t.Fatalf("Signup with previously-deleted username should succeed: %v", err)
	}
}

// TestDeleteUserRejectsSelf verifies the self-protection guard. Passing the
// same id for caller and target must return ErrCannotDeleteSelf and leave
// the row in place.
func TestDeleteUserRejectsSelf(t *testing.T) {
	t.Parallel()
	svc := newSvc(t)

	in := auth.SignupInput{
		Username: uniqueUsername(t, "self"),
		Email:    "self@example.com",
		Password: "goodpassword",
	}
	user, err := svc.Signup(context.Background(), in)
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	err = svc.DeleteUser(context.Background(), user.ID, user.ID)
	if !errors.Is(err, auth.ErrCannotDeleteSelf) {
		t.Fatalf("expected ErrCannotDeleteSelf, got %v", err)
	}

	users, err := svc.ListUsers(context.Background())
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	var found bool
	for _, u := range users {
		if u.ID == user.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("user %d was deleted despite self-protection", user.ID)
	}
}

// TestDeleteUserUnknownID verifies the unknown-id path returns
// ErrUserNotFound (same shape as setStatus so the HTTP layer can map it).
func TestDeleteUserUnknownID(t *testing.T) {
	t.Parallel()
	svc := newSvc(t)

	err := svc.DeleteUser(context.Background(), 1, 9999)
	if !errors.Is(err, auth.ErrUserNotFound) {
		t.Fatalf("expected ErrUserNotFound, got %v", err)
	}
}

// TestDeleteUserCascadesSessions verifies the FK CASCADE on sessions: the
// deleted user's active session token must no longer resolve.
func TestDeleteUserCascadesSessions(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	svc := auth.NewService(db)

	in := auth.SignupInput{
		Username: uniqueUsername(t, "cascade"),
		Email:    "cascade@example.com",
		Password: "goodpassword",
	}
	user, err := svc.Signup(context.Background(), in)
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}
	_ = svc.ApproveUser(context.Background(), user.ID)
	sess, err := svc.Login(context.Background(), in.Username, in.Password)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	// Seed a separate caller row so callerID != user.ID (which is 1 in a
	// fresh DB — the same id hard-coded below would trip the self-protection).
	if err := auth.SeedAdmin(context.Background(), db, "caller-admin", "goodpassword"); err != nil {
		t.Fatalf("SeedAdmin: %v", err)
	}
	caller, err := svc.ListUsers(context.Background())
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	var callerID int64
	for _, u := range caller {
		if u.Username == "caller-admin" {
			callerID = u.ID
			break
		}
	}
	if callerID == user.ID {
		t.Fatalf("test setup invariant: callerID (%d) must differ from user.ID (%d)", callerID, user.ID)
	}

	if err := svc.DeleteUser(context.Background(), callerID, user.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if _, err := svc.LoadSession(context.Background(), sess.Token); !errors.Is(err, auth.ErrSessionNotFound) {
		t.Fatalf("expected session to be cascaded away, got %v", err)
	}
}

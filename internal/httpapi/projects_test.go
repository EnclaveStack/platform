package httpapi_test

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/enclavestack/platform/internal/server"
	"github.com/enclavestack/platform/internal/testenv"
	"github.com/enclavestack/platform/pkg/api"
	"github.com/enclavestack/platform/pkg/client"
)

// Projects as namespaces and roles (L2-009 PRJ-*, IAM-*, TOK-*; ADR-035, ADR-036).

func mustCode(t *testing.T, err error, want api.ErrorCode, what string) {
	t.Helper()
	if got := client.Code(err); got != want {
		t.Fatalf("%s: want %s, got %v", what, want, err)
	}
}

func TestTheSameNameLivesInTwoProjectsWithTheirOwnAddresses(t *testing.T) {
	env := testenv.Start(t)
	admin, _ := env.Token(t, "")
	ctx := context.Background()
	if _, err := admin.CreateProject(ctx, api.CreateProject{Name: "shop", Description: "the shop"}); err != nil {
		t.Fatal(err)
	}
	shop := admin.InProject("shop")
	for _, c := range []*client.Client{admin, shop} {
		sec, err := c.CreateResource(ctx, api.KindSecret, "stripe", api.SecretSpec{Value: "sk"})
		if err != nil {
			t.Fatalf("secret in %s: %v", c.Project, err)
		}
		testenv.WaitReady(t, c, sec.ID)
		app, err := c.CreateResource(ctx, api.KindApp, "web", api.AppSpec{Image: "nginx", Port: 80, SecretEnv: map[string]string{"STRIPE": "stripe"}})
		if err != nil {
			t.Fatalf("app in %s: %v", c.Project, err)
		}
		testenv.WaitReady(t, c, app.ID)
	}
	def, _ := admin.FindResource(ctx, api.KindApp, "web")
	inShop, _ := shop.FindResource(ctx, api.KindApp, "web")
	if def.ID == inShop.ID || def.Project != api.DefaultProject || inShop.Project != "shop" {
		t.Fatalf("not two apps: %+v %+v", def, inShop)
	}
	if def.Outputs["internalHost"] != "es-app-web" || inShop.Outputs["internalHost"] != "es-app-web.shop" {
		t.Errorf("internal hosts: %v / %v (default keeps its v0.6 name; others carry the project)", def.Outputs["internalHost"], inShop.Outputs["internalHost"])
	}

	// References stay inside a project (PRJ-04).
	_, err := shop.CreateResource(ctx, api.KindApp, "api", api.AppSpec{Image: "nginx", SecretEnv: map[string]string{"X": "only-in-default"}})
	mustCode(t, err, api.CodeValidationFailed, "a reference to a missing secret")
	if s, err := admin.CreateResource(ctx, api.KindSecret, "only-in-default", api.SecretSpec{Value: "v"}); err == nil {
		testenv.WaitReady(t, admin, s.ID)
	}
	_, err = shop.CreateResource(ctx, api.KindApp, "api", api.AppSpec{Image: "nginx", SecretEnv: map[string]string{"X": "only-in-default"}})
	mustCode(t, err, api.CodeValidationFailed, "a reference to another project's secret")

	// Bucket names are machine-wide (PRJ-03).
	if b, err := admin.CreateResource(ctx, api.KindBucket, "uploads", api.BucketSpec{}); err != nil {
		t.Fatal(err)
	} else {
		testenv.WaitReady(t, admin, b.ID)
	}
	_, err = shop.CreateResource(ctx, api.KindBucket, "uploads", api.BucketSpec{})
	mustCode(t, err, api.CodeNameTaken, "a bucket name used by another project")
	b, err := shop.CreateResource(ctx, api.KindBucket, "uploads-shop", api.BucketSpec{})
	if err != nil {
		t.Fatal(err)
	}
	testenv.WaitReady(t, shop, b.ID)
	if b, _ = shop.GetResource(ctx, b.ID); b.Outputs["credentialsSecret"] != "objectstore-credentials" {
		t.Errorf("bucket outputs %v", b.Outputs)
	}
	if _, err := shop.FindResource(ctx, api.KindSecret, "objectstore-credentials"); err != nil {
		t.Errorf("the shop project has no bucket credentials of its own: %v", err)
	}
	if got := env.Fake.ProjectStoreAccess("es-p-shop"); len(got) != 1 || got[0] != "uploads-shop" {
		t.Errorf("the shop user reaches %v; want only uploads-shop (PRJ-06)", got)
	}

	// Lists are per project; --all-projects sees both (PRJ-08).
	if l, _ := shop.ListAllResources(ctx, api.KindApp); len(l) != 1 || l[0].Project != "shop" {
		t.Errorf("shop's apps: %+v", l)
	}
	if l, _ := admin.ListAllResourcesIn(ctx, api.KindApp, true); len(l) != 2 {
		t.Errorf("all apps: %d", len(l))
	}

	// A project with resources can't be deleted; default never (PRJ-01, PRJ-07).
	mustCode(t, admin.DeleteProject(ctx, "shop"), api.CodeProjectNotEmpty, "delete a non-empty project")
	mustCode(t, admin.DeleteProject(ctx, api.DefaultProject), api.CodeActionNotSupported, "delete default")
	_, err = admin.InProject("nowhere").CreateResource(ctx, api.KindSecret, "x-secret", api.SecretSpec{Value: "v"})
	mustCode(t, err, api.CodeValidationFailed, "create in a project that doesn't exist")
}

func TestRolesLimitWhatPeopleAndTokensDo(t *testing.T) {
	env := testenv.Start(t)
	admin, _ := env.Token(t, "")
	ctx := context.Background()
	if _, err := admin.CreateProject(ctx, api.CreateProject{Name: "staging"}); err != nil {
		t.Fatal(err)
	}
	secret, err := admin.CreateResource(ctx, api.KindSecret, "admin-only", api.SecretSpec{Value: "v"})
	if err != nil {
		t.Fatal(err)
	}

	// Invite a contractor as an editor of staging (IAM-04).
	inv, err := admin.InviteUser(ctx, api.InviteUser{Email: "contractor@example.test", ProjectRoles: map[string]string{"staging": api.RoleEditor}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = admin.InviteUser(ctx, api.InviteUser{Email: "Contractor@example.test", ProjectRoles: map[string]string{"staging": api.RoleViewer}})
	mustCode(t, err, api.CodeEmailTaken, "invite the same email twice")
	u, _ := url.Parse(inv.URL)
	code := u.Query().Get("code")
	if info, err := client.New(env.URL, "").InviteInfo(ctx, code); err != nil || info.Email != "contractor@example.test" {
		t.Fatalf("invite info %+v %v", info, err)
	}
	if _, err := client.New(env.URL, "").SignIn(ctx, "contractor@example.test", "anything-goes-here"); client.Code(err) != api.CodeInvalidCredentials {
		t.Errorf("an open invite signs in: %v", err)
	}
	contractor := client.New(env.URL, "")
	sess, err := contractor.AcceptInvite(ctx, code, "contractor-password-1")
	if err != nil || sess.ProjectRoles["staging"] != api.RoleEditor || sess.MachineRole != nil {
		t.Fatalf("accept: %+v %v", sess, err)
	}
	_, err = client.New(env.URL, "").AcceptInvite(ctx, code, "contractor-password-2")
	mustCode(t, err, api.CodeInviteInvalid, "use an invite twice")

	// The contractor sees staging only; default's resources are "not found" (PRJ-08, IAM-10).
	if ps, _ := contractor.ListProjects(ctx); len(ps) != 1 || ps[0].Name != "staging" {
		t.Errorf("projects seen: %+v", ps)
	}
	_, err = contractor.GetResource(ctx, secret.ID)
	mustCode(t, err, api.CodeNotFound, "read another project's resource")
	_, err = contractor.CreateResource(ctx, api.KindSecret, "nope", api.SecretSpec{Value: "v"})
	mustCode(t, err, api.CodeValidationFailed, "create in an invisible project")
	stg := contractor.InProject("staging")
	s, err := stg.CreateResource(ctx, api.KindSecret, "mine", api.SecretSpec{Value: "v"})
	if err != nil {
		t.Fatalf("editor creates in staging: %v", err)
	}
	if _, err := stg.RevealSecret(ctx, s.ID); err != nil {
		t.Errorf("an editor reveals: %v", err)
	}
	mustCode(t, contractor.DeleteProject(ctx, "staging"), api.CodeForbidden, "an editor deletes a project")
	_, err = contractor.UpdateMachine(ctx, api.MachineSettings{})
	mustCode(t, err, api.CodeForbidden, "an editor changes machine settings")
	_, err = contractor.ListUsers(ctx)
	mustCode(t, err, api.CodeForbidden, "an editor lists people")

	// A viewer token of the contractor reads but can't write; it can't exceed the user (TOK-01, TOK-02).
	_, err = contractor.CreateToken(ctx, api.CreateTokenRequest{Name: "too-much", Role: api.RoleAdmin})
	mustCode(t, err, api.CodeValidationFailed, "a token stronger than its user")
	vt, err := contractor.CreateToken(ctx, api.CreateTokenRequest{Name: "read", Role: api.RoleViewer})
	if err != nil || vt.Token.Projects != nil || vt.Token.Role != api.RoleViewer {
		t.Fatalf("viewer token %+v %v", vt.Token, err)
	}
	viewer := client.New(env.URL, vt.Secret).InProject("staging")
	if _, err := viewer.GetResource(ctx, s.ID); err != nil {
		t.Errorf("a viewer reads: %v", err)
	}
	_, err = viewer.RevealSecret(ctx, s.ID)
	mustCode(t, err, api.CodeForbidden, "a viewer reveals a secret")
	_, err = viewer.DeleteResource(ctx, s.ID, nil)
	mustCode(t, err, api.CodeForbidden, "a viewer deletes")
	if !strings.Contains(err.Error(), "editor role in staging") {
		t.Errorf("the refusal names the role and project: %v", err)
	}

	// An admin's token limited to one project sees nothing else (TOK-05).
	lt, err := admin.CreateToken(ctx, api.CreateTokenRequest{Name: "ci", Role: api.RoleEditor, Projects: []string{"staging"}})
	if err != nil {
		t.Fatal(err)
	}
	ci := client.New(env.URL, lt.Secret)
	_, err = ci.GetResource(ctx, secret.ID)
	mustCode(t, err, api.CodeNotFound, "a staging token reads default")
	if ps, _ := ci.ListProjects(ctx); len(ps) != 1 {
		t.Errorf("a staging token sees %d projects", len(ps))
	}

	// Demoting the contractor applies at the next request; removing them ends everything (IAM-06).
	viewerRole := api.RoleViewer
	if _, err := admin.UpdateUser(ctx, sess.User.ID, false, nil, map[string]*string{"staging": &viewerRole}); err != nil {
		t.Fatal(err)
	}
	_, err = stg.CreateResource(ctx, api.KindSecret, "after-demotion", api.SecretSpec{Value: "v"})
	mustCode(t, err, api.CodeForbidden, "a demoted editor writes")
	if err := admin.DeleteUser(ctx, sess.User.ID); err != nil {
		t.Fatal(err)
	}
	_, err = contractor.Session(ctx)
	mustCode(t, err, api.CodeUnauthenticated, "a removed person's session")
	_, err = viewer.GetResource(ctx, s.ID)
	mustCode(t, err, api.CodeUnauthenticated, "a removed person's token")

	// The last machine admin stays (IAM-03), and the audit list recorded it all (IAM-09).
	me, err := admin.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mustCode(t, admin.DeleteUser(ctx, me.User.ID), api.CodeLastAdmin, "remove the last admin")
	_, err = admin.UpdateUser(ctx, me.User.ID, true, nil, nil)
	mustCode(t, err, api.CodeLastAdmin, "demote the last admin")
	page, err := admin.Audit(ctx, 200, "")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range page.Items {
		seen[e.Action] = true
	}
	for _, want := range []string{"project_created", "user_invited", "invite_accepted", "token_created", "user_roles_changed", "user_removed", "secret_revealed"} {
		if !seen[want] {
			t.Errorf("audit has no %s entry", want)
		}
	}
	_, err = ci.Audit(ctx, 10, "")
	mustCode(t, err, api.CodeForbidden, "a project token reads the audit list")
}

func TestPasswordChangeAndReset(t *testing.T) {
	env := testenv.Start(t)
	admin := env.Setup(t)
	ctx := context.Background()
	other := client.New(env.URL, "")
	if _, err := other.SignIn(ctx, testenv.AdminEmail, testenv.AdminPassword); err != nil {
		t.Fatal(err)
	}
	err := admin.ChangePassword(ctx, api.ChangePassword{CurrentPassword: "wrong-password-x", NewPassword: "a-new-long-password"})
	mustCode(t, err, api.CodeValidationFailed, "wrong current password")
	if err := admin.ChangePassword(ctx, api.ChangePassword{CurrentPassword: testenv.AdminPassword, NewPassword: "a-new-long-password"}); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Session(ctx); err != nil {
		t.Errorf("the changing session ended: %v", err)
	}
	_, err = other.Session(ctx)
	mustCode(t, err, api.CodeUnauthenticated, "another session after a password change")
	me, _ := admin.Session(ctx)
	inv, err := admin.ResetUser(ctx, me.User.ID)
	if err != nil || !strings.Contains(inv.URL, "/invite?code=") {
		t.Fatalf("reset %+v %v", inv, err)
	}
	_, err = admin.Session(ctx)
	mustCode(t, err, api.CodeUnauthenticated, "a session after a reset")
}

// TestInvitesAreEmailedWhenTheMachineSaysHow: with inviteEmail set, invites and resets go by email
// through an email service; without it, or when sending fails, the link is still answered (IAM-04).
func TestInvitesAreEmailedWhenTheMachineSaysHow(t *testing.T) {
	ctx := context.Background()
	env := testenv.Start(t)
	admin, _ := env.Token(t, "")
	svc := testenv.WaitReady(t, admin, func() string {
		r, err := admin.CreateResource(ctx, api.KindEmail, "mail", api.EmailSpec{})
		if err != nil {
			t.Fatal(err)
		}
		return r.ID
	}())

	// Not set up: the link only.
	inv, err := admin.InviteUser(ctx, api.InviteUser{Email: "first@example.test", MachineRole: ptr(api.RoleViewer)})
	if err != nil || inv.Emailed || inv.EmailError != "" {
		t.Fatalf("without inviteEmail: %+v %v", inv, err)
	}

	_, err = admin.UpdateMachine(ctx, api.MachineSettings{InviteEmail: &api.InviteEmail{Service: "nope", From: "hc@example.test"}})
	mustCode(t, err, api.CodeValidationFailed, "an email service that doesn't exist")
	m, err := admin.UpdateMachine(ctx, api.MachineSettings{InviteEmail: &api.InviteEmail{Service: "mail", From: "hc@example.test"}})
	if err != nil || m.InviteEmail == nil || m.InviteEmail.Project != api.DefaultProject {
		t.Fatalf("set inviteEmail: %+v %v", m.InviteEmail, err)
	}
	// Changing another setting keeps it (absent keeps; L3-002 §10.3).
	if m, err = admin.UpdateMachine(ctx, api.MachineSettings{PublicAddress: ptr("203.0.113.7")}); err != nil || m.InviteEmail == nil {
		t.Fatalf("inviteEmail after another change: %+v %v", m.InviteEmail, err)
	}

	inv, err = admin.InviteUser(ctx, api.InviteUser{Email: "dev@example.test", MachineRole: ptr(api.RoleEditor)})
	if err != nil || !inv.Emailed {
		t.Fatalf("emailed invite: %+v %v", inv, err)
	}
	list, err := admin.Emails(ctx, svc.ID, 10)
	if err != nil || len(list.Items) != 1 {
		t.Fatalf("messages: %+v %v", list, err)
	}
	msg, err := admin.Email(ctx, svc.ID, list.Items[0].ID)
	if err != nil || msg.To[0] != "dev@example.test" || !strings.Contains(msg.Subject, "invited") || !strings.Contains(msg.Text, inv.URL) {
		t.Fatalf("the invite email: %+v %v", msg, err)
	}

	// A reset of an active user is emailed as a reset.
	code := mustQuery(t, inv.URL, "code")
	if _, err := client.New(env.URL, "").AcceptInvite(ctx, code, "a-long-password-1"); err != nil {
		t.Fatal(err)
	}
	reset, err := admin.ResetUser(ctx, inv.User.ID)
	if err != nil || !reset.Emailed {
		t.Fatalf("reset: %+v %v", reset, err)
	}
	list, _ = admin.Emails(ctx, svc.ID, 10)
	if len(list.Items) != 2 || !strings.Contains(list.Items[0].Subject, "Reset your password") {
		t.Errorf("reset email: %+v", list.Items)
	}

	// Sending fails: the invite still succeeds and says why.
	env.Mail.SetDown(true)
	inv, err = admin.InviteUser(ctx, api.InviteUser{Email: "third@example.test", MachineRole: ptr(api.RoleViewer)})
	if err != nil || inv.Emailed || !strings.Contains(inv.EmailError, "Send it yourself") || inv.URL == "" {
		t.Errorf("a failed send: %+v %v", inv, err)
	}
	env.Mail.SetDown(false)

	// null turns it off.
	if m, err = admin.UpdateMachine(ctx, api.MachineSettings{ClearInviteEmail: true}); err != nil || m.InviteEmail != nil {
		t.Errorf("clear: %+v %v", m.InviteEmail, err)
	}
}

func ptr[T any](v T) *T { return &v }

func mustQuery(t *testing.T, raw, key string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get(key)
}

// With --console-url, invite links point there, not at the address the admin happened to use (IAM-04).
func TestInviteLinksUseTheConsoleURL(t *testing.T) {
	ctx := context.Background()
	env := testenv.Start(t, testenv.WithConfig(func(c *server.Config) { c.ConsoleURL = "https://enclave.example.com/" }))
	admin, _ := env.Token(t, "")
	inv, err := admin.InviteUser(ctx, api.InviteUser{Email: "remote@example.test", MachineRole: ptr(api.RoleViewer)})
	if err != nil || !strings.HasPrefix(inv.URL, "https://enclave.example.com/invite?code=") {
		t.Errorf("invite link: %q %v", inv.URL, err)
	}
}

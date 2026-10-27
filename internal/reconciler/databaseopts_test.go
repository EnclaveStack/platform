package reconciler_test

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"testing"

	"github.com/enclavestack/platform/internal/testenv"
	"github.com/enclavestack/platform/pkg/api"
)

// Database TLS, setup scripts and exposed access (ADR-058, L2-016 DBX-01 – DBX-05).
func TestDatabaseTLS(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	db := create(t, c, api.KindDatabase, "orders", api.DatabaseSpec{TLS: true, InitScript: "CREATE TABLE t (id int);"})
	ca, _ := db.Outputs["caCertificate"].(string)
	block, _ := pem.Decode([]byte(ca))
	if block == nil || db.Outputs["tls"] != true || db.Outputs["certificateExpiresAt"] == nil || db.Outputs["initScript"] != "ran" {
		t.Fatalf("outputs %v", db.Outputs)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !cert.IsCA || cert.Subject.CommonName != "EnclaveStack databases CA" {
		t.Errorf("CA %v %v", cert, err)
	}
	// The same certificate across a reconcile: the container isn't recreated for nothing.
	expires := db.Outputs["certificateExpiresAt"]
	if _, err := c.Action(ctx, db.ID, api.ActionReconcile); err != nil {
		t.Fatal(err)
	}
	if again := waitReadyWithin(t, c, db.ID, 20e9); again.Outputs["certificateExpiresAt"] != expires {
		t.Errorf("certificate reissued: %v → %v", expires, again.Outputs["certificateExpiresAt"])
	}
	// The setup script ran once and can't change; TLS can be turned off.
	if _, err := c.UpdateResource(ctx, db.ID, map[string]any{"initScript": "DROP TABLE t;"}, nil); apiCode(err) != api.CodeImmutableField {
		t.Errorf("changing the setup script: %v", err)
	}
	upd, err := c.UpdateResource(ctx, db.ID, map[string]any{"tls": false}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if off := waitReadyWithin(t, c, upd.ID, 20e9); off.Outputs["tls"] != nil || off.Outputs["caCertificate"] != nil {
		t.Errorf("TLS off: %v", off.Outputs)
	}
	for spec, want := range map[string]api.DatabaseSpec{
		"spec.tls":     {Engine: api.EngineMongoDB, Version: "7.0", TLS: true},
		"spec.exposed": {Exposed: true},
	} {
		if _, err := c.CreateResource(ctx, api.KindDatabase, "nope", want); apiCode(err) != api.CodeValidationFailed {
			t.Errorf("%s: %v", spec, err)
		}
	}
}

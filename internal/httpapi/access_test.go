package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/testenv"
	"github.com/enclavestack/platform/pkg/api"
	"github.com/enclavestack/platform/pkg/client"
)

// Per-app bucket access through the API, on the simulated engine (L2-011 BKA-01 – BKA-08; the real object
// store is in internal/engine/docker).
func TestAnAppGetsKeysOfItsOwnForItsBuckets(t *testing.T) {
	env := testenv.Start(t)
	admin := env.Setup(t)
	ctx := context.Background()
	for _, b := range []string{"media", "invoices"} {
		res, err := admin.CreateResource(ctx, api.KindBucket, b, api.BucketSpec{})
		if err != nil {
			t.Fatal(err)
		}
		testenv.WaitReady(t, admin, res.ID)
	}

	// Created with read on media: its own user, its own keys, the variables in its containers.
	app, err := admin.CreateResource(ctx, api.KindApp, "storefront", api.AppSpec{Image: "nginx:1.27", Port: 80,
		BucketAccess: []api.BucketGrant{{Bucket: "media", Access: api.BucketRead}}})
	if err != nil {
		t.Fatal(err)
	}
	app = testenv.WaitReady(t, admin, app.ID)
	if app.Outputs[api.OutputBucketCredentialsSecret] != "storefront-bucket-credentials" {
		t.Errorf("outputs: %v", app.Outputs)
	}
	user := domain.StoreUserFor(app.ID)
	if got := env.Fake.StoreUserAccess(user); len(got) != 1 || got[0] != (api.BucketGrant{Bucket: "media", Access: api.BucketRead}) {
		t.Errorf("the store user reaches %+v", got)
	}
	keys := revealKeys(t, admin, "storefront-bucket-credentials")
	if keys.AccessKey != user || len(keys.SecretKey) < 24 {
		t.Errorf("keys %q / %d characters", keys.AccessKey, len(keys.SecretKey))
	}
	got := env.Fake.EnvOf(app.ID)
	want := map[string]string{"AWS_ACCESS_KEY_ID": keys.AccessKey, "AWS_SECRET_ACCESS_KEY": keys.SecretKey, "AWS_REGION": "us-east-1",
		"AWS_ENDPOINT_URL_S3": "http://es-system-objectstore:9000", "AWS_ENDPOINT_URL": "http://es-system-objectstore:9000"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}

	// The keys appear in no response or event.
	raw, _ := json.Marshal(app)
	evs, _ := admin.Events(ctx, app.ID, 50)
	rawEvents, _ := json.Marshal(evs)
	if strings.Contains(string(raw)+string(rawEvents), keys.SecretKey) {
		t.Error("the secret key leaked into the app or its events")
	}
	if !strings.Contains(string(rawEvents), "Bucket access: media read.") {
		t.Errorf("events: %s", rawEvents)
	}

	// Read to read and write, and a second bucket: the same keys, a wider policy.
	app, err = admin.UpdateResource(ctx, app.ID, map[string]any{"bucketAccess": []api.BucketGrant{
		{Bucket: "media", Access: api.BucketReadWrite}, {Bucket: "invoices", Access: api.BucketRead}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	testenv.WaitReady(t, admin, app.ID)
	if got := env.Fake.StoreUserAccess(user); len(got) != 2 || got[0].Access != api.BucketReadWrite {
		t.Errorf("after the change the store user reaches %+v", got)
	}
	if again := revealKeys(t, admin, "storefront-bucket-credentials"); again != keys {
		t.Error("changing the access changed the keys")
	}

	// A bucket it can access can't be deleted.
	inv, _ := admin.FindResource(ctx, api.KindBucket, "invoices")
	_, err = admin.DeleteResource(ctx, inv.ID, nil)
	mustCode(t, err, api.CodeResourceInUse, "deleting a bucket an app can access")
	var ae *api.Error
	if !errors.As(err, &ae) || !strings.Contains(fmt.Sprint(ae.Details), "app/storefront") {
		t.Errorf("the refusal doesn't name the app: %+v", err)
	}

	// Removing every grant: the user goes, the keys are deleted, the variables leave the container.
	if _, err := admin.UpdateResource(ctx, app.ID, map[string]any{"bucketAccess": []api.BucketGrant{}}, nil); err != nil {
		t.Fatal(err)
	}
	app = testenv.WaitReady(t, admin, app.ID)
	if env.Fake.StoreUserAccess(user) != nil {
		t.Error("the store user outlived its access")
	}
	if _, err := admin.FindResource(ctx, api.KindSecret, "storefront-bucket-credentials"); client.Code(err) != api.CodeNotFound {
		t.Errorf("the keys outlived the access: %v", err)
	}
	if _, ok := app.Outputs[api.OutputBucketCredentialsSecret]; ok || env.Fake.EnvOf(app.ID)["AWS_ACCESS_KEY_ID"] != "" {
		t.Errorf("after removal: outputs %v, env %v", app.Outputs, env.Fake.EnvOf(app.ID))
	}
	// …and now the bucket can go.
	if _, err := admin.DeleteResource(ctx, inv.ID, nil); err != nil {
		t.Errorf("deleting the bucket once nothing uses it: %v", err)
	}

	// Given again, then the app deleted: the user and the keys go with it.
	app, err = admin.UpdateResource(ctx, app.ID, map[string]any{"bucketAccess": []api.BucketGrant{{Bucket: "media", Access: api.BucketRead}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	testenv.WaitReady(t, admin, app.ID)
	if env.Fake.StoreUserAccess(user) == nil {
		t.Fatal("access given again isn't applied")
	}
	if _, err := admin.DeleteResource(ctx, app.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := admin.WaitGone(ctx, app.ID, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if env.Fake.StoreUserAccess(user) != nil {
		t.Error("the store user outlived its app")
	}
	if _, err := admin.FindResource(ctx, api.KindSecret, "storefront-bucket-credentials"); client.Code(err) != api.CodeNotFound {
		t.Errorf("the keys outlived the app: %v", err)
	}
}

// Jobs: one from an image has its own access; one that runs as an app uses the app's (BKA-05).
func TestJobsGetBucketAccessOfTheirOwnOrTheirApps(t *testing.T) {
	env := testenv.Start(t)
	admin := env.Setup(t)
	ctx := context.Background()
	b, err := admin.CreateResource(ctx, api.KindBucket, "reports", api.BucketSpec{})
	if err != nil {
		t.Fatal(err)
	}
	testenv.WaitReady(t, admin, b.ID)

	own, err := admin.CreateResource(ctx, api.KindJob, "export", api.JobSpec{Schedule: "@daily", Image: "busybox:1.36", Command: []string{"true"},
		BucketAccess: []api.BucketGrant{{Bucket: "reports", Access: api.BucketReadWrite}}})
	if err != nil {
		t.Fatal(err)
	}
	testenv.WaitReady(t, admin, own.ID)
	keys := revealKeys(t, admin, "export-bucket-credentials")
	// Given as soon as the job is ready, before any run (BKA-02).
	if got := env.Fake.StoreUserAccess(keys.AccessKey); len(got) != 1 {
		t.Errorf("before its first run the job's store user reaches %+v", got)
	}
	if run := runJob(t, env, admin, own.ID); run.Env["AWS_ACCESS_KEY_ID"] != keys.AccessKey || run.Env["AWS_SECRET_ACCESS_KEY"] != keys.SecretKey {
		t.Errorf("the image job's run env: %v", run.Env)
	}
	if got := env.Fake.StoreUserAccess(keys.AccessKey); len(got) != 1 || got[0].Access != api.BucketReadWrite {
		t.Errorf("the job's store user reaches %+v", got)
	}
	// Taken away at once when the job's access is removed (BKA-06).
	if _, err := admin.UpdateResource(ctx, own.ID, map[string]any{"bucketAccess": nil}, nil); err != nil {
		t.Fatal(err)
	}
	testenv.WaitReady(t, admin, own.ID)
	if env.Fake.StoreUserAccess(keys.AccessKey) != nil {
		t.Error("the job's store user outlived its access")
	}
	if _, err := admin.FindResource(ctx, api.KindSecret, "export-bucket-credentials"); client.Code(err) != api.CodeNotFound {
		t.Errorf("the job's keys outlived its access: %v", err)
	}

	app, err := admin.CreateResource(ctx, api.KindApp, "api", api.AppSpec{Image: "nginx:1.27", Port: 80,
		BucketAccess: []api.BucketGrant{{Bucket: "reports", Access: api.BucketRead}}})
	if err != nil {
		t.Fatal(err)
	}
	testenv.WaitReady(t, admin, app.ID)
	appKeys := revealKeys(t, admin, "api-bucket-credentials")
	asApp, err := admin.CreateResource(ctx, api.KindJob, "cleanup", api.JobSpec{Schedule: "@daily", App: "api", Command: []string{"true"}})
	if err != nil {
		t.Fatal(err)
	}
	testenv.WaitReady(t, admin, asApp.ID)
	if run := runJob(t, env, admin, asApp.ID); run.Env["AWS_ACCESS_KEY_ID"] != appKeys.AccessKey {
		t.Errorf("the app job's run env: %v", run.Env)
	}
	if _, err := admin.FindResource(ctx, api.KindSecret, "cleanup-bucket-credentials"); client.Code(err) != api.CodeNotFound {
		t.Errorf("an app job got keys of its own: %v", err)
	}

	// Refused: access of its own on an app job; an AWS variable the app's access sets.
	_, err = admin.CreateResource(ctx, api.KindJob, "bad-own", api.JobSpec{Schedule: "@daily", App: "api",
		BucketAccess: []api.BucketGrant{{Bucket: "reports", Access: api.BucketRead}}})
	mustCode(t, err, api.CodeValidationFailed, "an app job with access of its own")
	_, err = admin.CreateResource(ctx, api.KindJob, "bad-env", api.JobSpec{Schedule: "@daily", App: "api", Env: map[string]string{"AWS_REGION": "eu-west-1"}})
	mustCode(t, err, api.CodeValidationFailed, "an app job setting a variable its app's access sets")
}

// Requests the server refuses (BKA-01, BKA-04, BKA-07).
func TestBucketAccessRefusals(t *testing.T) {
	env := testenv.Start(t)
	admin := env.Setup(t)
	ctx := context.Background()
	b, err := admin.CreateResource(ctx, api.KindBucket, "media", api.BucketSpec{})
	if err != nil {
		t.Fatal(err)
	}
	testenv.WaitReady(t, admin, b.ID)
	for name, spec := range map[string]api.AppSpec{
		"missing":   {Image: "nginx", BucketAccess: []api.BucketGrant{{Bucket: "nowhere", Access: api.BucketRead}}},
		"twice":     {Image: "nginx", BucketAccess: []api.BucketGrant{{Bucket: "media", Access: api.BucketRead}, {Bucket: "media", Access: api.BucketReadWrite}}},
		"level":     {Image: "nginx", BucketAccess: []api.BucketGrant{{Bucket: "media", Access: "write"}}},
		"overrides": {Image: "nginx", Env: map[string]string{"AWS_ACCESS_KEY_ID": "mine"}, BucketAccess: []api.BucketGrant{{Bucket: "media", Access: api.BucketRead}}},
	} {
		_, err := admin.CreateResource(ctx, api.KindApp, "bad-"+name, spec)
		mustCode(t, err, api.CodeValidationFailed, name)
	}
	// Another project's bucket of the same name doesn't count.
	if _, err := admin.CreateProject(ctx, api.CreateProject{Name: "shop"}); err != nil {
		t.Fatal(err)
	}
	other := admin.InProject("shop")
	_, err = other.CreateResource(ctx, api.KindApp, "elsewhere", api.AppSpec{Image: "nginx", BucketAccess: []api.BucketGrant{{Bucket: "media", Access: api.BucketRead}}})
	mustCode(t, err, api.CodeValidationFailed, "a bucket of another project")
}

type bucketKeys struct{ AccessKey, SecretKey string }

func revealKeys(t *testing.T, c *client.Client, name string) bucketKeys {
	t.Helper()
	sec, err := c.FindResource(context.Background(), api.KindSecret, name)
	if err != nil {
		t.Fatalf("find %s: %v", name, err)
	}
	v, err := c.RevealSecret(context.Background(), sec.ID)
	if err != nil {
		t.Fatalf("reveal %s: %v", name, err)
	}
	var k bucketKeys
	if err := json.Unmarshal([]byte(v), &k); err != nil {
		t.Fatalf("%s holds %q", name, v)
	}
	return k
}

// runJob runs a job once, waits for the run to end and returns what it started with.
func runJob(t *testing.T, env *testenv.Env, c *client.Client, id string) engine.JobRun {
	t.Helper()
	ctx := context.Background()
	run, err := c.RunNow(ctx, id)
	if err != nil {
		t.Fatalf("run now: %v", err)
	}
	testenv.Eventually(t, 10*time.Second, "the run to end", func() bool {
		d, err := c.GetRun(ctx, id, run.ID)
		return err == nil && d.Status != api.RunRunning
	})
	jr, ok := env.Fake.JobRunOf(run.ID)
	if !ok {
		t.Fatalf("run %s never started", run.ID)
	}
	return jr
}

//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/enclavestack/platform/pkg/api"
	"github.com/enclavestack/platform/pkg/client"
)

// awsCLI is the official AWS CLI, pinned: it reads only the standard variables, so it proves what an app's
// own S3 code would see (L2-011 BKA-04).
const awsCLI = "amazon/aws-cli:2.27.50"

// Per-app bucket access for real (L2-011 §4): the real server, Docker and object store, and a real S3 client
// in the containers, configured by nothing but the five variables EnclaveStack gives them.
func TestBucketAccessForRealWithTheAWSCLI(t *testing.T) {
	requireTools(t, "docker")
	if exec.Command("docker", "inspect", "es-system-objectstore").Run() == nil {
		t.Skip("an object store is already running on this Docker; this test would replace its keys")
	}
	s := startServer(t, t.TempDir(), freePort(t))
	token, c := setupAndToken(t, s)
	ctx := context.Background()
	id := suffix()
	media, invoices := "e2e-media-"+id, "e2e-invoices-"+id
	reader, writer := "e2e-reader-"+id, "e2e-writer-"+id
	t.Cleanup(func() {
		for _, kr := range [][2]string{{"job", reader}, {"job", writer}, {"bucket", media}, {"bucket", invoices}} {
			cli(t, s, token, "", "delete", kr[0], kr[1], "--yes", "--wait", "--timeout", "3m")
		}
		_ = exec.Command("docker", "rm", "-f", "es-system-objectstore").Run()
		_ = exec.Command("docker", "volume", "rm", "-f", "es-system-objectstore-data").Run()
	})
	mustCLI(t, s, token, "", "bucket", "create", media, "--wait", "--timeout", "5m")
	mustCLI(t, s, token, "", "bucket", "create", invoices, "--wait", "--timeout", "5m")

	// Seed both buckets with the store's own keys.
	var root struct{ AccessKey, SecretKey string }
	_ = json.Unmarshal([]byte(strings.TrimSpace(mustCLI(t, s, token, "", "secret", "get", "objectstore-credentials", "--reveal"))), &root)
	endpoint := strings.TrimPrefix(fmt.Sprint(getJSON(t, s, token, "bucket", media).Outputs["endpoint"]), "http://")
	rootStore, err := minio.New(endpoint, &minio.Options{Creds: credentials.NewStaticV4(root.AccessKey, root.SecretKey, "")})
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range []string{media, invoices} {
		if _, err := rootStore.PutObject(ctx, b, "seed.txt", strings.NewReader("seed"), 4, minio.PutObjectOptions{}); err != nil {
			t.Fatalf("seed %s: %v", b, err)
		}
	}

	// A job that may only read media: it lists and reads it, can't write to it, can't see invoices. A refusal
	// reads AccessDenied, or 403 for a HEAD request, which has no body.
	mustCLI(t, s, token, "", "job", "create", reader, "--schedule", "0 0 1 1 *", "--image", awsCLI, "--bucket", media+":read", "--wait", "--timeout", "5m",
		"--", "s3", "ls", "s3://"+media+"/")
	ran := runOnce(t, c, reader)
	if ran.Status != api.RunSucceeded || !strings.Contains(ran.OutputTail, "seed.txt") {
		t.Fatalf("reading its bucket with read access: %s %s\n%s", ran.Status, ran.Message, ran.OutputTail)
	}
	for _, cmd := range [][]string{
		{"s3", "cp", "/etc/hostname", "s3://" + media + "/written-by-reader.txt"},
		{"s3", "ls", "s3://" + invoices + "/"},
		{"s3", "cp", "s3://" + invoices + "/seed.txt", "-"},
		{"s3", "mb", "s3://e2e-made-" + id},
	} {
		mustCLI(t, s, token, "", append([]string{"job", "update", reader, "--wait", "--timeout", "3m", "--"}, cmd...)...)
		if ran := runOnce(t, c, reader); ran.Status == api.RunSucceeded {
			t.Errorf("a read-only job ran %v: %s", cmd, ran.OutputTail)
		} else if !strings.Contains(ran.OutputTail, "AccessDenied") && !strings.Contains(ran.OutputTail, "Access Denied") && !strings.Contains(ran.OutputTail, "(403)") {
			t.Errorf("%v failed, but not for access: %s", cmd, ran.OutputTail)
		}
	}
	if _, err := rootStore.StatObject(ctx, media, "written-by-reader.txt", minio.StatObjectOptions{}); err == nil {
		t.Error("the read-only job's write landed")
	}

	// A job that may read and write media: it writes, and the object is there.
	mustCLI(t, s, token, "", "job", "create", writer, "--schedule", "0 0 1 1 *", "--image", awsCLI, "--bucket", media+":readwrite", "--wait", "--timeout", "5m",
		"--", "s3", "cp", "/etc/hostname", "s3://"+media+"/written-by-writer.txt")
	if ran := runOnce(t, c, writer); ran.Status != api.RunSucceeded {
		t.Fatalf("writing with read and write access: %s %s\n%s", ran.Status, ran.Message, ran.OutputTail)
	}
	if _, err := rootStore.StatObject(ctx, media, "written-by-writer.txt", minio.StatObjectOptions{}); err != nil {
		t.Errorf("the writer's object isn't there: %v", err)
	}

	// Taking the writer's access away revokes its keys at once, even for a client that kept them.
	var keys struct{ AccessKey, SecretKey string }
	_ = json.Unmarshal([]byte(strings.TrimSpace(mustCLI(t, s, token, "", "secret", "get", writer+"-bucket-credentials", "--reveal"))), &keys)
	kept, err := minio.New(endpoint, &minio.Options{Creds: credentials.NewStaticV4(keys.AccessKey, keys.SecretKey, "")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kept.StatObject(ctx, media, "seed.txt", minio.StatObjectOptions{}); err != nil {
		t.Fatalf("the writer's keys from outside before revoking: %v", err)
	}
	mustCLI(t, s, token, "", "job", "update", writer, "--bucket", "", "--wait", "--timeout", "3m")
	if _, err := kept.StatObject(ctx, media, "seed.txt", minio.StatObjectOptions{}); err == nil {
		t.Error("revoked keys still read")
	}

	// A bucket a job can access can't be deleted.
	if _, _, code := cli(t, s, token, "", "bucket", "delete", media, "--yes"); code == 0 {
		t.Error("deleted a bucket a job can access")
	}
}

// runOnce runs a job now and waits for the run to end.
func runOnce(t *testing.T, c *client.Client, name string) api.RunDetail {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	job, err := c.FindResource(ctx, api.KindJob, name)
	if err != nil {
		t.Fatal(err)
	}
	run, err := c.RunNow(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	for {
		d, err := c.GetRun(ctx, job.ID, run.ID)
		if err == nil && d.Status != api.RunRunning {
			return d
		}
		select {
		case <-ctx.Done():
			t.Fatalf("the run of %s didn't end", name)
		case <-time.After(time.Second):
		}
	}
}

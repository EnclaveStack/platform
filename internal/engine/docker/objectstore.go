package docker

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/go-connections/nat"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/pkg/api"
)

const objectStoreVolume = "es-system-objectstore-data"

// ensureObjectStore runs the shared S3-compatible store and returns a client for it.
func (d *Driver) ensureObjectStore(ctx context.Context, creds engine.ObjectStore) (*minio.Client, int, error) {
	if creds.AccessKey == "" || creds.SecretKey == "" {
		return nil, 0, engine.Permanent(fmt.Errorf("object store credentials are missing"))
	}
	if err := d.ensureImage(ctx, d.opts.ObjectStoreImage, false); err != nil {
		return nil, 0, err
	}
	if err := d.ensureNetwork(ctx); err != nil {
		return nil, 0, err
	}
	c, err := d.inspect(ctx, engine.ObjectStoreName)
	if err != nil {
		return nil, 0, err
	}
	if c == nil {
		vctx, cancel := context.WithTimeout(ctx, stepTimeout)
		_, err := d.cli.VolumeCreate(vctx, volume.CreateOptions{Name: objectStoreVolume, Labels: map[string]string{LabelManaged: "true"}})
		cancel()
		if err != nil {
			return nil, 0, d.classify(fmt.Errorf("create object store volume: %w", err))
		}
		port := nat.Port(strconv.Itoa(engine.ObjectStorePort) + "/tcp")
		hp, err := d.hostPortFor(0)
		if err != nil {
			return nil, 0, err
		}
		cfg := &container.Config{Image: d.opts.ObjectStoreImage, Cmd: []string{"server", "/data"},
			Env:          []string{"MINIO_ROOT_USER=" + creds.AccessKey, "MINIO_ROOT_PASSWORD=" + creds.SecretKey},
			ExposedPorts: nat.PortSet{port: {}}, Labels: map[string]string{LabelManaged: "true", LabelKind: "system"}}
		host := &container.HostConfig{RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
			PortBindings: nat.PortMap{port: {{HostIP: d.opts.PublishAddress, HostPort: hp}}}, Binds: []string{objectStoreVolume + ":/data"},
			Resources: container.Resources{Memory: 512 << 20}}
		netCfg := &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{engine.NetworkName: {Aliases: []string{engine.ObjectStoreName}}}}
		cctx, cancel := context.WithTimeout(ctx, stepTimeout)
		_, err = d.cli.ContainerCreate(cctx, cfg, host, netCfg, nil, engine.ObjectStoreName)
		cancel()
		if err != nil && !strings.Contains(err.Error(), "already in use") {
			return nil, 0, d.classify(fmt.Errorf("create object store: %w", err))
		}
		if err := d.joinManagedNetworks(ctx, engine.ObjectStoreName); err != nil {
			return nil, 0, err
		}
	}
	if err := d.cli.ContainerStart(ctx, engine.ObjectStoreName, container.StartOptions{}); err != nil {
		return nil, 0, d.classify(fmt.Errorf("start object store: %w", err))
	}
	info, err := d.waitHealthy(ctx, engine.ObjectStoreName, false)
	if err != nil {
		return nil, 0, err
	}
	hp := hostPort(info, engine.ObjectStorePort)
	if hp == 0 {
		return nil, 0, fmt.Errorf("object store has no published port yet")
	}
	mc, err := minio.New("127.0.0.1:"+strconv.Itoa(hp), &minio.Options{Creds: credentials.NewStaticV4(creds.AccessKey, creds.SecretKey, ""), Secure: false})
	if err != nil {
		return nil, 0, engine.Permanent(err)
	}
	// MinIO answers HTTP a moment after the container starts; wait for it.
	deadline := time.Now().Add(healthyTimeout)
	for {
		hctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		_, err := mc.ListBuckets(hctx)
		cancel()
		if err == nil {
			return mc, hp, nil
		}
		if time.Now().After(deadline) {
			return nil, 0, fmt.Errorf("object store did not answer within %s: %w", healthyTimeout, err)
		}
		select {
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (d *Driver) ensureBucket(ctx context.Context, in engine.EnsureInput, report engine.Reporter) (engine.Outputs, error) {
	r := in.Resource
	spec, _ := in.Spec.(api.BucketSpec)
	report.Step("objectstore", api.StepActive)
	mc, port, err := d.ensureObjectStore(ctx, in.ObjectStore)
	if err != nil {
		return nil, err
	}
	report.Step("objectstore", api.StepDone)
	report.Step("bucket", api.StepActive)
	bctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	exists, err := mc.BucketExists(bctx, r.Name)
	if err != nil {
		return nil, fmt.Errorf("check bucket: %w", err)
	}
	if !exists {
		if err := mc.MakeBucket(bctx, r.Name, minio.MakeBucketOptions{}); err != nil {
			return nil, fmt.Errorf("create bucket: %w", err)
		}
	}
	cfg := minio.BucketVersioningConfiguration{Status: minio.Suspended}
	if spec.Versioning {
		cfg.Status = minio.Enabled
	}
	if err := mc.SetBucketVersioning(bctx, r.Name, cfg); err != nil {
		return nil, fmt.Errorf("set versioning: %w", err)
	}
	report.Step("bucket", api.StepDone)
	host := d.opts.PublishAddress
	if host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	return engine.Outputs{"endpoint": "http://" + host + ":" + strconv.Itoa(port), "internalEndpoint": "http://" + engine.ObjectStoreName + ":" + strconv.Itoa(engine.ObjectStorePort),
		"bucket": r.Name, "credentialsSecret": domain.ObjectStoreCredentialsSecret}, nil
}

func (d *Driver) objectStoreClient(ctx context.Context, creds engine.ObjectStore) (*minio.Client, error) {
	c, err := d.inspect(ctx, engine.ObjectStoreName)
	if err != nil || c == nil || c.State == nil || !c.State.Running {
		return nil, err
	}
	hp := hostPort(c, engine.ObjectStorePort)
	return minio.New("127.0.0.1:"+strconv.Itoa(hp), &minio.Options{Creds: credentials.NewStaticV4(creds.AccessKey, creds.SecretKey, "")})
}

func (d *Driver) deleteBucket(ctx context.Context, r *domain.Resource, creds engine.ObjectStore) error {
	mc, err := d.objectStoreClient(ctx, creds)
	if err != nil {
		return err
	}
	if mc == nil {
		return nil // no object store, nothing to delete
	}
	bctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	exists, err := mc.BucketExists(bctx, r.Name)
	if err != nil || !exists {
		return err
	}
	objects := mc.ListObjects(bctx, r.Name, minio.ListObjectsOptions{Recursive: true, WithVersions: true})
	for res := range mc.RemoveObjects(bctx, r.Name, objects, minio.RemoveObjectsOptions{}) {
		if res.Err != nil {
			return fmt.Errorf("empty bucket: %w", res.Err)
		}
	}
	return mc.RemoveBucket(bctx, r.Name)
}

func (d *Driver) bucketHealth(ctx context.Context, r *domain.Resource, creds engine.ObjectStore) (engine.Health, error) {
	mc, err := d.objectStoreClient(ctx, creds)
	if err != nil {
		return engine.Missing, err
	}
	if mc == nil {
		return engine.Missing, nil
	}
	bctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ok, err := mc.BucketExists(bctx, r.Name)
	if err != nil {
		return engine.Missing, err
	}
	if !ok {
		return engine.Missing, nil
	}
	return engine.Healthy, nil
}

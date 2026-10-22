package provider_test

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// v0.5 resources (L2-003 TF-22 – TF-26) with real OpenTofu against the fake engine and hypervisor.

const tfKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEXTqZbAPryM1c8TBIUjg6rtRL1Dfr2uR7E954v/Icd4 test@enclave"

func TestAccComputeStack(t *testing.T) {
	boot(t)
	var vmID, diskID, appID string
	config := func(cpus, diskGiB, volSize, power, extra string) string {
		return fmt.Sprintf(`
resource "enclave_network" "backend" {
  name = "backend"
  cidr = "10.40.0.0/16"
}
resource "enclave_volume" "data" {
  name     = "data"
  type     = "disk"
  size_gib = %s
}
resource "enclave_volume" "uploads" {
  name = "uploads"
  type = "filesystem"
}
resource "enclave_app" "web" {
  name    = "web"
  image   = "nginx:1.27-alpine"
  port    = 80
  volumes = [{ volume = enclave_volume.uploads.name, path = "/usr/share/nginx/html" }]
}
resource "enclave_job" "thumbs" {
  name     = "thumbs"
  schedule = "@daily"
  image    = "busybox:1.37"
  volumes  = [{ volume = enclave_volume.uploads.name, path = "/in", read_only = true }]
}
resource "enclave_vm" "worker" {
  name      = "worker"
  image     = "debian-12"
  ssh_keys  = [%q]
  user_data = "#!/bin/sh\necho hi\n"
  cpus      = %s
  disk_gib  = %s
  network   = enclave_network.backend.name
  disks     = [enclave_volume.data.name]
  publish   = [8080]
  power     = %q
}
resource "enclave_snapshot" "before" {
  name   = "before"
  source = "volume/${enclave_volume.uploads.name}"
  depends_on = [enclave_app.web]
}
%s
`, volSize, tfKey, cpus, diskGiB, power, extra)
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: config("1", "10", "20", "running", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("enclave_vm.worker", "private_dns", "worker.vm.internal"),
					resource.TestCheckResourceAttr("enclave_vm.worker", "ssh_user", "enclave"),
					resource.TestCheckResourceAttr("enclave_vm.worker", "memory_mib", "1024"),
					resource.TestMatchResourceAttr("enclave_vm.worker", "ssh_address", regexp.MustCompile(`^127\.0\.0\.1:\d+$`)),
					resource.TestMatchResourceAttr("enclave_vm.worker", "private_ip", regexpIn("10.40.")),
					resource.TestCheckNoResourceAttr("enclave_volume.uploads", "size_gib"),
					resource.TestCheckResourceAttr("enclave_app.web", "volumes.0.path", "/usr/share/nginx/html"),
					resource.TestCheckResourceAttr("enclave_job.thumbs", "volumes.0.read_only", "true"),
					resource.TestCheckResourceAttr("enclave_snapshot.before", "type", "filesystem"),
					resource.TestCheckResourceAttrSet("enclave_snapshot.before", "taken_at"),
					sameID("enclave_vm.worker", &vmID), sameID("enclave_volume.data", &diskID), sameID("enclave_app.web", &appID),
				),
			},
			{
				// In place: CPUs, root disk growth, disk growth, power; a volume from the snapshot.
				Config: config("2", "20", "30", "stopped", `
resource "enclave_volume" "restored" {
  name     = "restored"
  type     = "filesystem"
  snapshot = enclave_snapshot.before.name
}`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("enclave_vm.worker", "cpus", "2"),
					resource.TestCheckResourceAttr("enclave_vm.worker", "disk_gib", "20"),
					resource.TestCheckResourceAttr("enclave_vm.worker", "power", "stopped"),
					resource.TestCheckResourceAttr("enclave_volume.data", "size_gib", "30"),
					resource.TestCheckResourceAttr("enclave_volume.restored", "snapshot", "before"),
					sameID("enclave_vm.worker", &vmID), sameID("enclave_volume.data", &diskID), sameID("enclave_app.web", &appID),
				),
			},
			{
				// Shrinking is refused at plan time.
				Config:      config("2", "15", "30", "stopped", ""),
				ExpectError: regexp.MustCompile(`can only grow`),
			},
			{ResourceName: "enclave_vm.worker", ImportState: true, ImportStateId: "worker", ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts"}},
			{ResourceName: "enclave_volume.data", ImportState: true, ImportStateId: "data", ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts", "used_bytes", "attached_to"}},
			{ResourceName: "enclave_snapshot.before", ImportState: true, ImportStateId: "before", ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts"}},
		},
	})
}

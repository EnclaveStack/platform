import { describe, expect, it } from 'vitest';
import type { Resource } from '../api/types';
import { vmRestarts } from '../routes/ResourceForm';
import { cliCommand, tofuBlock } from './codegen';
import { diskSerial, validateMounts, validateSnapshot, validateVM, validateVolume } from './validation';

// v0.5 rules (L3-002 §8.4) and "Use from code" (CON-36), kept equal to pkg/api and the CLI/provider.

const KEY = 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEXTqZbAPryM1c8TBIUjg6rtRL1Dfr2uR7E954v/Icd4 test@enclave';
const res = (kind: Resource['kind'], name: string, spec: object): Resource => ({ id: 'x', kind, name, project: 'default', generation: 1, ownerId: null, spec: spec as never,
  status: { phase: 'ready', message: '', observedGeneration: 1, attempts: 0, steps: [], lastTransitionAt: '' }, outputs: {}, createdAt: '', updatedAt: '' });

describe('VM, volume and snapshot validation', () => {
  it('accepts the catalog, URLs and snapshots, and refuses the rest', () => {
    for (const image of ['ubuntu-24.04', 'debian-12', 'https://example.com/a.qcow2', 'snapshot/worker-1']) {
      expect(validateVM({ image, sshKeys: [KEY] })).toEqual({});
    }
    expect(validateVM({ image: 'windows-11', sshKeys: [KEY] })['spec.image']).toMatch(/Choose ubuntu-24.04/);
    expect(validateVM({ image: 'debian-12', sshKeys: [KEY], imageSha256: 'a'.repeat(64) })['spec.imageSha256']).toMatch(/Only an image from a URL/);
    expect(validateVM({ image: 'debian-12' })['spec.sshKeys']).toMatch(/at least one/);
    expect(validateVM({ image: 'debian-12', sshKeys: ['-----BEGIN OPENSSH PRIVATE KEY-----'] })['spec.sshKeys[0]']).toMatch(/private key/);
    expect(validateVM({ image: 'debian-12', sshKeys: [KEY], sshKeySecret: 'keys' })['spec.sshKeys']).toMatch(/choose one/);
    expect(validateVM({ image: 'debian-12', sshKeys: [KEY], userData: 'apt install x' })['spec.userData']).toMatch(/#cloud-config/);
    expect(validateVM({ image: 'debian-12', sshKeys: [KEY], memoryMiB: 1000 })['spec.memoryMiB']).toMatch(/steps of 128/);
    expect(validateVM({ image: 'debian-12', sshKeys: [KEY], publish: [22] })['spec.publish[0]']).toMatch(/SSH/);
    expect(validateVM({ image: 'debian-12', sshKeys: [KEY], disks: ['customer-uploads-2026-a', 'customer-uploads-2026-b'] })['spec.disks[1]']).toMatch(/same 20 characters/);
    expect(diskSerial('customer-uploads-2026-a')).toBe('customer-uploads-202');
  });

  it('checks volumes, snapshots and mounts like the server', () => {
    expect(validateVolume({ type: 'disk', sizeGiB: 50 })).toEqual({});
    expect(validateVolume({ type: 'filesystem', sizeGiB: 5 })['spec.sizeGiB']).toMatch(/no size/);
    expect(validateSnapshot({ source: 'vm/worker' })).toEqual({});
    expect(validateSnapshot({ source: 'app/web' })['spec.source']).toMatch(/volume\/<name>/);
    expect(validateMounts([{ volume: 'uploads', path: '/data' }])).toEqual({});
    expect(validateMounts([{ volume: 'uploads', path: '/' }])['spec.volumes[0].path']).toMatch(/not on \//);
    expect(validateMounts([{ volume: 'uploads', path: '/a/../b' }])['spec.volumes[0].path']).toMatch(/absolute path/);
    expect(validateMounts([{ volume: 'a-a', path: '/d' }, { volume: 'b-b', path: '/d' }])['spec.volumes[1].path']).toMatch(/already mounted/);
  });

  it('names what restarts a running VM, as pkg/api does', () => {
    expect(vmRestarts({ image: 'x', cpus: 1, memoryMiB: 1024 }, { image: 'x', cpus: 1, memoryMiB: 1024, diskGiB: 30, power: 'stopped' })).toEqual([]);
    expect(vmRestarts({ image: 'x', cpus: 1 }, { image: 'x', cpus: 2, memoryMiB: 2048, disks: ['data'] })).toEqual(['CPUs', 'memory', 'disks']);
  });
});

describe('use from code, v0.5', () => {
  it('writes the CLI command and OpenTofu block for a VM', () => {
    const vm = res('vm', 'worker', { image: 'debian-12', sshKeys: [KEY], userData: '#!/bin/sh\n', cpus: 2, memoryMiB: 2048, diskGiB: 10, network: 'backend', disks: ['data'], publish: [8080], power: 'running' });
    const cli = cliCommand(vm);
    expect(cli).toContain('enclave vm create worker');
    expect(cli).toContain(`--ssh-key '${KEY}'`);
    expect(cli).toContain('--user-data ./worker-init.sh');
    expect(cli).toContain('--disk-volume data');
    expect(cli).not.toContain('--disk 10');
    const hcl = tofuBlock(vm);
    expect(hcl).toContain('resource "enclave_vm" "worker"');
    expect(hcl).toContain('user_data = file("${path.module}/worker-init.sh")');
    expect(hcl).toContain('disks = ["data"]');
    expect(hcl).toContain('publish = [8080]');
  });

  it('writes volumes, snapshots and mounts', () => {
    expect(cliCommand(res('volume', 'data', { type: 'disk', sizeGiB: 50 }))).toBe('enclave volume create data --type disk --size 50 --wait');
    expect(tofuBlock(res('snapshot', 'before', { source: 'volume/data' }))).toContain('source = "volume/data"');
    const app = res('app', 'web', { image: 'nginx:1.27', volumes: [{ volume: 'uploads', path: '/data', readOnly: true }] });
    expect(cliCommand(app)).toContain('--volume uploads:/data:ro');
    expect(tofuBlock(app)).toContain('{ volume = "uploads", path = "/data", read_only = true },');
  });
});

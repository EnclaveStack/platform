package docker

import (
	"context"
	"fmt"
	"slices"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
)

// Sweep removes the containers of every resource EnclaveStack manages here that isn't in keep: what moved
// to another machine while this one was offline (ADR-046). Volumes stay, and so do system containers. With
// everything, it removes all of EnclaveStack from this engine, containers, volumes and networks alike
// (`enclave agent leave`).
func (d *Driver) Sweep(ctx context.Context, keep []string, everything bool) ([]string, error) {
	lctx, cancel := context.WithTimeout(ctx, stepTimeout)
	list, err := d.cli.ContainerList(lctx, container.ListOptions{All: true, Filters: filters.NewArgs(filters.Arg("label", LabelManaged+"=true"))})
	cancel()
	if err != nil {
		return nil, d.classify(fmt.Errorf("list containers: %w", err))
	}
	var removed []string
	for _, c := range list {
		rid := c.Labels[LabelResourceID]
		if !everything && (rid == "" || slices.Contains(keep, rid)) {
			continue
		}
		name := strings.TrimPrefix(firstName(c.Names), "/")
		if err := d.remove(ctx, c.ID); err != nil {
			return removed, err
		}
		removed = append(removed, name)
	}
	if !everything {
		return removed, nil
	}
	vctx, cancel := context.WithTimeout(ctx, stepTimeout)
	vols, err := d.cli.VolumeList(vctx, volume.ListOptions{Filters: filters.NewArgs(filters.Arg("label", LabelManaged+"=true"))})
	cancel()
	if err != nil {
		return removed, d.classify(fmt.Errorf("list volumes: %w", err))
	}
	for _, v := range vols.Volumes {
		rctx, cancel := context.WithTimeout(ctx, stepTimeout)
		err := d.cli.VolumeRemove(rctx, v.Name, true)
		cancel()
		if err != nil && !cerrdefs.IsNotFound(err) {
			return removed, d.classify(fmt.Errorf("remove volume %s: %w", v.Name, err))
		}
	}
	nctx, cancel := context.WithTimeout(ctx, stepTimeout)
	nets, err := d.cli.NetworkList(nctx, network.ListOptions{Filters: filters.NewArgs(filters.Arg("label", LabelManaged+"=true"))})
	cancel()
	if err != nil {
		return removed, d.classify(fmt.Errorf("list networks: %w", err))
	}
	for _, n := range nets {
		rctx, cancel := context.WithTimeout(ctx, stepTimeout)
		err := d.cli.NetworkRemove(rctx, n.ID)
		cancel()
		if err != nil && !cerrdefs.IsNotFound(err) {
			return removed, d.classify(fmt.Errorf("remove network %s: %w", n.Name, err))
		}
	}
	return removed, nil
}

func firstName(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

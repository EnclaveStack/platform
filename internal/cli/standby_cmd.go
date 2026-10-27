package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/enclavestack/platform/internal/agent"
	"github.com/enclavestack/platform/internal/machines"
	"github.com/enclavestack/platform/internal/server"
)

// The standby control plane (ADR-057, L2-016 SBY-04): promote on the standby itself.
func (a *app) standbyCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "standby", Short: "Take over as the main machine on the standby (run on the standby)"}
	var cfg server.Config
	var logFormat, setupCodeFile, stateDir string
	var yes, noStart bool
	promote := &cobra.Command{
		Use: "promote", Short: "Make this standby the main machine from its newest copy, then run the control plane", Args: cobra.NoArgs,
		Long: "Make this standby the main machine from its newest copy of the control plane, then run the control plane here.\n\n" +
			"Stop the old main machine first if you can still reach it. Changes made after the last copy are lost.\n" +
			"Every agent then follows this machine and refuses the old one.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			copies := agent.StandbyCopies(stateDir)
			if len(copies) == 0 {
				return fmt.Errorf("this computer holds no copy of the control plane (%s)", stateDir)
			}
			if err := a.confirm(fmt.Sprintf("Take over from the copy taken %s ago? Stop the old main machine first if you can.",
				time.Since(copies[0].TakenAt).Round(time.Second)), yes); err != nil {
				return err
			}
			return a.promote(cmd, stateDir, cfg, logFormat, setupCodeFile, noStart)
		},
	}
	a.serverFlags(promote, &cfg, &logFormat, &setupCodeFile)
	promote.Flags().StringVar(&stateDir, "state-dir", agent.DefaultStateDir(), "where the agent keeps its identity and copies")
	promote.Flags().BoolVar(&yes, "yes", false, "don't ask")
	promote.Flags().BoolVar(&noStart, "no-start", false, "promote, then exit without running the control plane")
	status := &cobra.Command{
		Use: "status", Short: "Show the copies of the control plane this standby holds", Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			copies := agent.StandbyCopies(stateDir)
			if a.output == "json" {
				return a.json(agent.StandbyState{Copies: copies})
			}
			rows := [][]string{}
			for _, c := range copies {
				rows = append(rows, []string{fmt.Sprint(c.N), c.TakenAt.Local().Format(time.DateTime), mib(c.Bytes)})
			}
			a.table([]string{"COPY", "TAKEN", "SIZE"}, rows)
			return nil
		},
	}
	status.Flags().StringVar(&stateDir, "state-dir", agent.DefaultStateDir(), "where the agent keeps its identity and copies")
	cmd.AddCommand(promote, status)
	return cmd
}

func (a *app) promote(cmd *cobra.Command, stateDir string, cfg server.Config, logFormat, setupCodeFile string, noStart bool) error {
	if cfg.DataDir == "" {
		cfg.DataDir = server.DefaultDataDir()
	}
	p, err := machines.Promote(cmd.Context(), stateDir, cfg.DataDir, time.Now())
	if err != nil {
		return err
	}
	fmt.Fprintf(a.io.Out, "%s is the main machine now (epoch %d), from the copy taken %s.\n", p.Machine, p.Epoch, p.TakenAt.Local().Format(time.DateTime))
	if noStart {
		fmt.Fprintf(a.io.Out, "Start the control plane with: enclave server --data-dir %s\n", cfg.DataDir)
		return nil
	}
	return a.serve(cmd, cfg, logFormat, setupCodeFile)
}

// autoPromote watches a standby's agent and promotes it when no control plane called for after (ADR-057).
// It returns when ctx ends or after promoting, with the context the control plane should run under.
func autoPromote(ctx context.Context, s *agent.Server, after time.Duration, started time.Time, promote func() error) error {
	t := time.NewTicker(min(after/4, 10*time.Second))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		last := started
		if lc := s.LastContact(); lc != nil {
			last = *lc
		}
		if time.Since(last) >= after && len(agent.StandbyCopies(s.StateDir)) > 0 {
			return promote()
		}
	}
}

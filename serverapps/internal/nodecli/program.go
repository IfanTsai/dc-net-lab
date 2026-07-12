package nodecli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/ifantsai/dcnetlab/internal/agentapi"
	pb "github.com/ifantsai/dcnetlab/pb/nodeagent/v1"
)

// The program command manages programs on this server through the
// local node-agent daemon, with the same semantics as the UI's
// Programs page: deploy registers without starting, oneshot programs
// run once per start, auto-start means "run on every server boot".
// Programs created here are node-local: the controller does not
// manage them, so a lab redeploy will not restore them (like a
// hand-written systemd unit next to config management).
const programUsage = `usage: program [flags] <command>        (equivalent: node-cli program ...)

commands:
  list                        list the programs on this server
  deploy <name> <pkg>[@<ver>] [deploy flags] [-- <args>...]
                              register a program running a package
                              entrypoint (not started; latest version
                              by default, installed on demand)
  start <name>                start a program (oneshot: run it once)
  stop <name>                 stop a program
  remove <name>               stop and delete a program
  logs <name> [-n <lines>]    tail the program log

deploy flags:
  --type <simple|oneshot>     systemd-like service type (default simple)
  --auto-start                start on every server boot (systemd enable)
  --restart-policy <p>        Never | OnFailure | Always (default Never;
                              oneshot rejects Always)

flags (before the command):
  --repo <url>    repository base URL (default: $DCNETLAB_REPO, the
                  URL remembered by the agent, or the management
                  gateway on port %d)
  --dir <path>    agent state directory (default /opt/dcnetlab/run)
  --agent <addr>  local agent gRPC address (default 127.0.0.1:%d)
`

// ProgramMain runs the program subcommand and returns the process
// exit code.
func ProgramMain(args []string) int {
	fs := flag.NewFlagSet("program", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, programUsage, agentapi.DefaultRepoPort, agentapi.DefaultPort)
	}

	repo := fs.String("repo", "", "repository base URL")
	dir := fs.String("dir", "/opt/dcnetlab/run", "agent state directory")
	agentAddr := fs.String("agent", fmt.Sprintf("127.0.0.1:%d", agentapi.DefaultPort), "local agent gRPC address")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cli := &cliCtx{repo: *repo, dir: *dir, agentAddr: *agentAddr, out: os.Stdout}

	var err error
	switch cmd, target := fs.Arg(0), fs.Arg(1); cmd {
	case "list":
		err = cli.programList()
	case "deploy":
		err = cli.programDeploy(fs.Args()[1:])
	case "start":
		err = cli.programPower(target, true)
	case "stop":
		err = cli.programPower(target, false)
	case "remove":
		err = cli.programRemove(target)
	case "logs":
		err = cli.programLogs(fs.Args()[1:])
	default:
		fs.Usage()

		return 2
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "program:", err)

		return 1
	}

	return 0
}

// programList prints every program on this server with its live
// state.
func (c *cliCtx) programList() error {
	var reply *pb.ListReply
	err := c.callAgent(pkgCallTimeout, func(ctx context.Context, client pb.NodeAgentClient) error {
		var err error
		reply, err = client.List(ctx, &pb.ListRequest{})

		return err
	})
	if err != nil {
		return err
	}

	tw := tabwriter.NewWriter(c.out, 2, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tPACKAGE\tTYPE\tAUTOSTART\tRESTART\tSTATE\tPID\tRESTARTS")
	for _, info := range reply.Programs {
		if info.Spec == nil {
			continue
		}

		autoStart := "-"
		if info.Spec.AutoStart {
			autoStart = "enabled"
		}

		pid := "-"
		if info.Pid > 0 {
			pid = fmt.Sprint(info.Pid)
		}

		_, _ = fmt.Fprintf(tw, "%s\t%s@%s\t%s\t%s\t%s\t%s\t%s\t%d\n",
			info.Spec.Name, info.Spec.PackageName, info.Spec.PackageVersion,
			info.Spec.Type, autoStart, info.Spec.RestartPolicy, info.State, pid, info.Restarts)
	}

	return tw.Flush()
}

// programDeploy registers a program: the package version is resolved
// against the repository (and installed when missing), then the
// definition lands on the local agent. Like the UI, deploying does
// not start the program.
func (c *cliCtx) programDeploy(args []string) error {
	fs := flag.NewFlagSet("program deploy", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	typ := fs.String("type", agentapi.TypeSimple, "simple or oneshot")
	autoStart := fs.Bool("auto-start", false, "start on every server boot")
	policy := fs.String("restart-policy", agentapi.RestartNever, "Never, OnFailure or Always")

	// Flags may come before or after the two positional arguments.
	positional, flagArgs := splitPositional(args, 2)
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}

	if len(positional) < 2 {
		return fmt.Errorf("usage: program deploy <name> <pkg>[@<ver>] [deploy flags] [-- <args>...]")
	}

	progName := positional[0]
	pkgName, version, err := splitTarget(positional[1])
	if err != nil {
		return err
	}

	entry, err := c.resolveEntry(pkgName, version)
	if err != nil {
		return err
	}

	if err := c.installEntry(entry); err != nil {
		return err
	}

	spec := &pb.ProgramSpec{
		Name:           progName,
		PackageName:    entry.Name,
		PackageVersion: entry.Version,
		Entrypoint:     entry.Entrypoint,
		Args:           positional[2:],
		RestartPolicy:  *policy,
		Type:           *typ,
		AutoStart:      *autoStart,
	}
	err = c.callAgent(pkgCallTimeout, func(ctx context.Context, client pb.NodeAgentClient) error {
		_, err := client.Install(ctx, &pb.InstallRequest{Spec: spec})

		return err
	})
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(c.out, "deployed %s (%s@%s, node-local); start it with: program start %s\n",
		progName, entry.Name, entry.Version, progName)

	return nil
}

// programPower starts or stops one program.
func (c *cliCtx) programPower(name string, on bool) error {
	if name == "" {
		return fmt.Errorf("program name required")
	}

	var info *pb.ProgramInfo
	err := c.callAgent(pkgCallTimeout, func(ctx context.Context, client pb.NodeAgentClient) error {
		var err error
		if on {
			info, err = client.Start(ctx, &pb.ProgramRef{Name: name})
		} else {
			info, err = client.Stop(ctx, &pb.ProgramRef{Name: name})
		}

		return err
	})
	if err != nil {
		return err
	}

	if info.Pid > 0 {
		_, _ = fmt.Fprintf(c.out, "%s: %s (pid %d)\n", name, info.State, info.Pid)
	} else {
		_, _ = fmt.Fprintf(c.out, "%s: %s\n", name, info.State)
	}

	return nil
}

// programRemove stops and deletes one program definition.
func (c *cliCtx) programRemove(name string) error {
	if name == "" {
		return fmt.Errorf("program name required")
	}

	err := c.callAgent(pkgCallTimeout, func(ctx context.Context, client pb.NodeAgentClient) error {
		_, err := client.Remove(ctx, &pb.ProgramRef{Name: name})

		return err
	})
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(c.out, "removed %s\n", name)

	return nil
}

// programLogs tails one program's combined log.
func (c *cliCtx) programLogs(args []string) error {
	fs := flag.NewFlagSet("program logs", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	lines := fs.Int("n", 0, "number of lines")

	positional, flagArgs := splitPositional(args, 1)
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}

	if len(positional) < 1 {
		return fmt.Errorf("usage: program logs <name> [-n <lines>]")
	}

	var reply *pb.TailLogsReply
	err := c.callAgent(pkgCallTimeout, func(ctx context.Context, client pb.NodeAgentClient) error {
		var err error
		reply, err = client.TailLogs(ctx, &pb.TailLogsRequest{Name: positional[0], Lines: int32(*lines)})

		return err
	})
	if err != nil {
		return err
	}

	_, _ = fmt.Fprint(c.out, reply.Content)

	return nil
}

// splitPositional separates up to max leading positional arguments
// from the flags around them; everything after a bare "--" is
// appended to the positionals verbatim (program arguments).
func splitPositional(args []string, max int) (positional, flags []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			positional = append(positional, args[i+1:]...)

			return positional, flags
		case strings.HasPrefix(a, "-") || len(positional) >= max:
			flags = append(flags, a)
			// A flag of the form --name value consumes the next token.
			if !strings.Contains(a, "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") && isValueFlag(a) {
				i++
				flags = append(flags, args[i])
			}
		default:
			positional = append(positional, a)
		}
	}

	return positional, flags
}

// isValueFlag reports whether a deploy/logs flag takes a value (so
// splitPositional keeps the value token with it).
func isValueFlag(flagToken string) bool {
	switch strings.TrimLeft(flagToken, "-") {
	case "type", "restart-policy", "n":
		return true
	}

	return false
}

package patch

import (
	"fmt"
	"io"
	"os"
	"os/exec"
)

// Runner abstracts process execution so dry-run and real-run share one path.
type Runner struct {
	DryRun bool
	Out    io.Writer
}

// NewRunner constructs a Runner that writes progress to out.
func NewRunner(dryRun bool, out io.Writer) *Runner {
	if out == nil {
		out = os.Stdout
	}
	return &Runner{DryRun: dryRun, Out: out}
}

// Run executes a command, or prints what would run when DryRun is true.
// stdout and stderr are forwarded to the runner's Out.
func (r *Runner) Run(name string, args ...string) error {
	fmt.Fprintf(r.Out, "%s %s %s\n", r.prefix(), name, joinArgs(args))
	if r.DryRun {
		return nil
	}
	cmd := exec.Command(name, args...)
	cmd.Stdout = r.Out
	cmd.Stderr = r.Out
	return cmd.Run()
}

// RunCaptureStdout runs a command and returns only its stdout (stderr goes to Out).
func (r *Runner) RunCaptureStdout(name string, args ...string) ([]byte, error) {
	fmt.Fprintf(r.Out, "%s %s %s\n", r.prefix(), name, joinArgs(args))
	if r.DryRun {
		return nil, nil
	}
	cmd := exec.Command(name, args...)
	cmd.Stderr = r.Out
	return cmd.Output()
}

// Note logs a non-command step.
func (r *Runner) Note(format string, args ...any) {
	fmt.Fprintf(r.Out, "%s "+format+"\n", append([]any{r.prefix()}, args...)...)
}

func (r *Runner) prefix() string {
	if r.DryRun {
		return "[dry-run]"
	}
	return "[run]"
}

func joinArgs(args []string) string {
	out := ""
	for i, a := range args {
		if i > 0 {
			out += " "
		}
		out += a
	}
	return out
}

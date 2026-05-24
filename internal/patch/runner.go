package patch

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
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
	r.logCommand(nil, name, args...)
	if r.DryRun {
		return nil
	}
	cmd := exec.Command(name, args...)
	cmd.Stdout = r.Out
	cmd.Stderr = r.Out
	return cmd.Run()
}

// RunWithHeartbeat executes a command and logs periodic progress while it is
// still running.
func (r *Runner) RunWithHeartbeat(label string, interval time.Duration, name string, args ...string) error {
	r.logCommand(nil, name, args...)
	if r.DryRun {
		return nil
	}
	cmd := exec.Command(name, args...)
	cmd.Stdout = r.Out
	cmd.Stderr = r.Out
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		done <- cmd.Wait()
	}()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			return err
		case <-ticker.C:
			r.Note("%s still running after %s", label, time.Since(start).Round(time.Second))
		}
	}
}

// RunEnvWithHeartbeat executes a command with environment overrides and logs
// periodic progress while it is still running.
func (r *Runner) RunEnvWithHeartbeat(
	label string,
	interval time.Duration,
	env map[string]string,
	name string,
	args ...string,
) error {
	r.logCommand(env, name, args...)
	if r.DryRun {
		return nil
	}
	cmd := exec.Command(name, args...)
	cmd.Env = mergedEnv(env)
	cmd.Stdout = r.Out
	cmd.Stderr = r.Out
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		done <- cmd.Wait()
	}()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			return err
		case <-ticker.C:
			r.Note("%s still running after %s", label, time.Since(start).Round(time.Second))
		}
	}
}

// RunCaptureStdout runs a command and returns only its stdout (stderr goes to Out).
func (r *Runner) RunCaptureStdout(name string, args ...string) ([]byte, error) {
	r.logCommand(nil, name, args...)
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

func (r *Runner) logCommand(env map[string]string, name string, args ...string) {
	if len(env) > 0 {
		keys := make([]string, 0, len(env))
		for key := range env {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Fprintf(r.Out, "%s env %s=%s\n", r.prefix(), key, env[key])
		}
	}
	fmt.Fprintf(r.Out, "%s %s %s\n", r.prefix(), name, joinArgs(args))
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

func mergedEnv(overrides map[string]string) []string {
	if len(overrides) == 0 {
		return nil
	}
	base := os.Environ()
	out := make([]string, 0, len(base)+len(overrides))
	seen := make(map[string]bool, len(base)+len(overrides))
	for _, entry := range base {
		key := entry
		if cut := strings.IndexByte(entry, '='); cut >= 0 {
			key = entry[:cut]
		}
		if value, ok := overrides[key]; ok {
			out = append(out, key+"="+value)
			seen[key] = true
			continue
		}
		out = append(out, entry)
		seen[key] = true
	}
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		if seen[key] {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		out = append(out, key+"="+overrides[key])
	}
	return out
}

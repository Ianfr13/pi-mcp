package runner

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
)

// SpawnConfig holds everything needed to launch one pi subprocess (§4).
type SpawnConfig struct {
	// Prompt is the fully rendered single positional prompt (see RenderPrompt).
	Prompt string
	// NoContextFiles toggles the --no-context-files flag (§4 / §4.2).
	NoContextFiles bool
	// CWD is the working directory: the project (read) or the job worktree (write).
	CWD string
	// Stderr receives the child's stderr (diagnostic only, §4). Required.
	// Use io.Discard if you don't want it. The runner never logs it itself.
	Stderr io.Writer
}

// Process is a started pi subprocess. The caller wires Stdout to the parser and
// calls Wait when the stream is drained. Killing is done by cancelling the
// context passed to Spawn (see Spawn).
type Process struct {
	// Stdout streams the pi JSONL events; read to EOF then call Wait.
	Stdout  io.ReadCloser
	cmd     *exec.Cmd
	devnull *os.File
}

// Wait blocks until the process exits and releases resources. It returns the
// process's exit error (nil on exit 0). Per §4, exit code is one input to the
// success gate; the parser/runstore make the authoritative determination.
func (p *Process) Wait() error {
	err := p.cmd.Wait()
	if p.devnull != nil {
		_ = p.devnull.Close()
	}
	return err
}

// PID returns the OS process id (used by the jobs registry for liveness, §8).
func (p *Process) PID() int {
	if p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

// Spawn renders nothing — it takes an already-rendered prompt — and launches
// `pi` (§4):
//
//	argv : pi -p --mode json --no-session [--no-context-files] <prompt>
//	stdin: /dev/null              (CRITICAL — pi -p hangs on an open stdin)
//	env  : os.Environ()           (intentional broad passthrough — §4/§14)
//	cwd  : cfg.CWD                (project for read, worktree for write)
//
// The returned Process exposes Stdout for the parser. Cancelling ctx kills the
// process (used by pi_cancel). The caller MUST call Wait after draining Stdout.
func Spawn(ctx context.Context, cfg SpawnConfig) (*Process, error) {
	if cfg.CWD == "" {
		return nil, fmt.Errorf("runner: empty cwd")
	}
	if cfg.Stderr == nil {
		return nil, fmt.Errorf("runner: nil stderr writer")
	}

	argv := BuildArgv(cfg.Prompt, cfg.NoContextFiles)

	// CommandContext so that cancelling ctx sends SIGKILL to the process,
	// satisfying the §5.5 cancel requirement.
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = cfg.CWD
	cmd.Env = os.Environ() // passthrough (§4): HOME, PATH, AGENT_VAULT_*, proxy/CA.
	cmd.Stderr = cfg.Stderr

	// CRITICAL: stdin = /dev/null. A nil Stdin inherits the parent's stdin and
	// risks the documented hang; an explicit /dev/null guarantees immediate EOF.
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		return nil, fmt.Errorf("runner: open %s: %w", os.DevNull, err)
	}
	cmd.Stdin = devnull

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = devnull.Close()
		return nil, fmt.Errorf("runner: stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		_ = devnull.Close()
		return nil, fmt.Errorf("runner: start %q: %w", argv[0], err)
	}

	return &Process{Stdout: stdout, cmd: cmd, devnull: devnull}, nil
}

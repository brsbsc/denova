package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"denova/config"
	"denova/internal/agents/runtime/external"
	"github.com/charmbracelet/x/xpty"
)

const (
	// authPTYWidth keeps long OAuth authorization URLs on a single terminal line
	// so interactive CLIs do not soft-wrap query parameters across lines.
	authPTYWidth  = 1024
	authPTYHeight = 40

	authLogoutTimeout = 30 * time.Second
)

type authProcessSpec struct {
	Executable  string
	Args        []string
	Env         []string
	UsePTY      bool
	PreferPipes bool
	Logger      *slog.Logger
	Engine      config.RuntimeID
	SessionID   string
}

// authProcess owns one interactive CLI authentication subprocess backed by a
// Unix PTY when requested and available, or standard pipes as a fallback.
type authProcess struct {
	cmd       *exec.Cmd
	pty       xpty.Pty
	reader    io.ReadCloser
	stdin     io.WriteCloser
	killTree  func()
	waitFn    func() error
	tempDir   string
	writeMu   sync.Mutex
	closeOnce sync.Once
	cleanOnce sync.Once
}

func startAuthProcess(spec authProcessSpec) (*authProcess, error) {
	if strings.TrimSpace(spec.Executable) == "" {
		return nil, ErrEngineNotInstalled
	}
	tempDir, err := os.MkdirTemp("", "denova-auth-")
	if err != nil {
		return nil, fmt.Errorf("create auth working directory: %w", err)
	}

	if spec.UsePTY && !spec.PreferPipes && runtime.GOOS != "windows" {
		terminal, ptyErr := xpty.NewPty(authPTYWidth, authPTYHeight)
		if ptyErr == nil {
			cmd := exec.Command(spec.Executable, spec.Args...)
			cmd.Dir = tempDir
			cmd.Env = spec.Env
			external.ConfigureProcess(cmd)
			if err := terminal.Start(cmd); err != nil {
				_ = terminal.Close()
				_ = os.RemoveAll(tempDir)
				return nil, fmt.Errorf("start %s auth process: %w", spec.Engine, err)
			}
			// Close the parent-side slave handle immediately so reading the master
			// observes EOF/EIO as soon as the child process exits.
			if unixPTY, ok := terminal.(*xpty.UnixPty); ok {
				_ = unixPTY.Slave().Close()
			}
			killTree := external.OwnProcessTree(cmd.Process)
			return &authProcess{
				cmd:      cmd,
				pty:      terminal,
				reader:   terminal,
				killTree: killTree,
				waitFn:   func() error { return xpty.WaitProcess(context.Background(), cmd) },
				tempDir:  tempDir,
			}, nil
		}
		if spec.Logger != nil {
			spec.Logger.Warn("[agent-runtime] auth PTY unavailable; falling back to pipes",
				"engine", spec.Engine, "session_id", spec.SessionID)
		}
	}

	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		_ = os.RemoveAll(tempDir)
		return nil, fmt.Errorf("create auth output pipe: %w", err)
	}
	cmd := exec.Command(spec.Executable, spec.Args...)
	cmd.Dir = tempDir
	cmd.Env = spec.Env
	cmd.Stdout = writePipe
	cmd.Stderr = writePipe
	external.ConfigureProcess(cmd)

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		_ = readPipe.Close()
		_ = writePipe.Close()
		_ = os.RemoveAll(tempDir)
		return nil, fmt.Errorf("create auth input pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = readPipe.Close()
		_ = writePipe.Close()
		_ = stdinPipe.Close()
		_ = os.RemoveAll(tempDir)
		return nil, fmt.Errorf("start %s auth process: %w", spec.Engine, err)
	}
	_ = writePipe.Close()
	killTree := external.OwnProcessTree(cmd.Process)
	return &authProcess{
		cmd:      cmd,
		reader:   readPipe,
		stdin:    stdinPipe,
		killTree: killTree,
		waitFn:   cmd.Wait,
		tempDir:  tempDir,
	}, nil
}

func (p *authProcess) Read(buf []byte) (int, error) {
	if p == nil || p.reader == nil {
		return 0, io.EOF
	}
	return p.reader.Read(buf)
}

func (p *authProcess) WriteInput(value string) error {
	if p == nil {
		return errors.New("authentication process is unavailable")
	}
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if p.pty != nil {
		// Interactive PTY prompts expect carriage return (Enter).
		_, err := io.WriteString(p.pty, value+"\r")
		return err
	}
	if p.stdin == nil {
		return errors.New("authentication process input is unavailable")
	}
	// Standard pipes have no terminal line discipline to translate CR to LF.
	_, err := io.WriteString(p.stdin, value+"\n")
	return err
}

func (p *authProcess) Terminate() {
	if p == nil {
		return
	}
	if p.killTree != nil {
		p.killTree()
	}
	p.CloseHandles()
}

func (p *authProcess) CloseHandles() {
	if p == nil {
		return
	}
	p.closeOnce.Do(func() {
		if p.stdin != nil {
			_ = p.stdin.Close()
		}
		if p.pty != nil {
			_ = p.pty.Close()
			return
		}
		if p.reader != nil {
			_ = p.reader.Close()
		}
	})
}

func (p *authProcess) Wait() error {
	if p == nil || p.waitFn == nil {
		return nil
	}
	err := p.waitFn()
	if p.killTree != nil {
		p.killTree()
	}
	return err
}

func (p *authProcess) Cleanup() {
	if p == nil {
		return
	}
	p.CloseHandles()
	p.cleanOnce.Do(func() {
		if p.tempDir != "" {
			_ = os.RemoveAll(p.tempDir)
		}
	})
}

// runLogoutCommand runs a short-lived non-interactive CLI logout command inside
// an isolated temporary directory while discarding stdout/stderr so raw CLI
// output can never leak into logs or error strings.
func runLogoutCommand(ctx context.Context, engine config.RuntimeID, executable string, args []string, env []string) error {
	if strings.TrimSpace(executable) == "" {
		return ErrEngineNotInstalled
	}
	runCtx, cancel := context.WithTimeout(ctx, authLogoutTimeout)
	defer cancel()

	tempDir, err := os.MkdirTemp("", "denova-auth-logout-")
	if err != nil {
		return fmt.Errorf("create logout working directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

	cmd := exec.Command(executable, args...)
	cmd.Dir = tempDir
	cmd.Env = env
	cmd.Stdin = strings.NewReader("")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	external.ConfigureProcess(cmd)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s logout process: %w", engine, err)
	}
	killTree := external.OwnProcessTree(cmd.Process)
	stopCancel := context.AfterFunc(runCtx, killTree)
	waitErr := cmd.Wait()
	stopCancel()
	killTree()

	if runCtx.Err() != nil {
		return runCtx.Err()
	}
	if waitErr != nil {
		return fmt.Errorf("%s logout command failed", engine)
	}
	return nil
}

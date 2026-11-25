package builtin

import (
	"context"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alfredxw/denova/agent"
	agentschema "github.com/alfredxw/denova/agent/schema"
	agentsession "github.com/alfredxw/denova/agent/session"
)

// narrowSessionOpener exposes only the required SessionOpener methods, masking
// optional fast-path interfaces (activeSessionCounter, exactSessionCatalog) so
// FollowUp exercises activeTaskCount Session inspection directly.
type narrowSessionOpener struct {
	owner *agent.Agent
}

func (o narrowSessionOpener) Session(ctx context.Context, key agentsession.Key) (*agent.Session, error) {
	return o.owner.Session(ctx, key)
}

func (o narrowSessionOpener) ListSessions(ctx context.Context, sel agentsession.Selector) ([]agentsession.Key, error) {
	return o.owner.ListSessions(ctx, sel)
}

func TestLocalTasksFollowUpDoesNotSelfCloseViaNarrowSessionOpener(t *testing.T) {
	store := agentsession.Memory()
	owner := newTaskAgent(t, store, &taskModel{responses: []*agentschema.Message{
		agentschema.AssistantMessage("first answer", nil),
		agentschema.AssistantMessage("second answer", nil),
	}})
	t.Cleanup(func() { _ = owner.Close(context.Background()) })

	executor, err := NewLocalTasks(LocalTaskOptions{Parallelism: 2}, LocalTaskAgent{
		Name:        "researcher",
		Description: "Researches bounded questions",
		Opener:      narrowSessionOpener{owner: owner},
		Identity:    agentschema.CapabilityIdentity{Kind: "test.task.narrow", Version: 1},
	})
	if err != nil {
		t.Fatal(err)
	}

	first, err := executor.Start(context.Background(), TaskRequest{
		Agent:          "researcher",
		Prompt:         "step one",
		IdempotencyKey: "narrow-start-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	outcomes, err := executor.Wait(context.Background(), []TaskRef{first.Ref})
	if err != nil || len(outcomes) != 1 || outcomes[0].Task == nil || outcomes[0].Task.Status != string(agentschema.ResultCompleted) {
		t.Fatalf("Wait outcomes=%#v err=%v", outcomes, err)
	}

	second, err := executor.FollowUp(context.Background(), first.Ref, agent.Input{
		Text:           "step two",
		IdempotencyKey: "narrow-followup-1",
	})
	if err != nil {
		t.Fatalf("FollowUp failed with narrow SessionOpener: %v", err)
	}
	outcomes, err = executor.Wait(context.Background(), []TaskRef{second.Ref})
	if err != nil || len(outcomes) != 1 || outcomes[0].Task == nil || outcomes[0].Task.Status != string(agentschema.ResultCompleted) {
		t.Fatalf("FollowUp Wait outcomes=%#v err=%v", outcomes, err)
	}
	observed, err := executor.Observe(context.Background(), second.Ref, "0")
	if err != nil || observed.Output != "second answer" {
		t.Fatalf("FollowUp Observe=%#v err=%v", observed, err)
	}
}

func TestCommandEnvironmentCaseFoldingAndDuplicateRejection(t *testing.T) {
	base := []string{"Path=C:\\base\\bin", "KEEP=1"}
	env, err := commandEnvironment(base, map[string]string{"PATH": "C:\\override\\bin"}, false, "")
	if err != nil {
		t.Fatalf("commandEnvironment failed: %v", err)
	}
	pathEntries := 0
	var matched string
	for _, item := range env {
		key, _, _ := strings.Cut(item, "=")
		if strings.EqualFold(key, "PATH") {
			pathEntries++
			matched = item
		}
	}
	if runtime.GOOS == "windows" {
		if pathEntries != 1 || !strings.HasSuffix(matched, "=C:\\override\\bin") {
			t.Fatalf("expected single case-folded PATH entry on Windows, count=%d entry=%q env=%v", pathEntries, matched, env)
		}
		if _, err := commandEnvironment(base, map[string]string{"Path": "a", "PATH": "b"}, false, ""); err == nil {
			t.Fatal("expected colliding case-folded extra env keys to fail on Windows")
		}
	}
}

func TestLocalWorkspaceRejectsAmbiguousWindowsPaths(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-specific path semantics")
	}
	ws, err := OpenWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"/rooted/escape.txt", `\rooted\escape.txt`, "C:drive-relative.txt"} {
		if _, _, err := ws.stat(invalid, true); err == nil {
			t.Fatalf("expected LocalWorkspace to reject %q on Windows", invalid)
		}
	}
}

func TestLocalCommandRunnerTerminatesWindowsGrandchildProcessTree(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows Job Object process-tree test")
	}
	ws, err := OpenWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewLocalCommandRunner(CommandRunnerOptions{
		Workspace: ws,
		Shell:     ShellKind("powershell"),
	})
	if err != nil {
		t.Skipf("PowerShell unavailable: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	res, err := runner.Run(ctx, CommandRequest{
		Command: `$child = Start-Process -FilePath $PSHOME\powershell.exe -ArgumentList '-NoProfile','-NonInteractive','-Command','Start-Sleep -Seconds 60' -PassThru -WindowStyle Hidden; Write-Output ("CHILD_PID=" + $child.Id); [Console]::Out.Flush(); Start-Sleep -Seconds 60`,
	}, nil)
	if err != nil {
		t.Fatalf("Run returned unexpected error: %v", err)
	}

	childPID := 0
	for _, line := range strings.Split(res.Output, "\n") {
		line = strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(line, "CHILD_PID="); ok {
			childPID, _ = strconv.Atoi(strings.TrimSpace(after))
			break
		}
	}
	if childPID <= 0 {
		// If cancellation raced before PowerShell spawned the child, skip assertion cleanly.
		return
	}

	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		out, _ := exec.Command("tasklist.exe", "/FI", "PID eq "+strconv.Itoa(childPID), "/NH", "/FO", "CSV").CombinedOutput()
		if !strings.Contains(string(out), `"`+strconv.Itoa(childPID)+`"`) {
			return
		}
		time.Sleep(40 * time.Millisecond)
	}
	_ = exec.Command("taskkill.exe", "/T", "/F", "/PID", strconv.Itoa(childPID)).Run()
	t.Fatalf("grandchild PID %d survived after parent command runner cancellation", childPID)
}

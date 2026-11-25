package builtin

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/alfredxw/denova/agent"
	agentevent "github.com/alfredxw/denova/agent/lifecycle/event"
	agentschema "github.com/alfredxw/denova/agent/schema"
)

// FollowUp accepts a distinct Run in the existing child Session. HostData is
// captured from this parent invocation, never from the child's first task.
func (tasks *LocalTasks) FollowUp(ctx context.Context, ref TaskRef, input agent.Input) (Task, error) {
	candidate, err := tasks.agent(ref.Agent)
	if err != nil {
		return Task{}, err
	}
	localTaskAdmissionMu.Lock()
	defer localTaskAdmissionMu.Unlock()
	type followUpState struct {
		session        *agent.Session
		snapshot       agentevent.SessionSnapshot
		acceptedBefore bool
	}
	opened, err := retryClosedTaskRead(ctx, func() (followUpState, error) {
		session, err := tasks.openExisting(ctx, candidate, ref.Session)
		if err != nil {
			return followUpState{}, err
		}
		snapshot, err := session.Snapshot(ctx)
		if err != nil {
			return followUpState{}, err
		}
		_, acceptedBefore, err := session.CommandSnapshot(ctx, input.IdempotencyKey)
		if err != nil {
			return followUpState{}, err
		}
		return followUpState{session: session, snapshot: snapshot, acceptedBefore: acceptedBefore}, nil
	})
	if err != nil {
		return Task{}, err
	}
	session, snapshot, acceptedBefore := opened.session, opened.snapshot, opened.acceptedBefore
	if snapshot.ActiveRunID == "" && !acceptedBefore {
		if err := tasks.checkCapacity(ctx); err != nil {
			return Task{}, err
		}
	}
	input.HostData = cloneTaskHostData(candidate.HostData)
	receipt, err := session.FollowUp(ctx, input)
	if err != nil {
		return Task{}, err
	}
	ref.Run = receipt.RunID
	accepted := Task{Ref: ref, Receipt: &receipt}
	run, found, err := session.AttachRun(ctx, ref.Run)
	if err != nil {
		return accepted, err
	}
	if !found {
		return accepted, errors.New("accepted child Run was not found")
	}
	if err := tasks.trackTaskCompletion(ctx, ref); err != nil {
		return accepted, err
	}
	tasks.watchCompletion(ctx, run, ref)
	snapshot, err = session.Snapshot(ctx)
	if err != nil {
		return accepted, err
	}
	task, err := taskFromSnapshot(ref, snapshot)
	if err != nil {
		return accepted, err
	}
	task.Receipt = &receipt
	return task, err
}

// SendMessage persists supplemental input only. An idle child remains idle;
// a suspended child remains suspended until explicitly resumed.
func (tasks *LocalTasks) SendMessage(ctx context.Context, ref TaskRef, input agent.Input) (agentevent.CommandReceipt, error) {
	candidate, err := tasks.agent(ref.Agent)
	if err != nil {
		return agentevent.CommandReceipt{}, err
	}
	localTaskAdmissionMu.Lock()
	defer localTaskAdmissionMu.Unlock()
	type sendState struct {
		session  *agent.Session
		hostData *agent.HostData
	}
	opened, err := retryClosedTaskRead(ctx, func() (sendState, error) {
		session, err := tasks.openExisting(ctx, candidate, ref.Session)
		if err != nil {
			return sendState{}, err
		}
		var hostData *agent.HostData
		if active, found, err := session.Active(ctx); err != nil {
			return sendState{}, err
		} else if found {
			original, _, err := session.RunInput(ctx, active.ID())
			if err != nil {
				return sendState{}, err
			}
			hostData = original.HostData
		}
		return sendState{session: session, hostData: hostData}, nil
	})
	if err != nil {
		return agentevent.CommandReceipt{}, err
	}
	session := opened.session
	input.HostData = opened.hostData
	if tasks.completionParent != nil {
		if err := tasks.completionParent.RegisterChildTask(ctx, agent.ChildTask{Key: session.Key()}); err != nil {
			return agentevent.CommandReceipt{}, err
		}
	}
	queued, err := session.Queue(ctx, input)
	if err != nil {
		return agentevent.CommandReceipt{}, err
	}
	return queued.Receipt(), nil
}

func (tasks *LocalTasks) Resume(ctx context.Context, ref TaskRef, request agent.ResumeRequest) (Task, error) {
	localTaskAdmissionMu.Lock()
	defer localTaskAdmissionMu.Unlock()
	type resumeState struct {
		session  *agent.Session
		snapshot agentevent.SessionSnapshot
	}
	opened, err := retryClosedTaskRead(ctx, func() (resumeState, error) {
		_, session, err := tasks.open(ctx, ref)
		if err != nil {
			return resumeState{}, err
		}
		snapshot, err := session.Snapshot(ctx)
		if err != nil {
			return resumeState{}, err
		}
		return resumeState{session: session, snapshot: snapshot}, nil
	})
	if err != nil {
		return Task{}, err
	}
	session, snapshot := opened.session, opened.snapshot
	if _, accepted := session.AcceptedControl(request.IdempotencyKey); !accepted {
		if snapshot.ActiveRunID != ref.Run {
			return Task{}, agentschema.ErrNoActiveRun
		}
		if snapshot.ActiveStatus != agentschema.ResultSuspended {
			return Task{}, ErrTaskInvalidState
		}
		if err := tasks.checkCapacity(ctx); err != nil {
			return Task{}, err
		}
	}
	if request.RunID != "" && request.RunID != ref.Run {
		return Task{}, errors.New("resume Run ID does not match the task ref")
	}
	request.RunID = ref.Run
	run, receipt, err := session.ResumeRunWithReceipt(ctx, request)
	if err != nil {
		return Task{}, err
	}
	accepted := Task{Ref: ref, Receipt: &receipt}
	if run == nil {
		return accepted, nil
	}
	if err := tasks.trackTaskCompletion(ctx, ref); err != nil {
		return accepted, err
	}
	tasks.watchCompletion(ctx, run, ref)
	snapshot, err = session.Snapshot(ctx)
	if err != nil {
		return accepted, err
	}
	task, err := taskFromSnapshot(ref, snapshot)
	if err != nil {
		return accepted, err
	}
	task.Receipt = &receipt
	return task, err
}

func (tasks *LocalTasks) checkCapacity(ctx context.Context) error {
	active, err := tasks.activeTaskCount(ctx)
	if err != nil {
		return fmt.Errorf("count active tasks: %w", err)
	}
	if active >= tasks.parallelism {
		return fmt.Errorf("%w: %d active tasks reached the configured limit of %d", ErrTaskCapacityExceeded, active, tasks.parallelism)
	}
	return nil
}

func validateTaskSessionRef(ref TaskRef) error {
	if err := validateTaskString("ref.agent", ref.Agent, 256); err != nil {
		return err
	}
	if err := validateTaskString("ref.session", ref.Session, 1024); err != nil {
		return err
	}
	if strings.TrimSpace(ref.Run) != "" && len(ref.Run) > 1024 {
		return fmt.Errorf("%w: ref.run exceeds 1024 bytes", ErrTaskInvalidInput)
	}
	return nil
}

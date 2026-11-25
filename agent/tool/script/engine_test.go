package script

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type stubHost struct {
	fn    func(context.Context, []Call) ([]Outcome, error)
	calls [][]Call
}

func (host *stubHost) CallTools(ctx context.Context, calls []Call) ([]Outcome, error) {
	cloned := append([]Call(nil), calls...)
	host.calls = append(host.calls, cloned)
	if host.fn != nil {
		return host.fn(ctx, calls)
	}
	outcomes := make([]Outcome, len(calls))
	for i, call := range calls {
		outcomes[i] = Outcome{
			Tool: call.Name, OK: true, Status: "success", Output: json.RawMessage(`"ok"`),
		}
	}
	return outcomes, nil
}

func TestParallelRejectsSparseOversizedArrayWithoutOOM(t *testing.T) {
	engine, err := NewEngine(Config{})
	if err != nil {
		t.Fatal(err)
	}
	program, diagnostics := engine.Compile(t.Context(), Source{
		Name: "sparse.js",
		Code: `const a = []; a.length = 2147483647; return ctx.tools.parallel(a);`,
	})
	if len(diagnostics) != 0 {
		t.Fatalf("unexpected compile diagnostics: %#v", diagnostics)
	}
	host := &stubHost{}
	result, err := engine.Run(t.Context(), program, host, nil)
	if err != nil {
		t.Fatalf("unexpected host error: %v", err)
	}
	if result.Failure != nil {
		t.Fatalf("unexpected runtime failure: %#v", result.Failure)
	}
	if len(host.calls) != 0 {
		t.Fatalf("expected host not to be called, got %d batches", len(host.calls))
	}
	var outcomes []Outcome
	if err := json.Unmarshal(result.Value, &outcomes); err != nil {
		t.Fatalf("decode parallel outcomes: %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].OK || outcomes[0].Reason != "invalid_arguments" {
		t.Fatalf("unexpected outcomes: %#v", outcomes)
	}
	if !strings.Contains(string(outcomes[0].Output), "exceeds the 256-call limit") {
		t.Fatalf("unexpected error output: %s", outcomes[0].Output)
	}
}

func TestHostCallToolsErrorCannotBeSwallowedByGuestTryCatch(t *testing.T) {
	engine, err := NewEngine(Config{})
	if err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("fatal host cancellation")
	for _, tc := range []struct {
		name string
		code string
	}{
		{
			name: "single_call",
			code: `
try {
	ctx.tools.call("first", { x: 1 });
	ctx.log("after_first");
	ctx.tools.call("second", { x: 2 });
} catch (err) {
	ctx.log("caught");
	ctx.tools.call("in_catch", {});
}
return "swallowed";`,
		},
		{
			name: "parallel_call",
			code: `
try {
	ctx.tools.parallel([{ tool: "first", input: {} }]);
	ctx.log("after_parallel");
	ctx.tools.call("second", {});
} catch (err) {
	ctx.log("caught_parallel");
}
return "swallowed";`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			program, diagnostics := engine.Compile(t.Context(), Source{Name: "interrupt.js", Code: tc.code})
			if len(diagnostics) != 0 {
				t.Fatalf("compile diagnostics: %#v", diagnostics)
			}
			host := &stubHost{
				fn: func(context.Context, []Call) ([]Outcome, error) {
					return nil, sentinel
				},
			}
			result, runErr := engine.Run(t.Context(), program, host, nil)
			if !errors.Is(runErr, sentinel) {
				t.Fatalf("runErr = %v, want %v (result=%#v)", runErr, sentinel, result)
			}
			if len(host.calls) != 1 {
				t.Fatalf("host called %d times, want 1", len(host.calls))
			}
		})
	}
}

func TestEngineEnforcesCallStackLogsAndParallelSlotOrdering(t *testing.T) {
	engine, err := NewEngine(Config{MaxCallStackSize: 32, MaxOutputBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}

	recursive, diagnostics := engine.Compile(t.Context(), Source{
		Name: "recurse.js",
		Code: `function f() { return f(); } return f();`,
	})
	if len(diagnostics) != 0 {
		t.Fatalf("compile diagnostics: %#v", diagnostics)
	}
	res, err := engine.Run(t.Context(), recursive, &stubHost{}, nil)
	if err != nil || res.Failure == nil || res.Failure.Kind != "script_runtime_failed" {
		t.Fatalf("expected stack overflow runtime failure, got res=%#v err=%v", res, err)
	}

	parallelProg, diagnostics := engine.Compile(t.Context(), Source{
		Name: "parallel.js",
		Code: `
ctx.log("step-1");
return ctx.tools.parallel([
	{ tool: "alpha", input: { n: 1 } },
	"bad-slot",
	{ tool: "beta", input: { n: 2 } }
]);`,
	})
	if len(diagnostics) != 0 {
		t.Fatalf("compile diagnostics: %#v", diagnostics)
	}
	host := &stubHost{
		fn: func(_ context.Context, calls []Call) ([]Outcome, error) {
			outcomes := make([]Outcome, len(calls))
			for i, call := range calls {
				outcomes[i] = Outcome{Tool: call.Name, OK: true, Status: "success", Output: call.Arguments}
			}
			return outcomes, nil
		},
	}
	res, err = engine.Run(t.Context(), parallelProg, host, nil)
	if err != nil || res.Failure != nil {
		t.Fatalf("parallel run failed: res=%#v err=%v", res, err)
	}
	var outcomes []Outcome
	if err := json.Unmarshal(res.Value, &outcomes); err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 3 || !outcomes[0].OK || outcomes[1].OK || !outcomes[2].OK {
		t.Fatalf("unexpected slot outcomes: %#v", outcomes)
	}
	if outcomes[0].Tool != "alpha" || outcomes[1].Reason != "invalid_arguments" || outcomes[2].Tool != "beta" {
		t.Fatalf("unexpected slot details: %#v", outcomes)
	}
}

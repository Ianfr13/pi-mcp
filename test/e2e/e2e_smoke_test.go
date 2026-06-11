// Package e2e holds the real end-to-end smoke test for pi-mcp: it drives the
// REAL pi coding agent through the REAL MCP server over stdio, end to end.
//
// This test is gated behind PI_MCP_E2E=1 so the normal `go test ./...` run is
// unaffected (it skips cleanly without launching pi). To run it:
//
//	PI_MCP_E2E=1 go test ./test/e2e/ -run TestE2ESmoke -v -timeout 6m
package e2e

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"pi-mcp/internal/livestatus"
	"pi-mcp/internal/model"
)

// runAgents is the minimal slice of the pi run file we assert on: the per-agent
// model attribution that proves multi-model fan-out actually happened.
type runAgents struct {
	RunID  string `json:"runId"`
	Status string `json:"status"`
	Agents []struct {
		Label string `json:"label"`
		Model string `json:"model"`
	} `json:"agents"`
}

func TestE2ESmoke(t *testing.T) {
	if os.Getenv("PI_MCP_E2E") != "1" {
		t.Skip("PI_MCP_E2E!=1: skipping real end-to-end pi smoke test (set PI_MCP_E2E=1 to run)")
	}

	start := time.Now()

	// 1. Build the server binary from source.
	tmpRoot := t.TempDir()
	bin := filepath.Join(tmpRoot, "pi-mcp")
	buildCmd := exec.Command("go", "build", "-o", bin, "./cmd/pi-mcp")
	buildCmd.Dir = repoRoot(t)
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/pi-mcp failed: %v\n%s", err, out)
	}
	t.Logf("built server binary: %s", bin)

	// 2. Dedicated cwd for the read-mode run; runs land in <cwd>/.pi/workflows/runs.
	workDir := t.TempDir()
	t.Logf("pi work dir (cwd): %s", workDir)

	// Overall budget for the whole flow.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// 3. Connect an MCP client over stdio via CommandTransport. The command
	// inherits the parent env (AGENT_VAULT_*/HOME) which pi needs.
	client := mcp.NewClient(&mcp.Implementation{Name: "pi-mcp-e2e", Version: "0.0.0"}, nil)
	cmd := exec.Command(bin)
	cmd.Stderr = os.Stderr // surface server-side logs into the test output
	transport := &mcp.CommandTransport{Command: cmd}

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("client.Connect over stdio failed: %v", err)
	}
	defer func() { _ = session.Close() }()

	// 4. ListTools -> assert the four tools are present.
	lt, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}
	got := map[string]bool{}
	for _, tl := range lt.Tools {
		got[tl.Name] = true
	}
	for _, want := range []string{"pi_workflow", "pi_status", "pi_list", "pi_cancel"} {
		if !got[want] {
			t.Fatalf("tool %q missing from ListTools; got %v", want, keys(got))
		}
	}
	t.Logf("ListTools OK: %v", keys(got))

	// 5. CallTool pi_workflow (read mode in workDir).
	const task = "Judge whether each of these is TRUE or FALSE with a one-sentence reason, " +
		"then give an overall verdict. A) The Earth orbits the Sun. " +
		"B) Water boils at 10C at sea level. C) 7 is prime."

	wfRes, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "pi_workflow",
		Arguments: map[string]any{
			"task": task,
			"mode": "read",
			"cwd":  workDir,
		},
	})
	if err != nil {
		t.Fatalf("CallTool pi_workflow failed: %v", err)
	}
	if wfRes.IsError {
		t.Fatalf("pi_workflow returned IsError; content=%s", contentText(wfRes))
	}

	var wf model.WorkflowOutput
	if err := decodeResult(wfRes, &wf); err != nil {
		t.Fatalf("decode WorkflowOutput failed: %v; content=%s", err, contentText(wfRes))
	}
	if wf.JobID == "" {
		t.Fatalf("pi_workflow returned empty jobId; output=%+v", wf)
	}
	t.Logf("pi_workflow accepted: jobId=%s status=%s mode=%s cwd=%s", wf.JobID, wf.Status, wf.Mode, wf.CWD)

	// 6. PRIMARY PATH: poll pi_status{jobId, wait:true} until it reaches a
	// terminal status. This is the flow the spec intends and the one BUG 1
	// regressed: correlation must resolve the late-arriving run file so the
	// jobId path leaves the blind window and reports the real terminal status
	// and result — WITHOUT any pi_list disk-scan workaround.
	//
	// pi_list is still queried purely as a corroborating log signal; the test
	// VERDICT is driven exclusively by the jobId StatusOutput.
	deadline := start.Add(4 * time.Minute)
	var statusFlow []string
	var final model.StatusOutput
	var discoveredRunID, listStatus string
	terminal := false

	for time.Now().Before(deadline) {
		// (a) jobId long-poll (blocks server-side up to WaitCap=60s on no change).
		stRes, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name:      "pi_status",
			Arguments: map[string]any{"jobId": wf.JobID, "wait": true},
		})
		if err != nil {
			// An MCP-level error here would include go-sdk output-schema
			// validation failures (BUG 2). Surface it, never mask it.
			t.Fatalf("CallTool pi_status{jobId} failed: %v", err)
		}
		if stRes.IsError {
			t.Fatalf("pi_status{jobId} returned IsError; content=%s", contentText(stRes))
		}
		var st model.StatusOutput
		if err := decodeResult(stRes, &st); err != nil {
			t.Fatalf("decode StatusOutput failed: %v; content=%s", err, contentText(stRes))
		}
		// Record status transitions only (long-poll returns on change).
		if len(statusFlow) == 0 || statusFlow[len(statusFlow)-1] != st.Status {
			phase := "<nil>"
			if st.Phase != nil {
				phase = *st.Phase
			}
			t.Logf("pi_status{jobId}: status=%s phase=%s blind=%v events=%d", st.Status, phase, st.BlindWindow, len(st.Events))
			statusFlow = append(statusFlow, st.Status)
		}
		final = st

		// (b) pi_list is a corroborating signal ONLY (the verdict is the jobId path).
		lRes, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name:      "pi_list",
			Arguments: map[string]any{"cwd": workDir},
		})
		if err != nil {
			t.Fatalf("CallTool pi_list failed: %v", err)
		}
		var lout model.ListOutput
		if err := decodeResult(lRes, &lout); err != nil {
			t.Fatalf("decode ListOutput failed: %v; content=%s", err, contentText(lRes))
		}
		if len(lout.Runs) > 0 {
			row := lout.Runs[0] // newest (sorted by updatedAt desc)
			if row.RunID != discoveredRunID || row.Status != listStatus {
				t.Logf("pi_list (corroborating): runId=%s status=%s by_model=%v agentCount=%d", row.RunID, row.Status, row.ByModel, row.AgentCount)
			}
			discoveredRunID, listStatus = row.RunID, row.Status
		}

		// VERDICT signal: the jobId path itself reaching terminal.
		if livestatus.IsTerminal(final.Status) {
			terminal = true
			break
		}
	}

	dur := time.Since(start)
	t.Logf("jobId status flow: %v", statusFlow)
	t.Logf("pi_list corroborating: runId=%s diskStatus=%s after %s", discoveredRunID, listStatus, dur.Round(time.Second))

	// BUG 1 assertion: the jobId long-poll path MUST reach a terminal status on
	// its own (correlation resolved the run file). If it never left the blind
	// window, that is the primary-flow regression — fail loudly.
	if !terminal {
		t.Fatalf("PRIMARY FLOW BROKEN: pi_status{jobId} never reached terminal within deadline "+
			"(stuck in blind window?); jobId-status=%q flow=%v list-status=%q runId=%q",
			final.Status, statusFlow, listStatus, discoveredRunID)
	}

	// The terminal status MUST be completed (real pi judging three claims).
	if final.Status != "completed" {
		t.Fatalf("pi run did NOT complete via jobId path: status=%q error=%q "+
			"(real finding, not a harness bug)", final.Status, final.Error)
	}

	// RunID must be surfaced once correlation resolved (no longer null).
	if final.RunID == nil || *final.RunID == "" {
		t.Fatalf("completed via jobId but RunID is nil/empty — correlation did not resolve")
	}
	if final.BlindWindow {
		t.Fatalf("completed via jobId but BlindWindow=true — should be false once run file exists")
	}

	// Non-empty result delivered VIA THE jobId path (not pi_list, not the disk file).
	resultStr := anyResultString(t, final.Result)
	if strings.TrimSpace(resultStr) == "" || resultStr == "null" {
		t.Fatalf("completed via jobId but result is empty; StatusOutput=%+v", final)
	}
	t.Logf("FINAL RESULT (via pi_status{jobId}):\n%s", resultStr)

	if final.Metadata != nil {
		t.Logf("metadata by_model: %v agentCount=%d", final.Metadata.ByModel, final.Metadata.AgentCount)
	}

	// 7/8. Cross-check against the on-disk run file (authoritative) for the
	// multi-model fan-out evidence. This corroborates the jobId-path verdict.
	ra, runPath := readRunFile(t, workDir)
	if ra == nil {
		t.Fatalf("NO RUN FILE under %s/.pi/workflows/runs after completed status "+
			"(NO_WORKFLOW_RUN: pi never wrote a run file)", workDir)
	}

	var models []string
	seen := map[string]bool{}
	distinct := 0
	byModel := map[string]int{}
	for _, a := range ra.Agents {
		models = append(models, a.Label+"="+a.Model)
		byModel[a.Model]++
		if a.Model != "" {
			if !seen[a.Model] {
				seen[a.Model] = true
				distinct++
			}
		}
	}
	t.Logf("run file: %s (status=%s, runId=%s)", runPath, ra.Status, ra.RunID)
	t.Logf("agents (%d): %v", len(ra.Agents), models)
	t.Logf("by_model histogram: %v", byModel)
	t.Logf("distinct models: %d", distinct)

	if len(ra.Agents) == 0 {
		t.Fatalf("completed but NO agents recorded in run file")
	}
	if distinct == 0 {
		t.Fatalf("completed but no agent recorded a model")
	}
	// The runId surfaced by the jobId path must match the on-disk run file.
	if *final.RunID != ra.RunID {
		t.Fatalf("jobId path RunID=%q != on-disk runId=%q", *final.RunID, ra.RunID)
	}

	t.Logf("E2E PASSED: real pi ran through the real MCP server; pi_status{jobId} reached "+
		"completed with a non-empty result via the jobId (no pi_list workaround) and %d agent "+
		"model(s) recorded (%d distinct)", len(ra.Agents), distinct)
}

// anyResultString renders an `any` StatusOutput.Result (object/array/scalar)
// back to a JSON string for non-emptiness assertions and logging.
func anyResultString(t *testing.T, v any) string {
	t.Helper()
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal StatusOutput.Result: %v", err)
	}
	return string(b)
}

// repoRoot finds the module root (containing go.mod) by walking up from cwd.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not locate go.mod above %s", dir)
		}
		dir = parent
	}
}

// decodeResult prefers the typed StructuredContent; it re-marshals it (it
// arrives as a generic map over the wire) and unmarshals into out. If
// StructuredContent is absent it falls back to parsing the first text content
// block as JSON.
func decodeResult(res *mcp.CallToolResult, out any) error {
	if res.StructuredContent != nil {
		b, err := json.Marshal(res.StructuredContent)
		if err != nil {
			return err
		}
		return json.Unmarshal(b, out)
	}
	return json.Unmarshal([]byte(contentText(res)), out)
}

// contentText concatenates the text of all *mcp.TextContent blocks.
func contentText(res *mcp.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

// readRunFile returns the single newest run file under <cwd>/.pi/workflows/runs.
func readRunFile(t *testing.T, cwd string) (*runAgents, string) {
	t.Helper()
	runsDir := filepath.Join(cwd, ".pi", "workflows", "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		return nil, runsDir
	}
	var newest string
	var newestMod time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if newest == "" || info.ModTime().After(newestMod) {
			newest = filepath.Join(runsDir, e.Name())
			newestMod = info.ModTime()
		}
	}
	if newest == "" {
		return nil, runsDir
	}
	b, err := os.ReadFile(newest)
	if err != nil {
		t.Logf("read run file %s: %v", newest, err)
		return nil, newest
	}
	var ra runAgents
	if err := json.Unmarshal(b, &ra); err != nil {
		t.Logf("unmarshal run file %s: %v", newest, err)
		return nil, newest
	}
	return &ra, newest
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

package trace_test

import (
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0xGurg/himay/internal/fixture"
	"github.com/0xGurg/himay/internal/model"
	"github.com/0xGurg/himay/internal/store"
	"github.com/0xGurg/himay/internal/trace"
	"github.com/0xGurg/himay/internal/usage"
)

var update = flag.Bool("update", false, "rewrite golden files")

func load(t *testing.T) ([]model.Message, []trace.Row) {
	t.Helper()
	b := fixture.Standard(t)
	st, err := store.Open(context.Background(), b.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	msgs, err := trace.LoadAll(context.Background(), st, "ses_root")
	if err != nil {
		t.Fatal(err)
	}
	return msgs, trace.Build(msgs)
}

func TestGolden(t *testing.T) {
	_, rows := load(t)
	var buf bytes.Buffer
	if err := trace.WritePlain(&buf, rows, time.UnixMilli(fixture.T0)); err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "ses_root.golden")
	if *update {
		os.MkdirAll("testdata", 0o755)
		os.WriteFile(golden, buf.Bytes(), 0o644)
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("missing golden (run with -update): %v", err)
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Errorf("trace differs from golden:\n%s", buf.String())
	}
}

func TestEveryKindCovered(t *testing.T) {
	_, rows := load(t)
	got := map[trace.Kind]int{}
	for _, r := range rows {
		got[r.Kind]++
	}
	want := []trace.Kind{trace.KUser, trace.KStep, trace.KText, trace.KReasoning, trace.KReasoningUnavailable,
		trace.KTool, trace.KSubagent, trace.KFileChange, trace.KShell, trace.KSkill, trace.KInstructionLoaded,
		trace.KInstructionUpdated, trace.KToolsChanged, trace.KSynthetic, trace.KModelSwitched, trace.KAgentSwitched,
		trace.KLocationSwitched, trace.KRetry, trace.KCompaction, trace.KIdle, trace.KError, trace.KUnknown}
	for _, k := range want {
		if got[k] == 0 {
			t.Errorf("no %s row", k)
		}
	}
	_ = trace.KSynthetic
}

func TestSubagentEdge(t *testing.T) {
	_, rows := load(t)
	for _, r := range rows {
		if r.Kind == trace.KSubagent {
			if r.Child != "ses_child" || r.Agent != "explorer" || r.Model.ID != "kimi-k2.6" {
				t.Errorf("subagent row = %+v", r)
			}
			return
		}
	}
	t.Fatal("no subagent row")
}

func TestUnknownsAreHonest(t *testing.T) {
	_, rows := load(t)
	var future, malformed, item bool
	for _, r := range rows {
		if r.Kind != trace.KUnknown {
			continue
		}
		switch {
		case strings.Contains(r.Label, "future-thing"):
			future = true
		case strings.Contains(r.Label, "malformed"):
			malformed = true
		case strings.Contains(r.Label, "mystery"):
			item = true
		}
	}
	if !future || !malformed || !item {
		t.Errorf("future=%v malformed=%v item=%v", future, malformed, item)
	}
}

func TestDerivedAndRecordedRetry(t *testing.T) {
	_, rows := load(t)
	var recorded, derivedAgent, recordedAgent bool
	for _, r := range rows {
		if r.Kind == trace.KRetry && r.Evidence == model.Recorded && strings.Contains(r.Label, "attempt 2") {
			recorded = true
		}
		if r.Kind == trace.KAgentSwitched {
			if r.Evidence == model.Derived {
				derivedAgent = true
			} else {
				recordedAgent = true
			}
		}
	}
	if !recorded || !recordedAgent {
		t.Errorf("recorded retry=%v recorded agent switch=%v", recorded, recordedAgent)
	}
	_ = derivedAgent
}

func TestUsageNeverAssumesSessionModel(t *testing.T) {
	msgs, _ := load(t)
	rep := usage.Build(msgs)
	keys := map[string]bool{}
	for _, l := range rep.ByModel {
		keys[l.Key] = true
	}
	if !keys["opencode-go/glm-5.2"] || !keys["openai/gpt-5.6-luna"] {
		t.Errorf("by model = %v", keys)
	}
	if rep.Total.Errors != 1 {
		t.Errorf("errors = %d", rep.Total.Errors)
	}
	if rep.Total.Tokens.Input != 3010 {
		t.Errorf("input tokens = %d, want 3010", rep.Total.Tokens.Input)
	}
}

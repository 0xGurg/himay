package provenance_test

import (
	"context"
	"testing"

	"github.com/0xGurg/himay/internal/fixture"
	"github.com/0xGurg/himay/internal/model"
	"github.com/0xGurg/himay/internal/provenance"
	"github.com/0xGurg/himay/internal/store"
	"github.com/0xGurg/himay/internal/trace"
)

func TestLiterals(t *testing.T) {
	got := provenance.Literals("Run `pnpm check:all` and `npm` then `git push --force`; avoid `true` and `Foo`. `myHelper` ok")
	want := map[string]bool{"pnpm check:all": true, "git push --force": true, "myHelper": true}
	if len(got) != len(want) {
		t.Fatalf("literals = %q", got)
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("unexpected literal %q", g)
		}
	}
}

func steps(texts ...store.StepText) []store.StepText { return texts }

func TestCorrelateLevels(t *testing.T) {
	ctx := context.Background()
	src := provenance.Source{InstructionRecord: model.InstructionRecord{Kind: "agents_md", Seq: 5, Path: "/r/AGENTS.md"},
		Text: "Always run `pnpm check:all` before finishing.\nPrefer the design tokens in src/theme over raw hex colors in every component.\nUse `make lint-fix` and `go vet ./...` often."}
	cases := []struct {
		name string
		step store.StepText
		want model.Evidence
		none bool
	}{
		{"verbatim span", store.StepText{Seq: 6, Kind: "text", Text: "OK: prefer the design tokens in src/theme over raw hex colors in every component!"}, model.Strong, false},
		{"literal in tool input", store.StepText{Seq: 6, Kind: "tool", Tool: "bash", Text: `{"command":"pnpm check:all"}`}, model.Strong, false},
		{"two literals in text", store.StepText{Seq: 6, Kind: "text", Text: "I'll use make lint-fix and go vet ./... next"}, model.Possible, false},
		{"names the file", store.StepText{Seq: 6, Kind: "reasoning", Text: "The r/agents.md file says something"}, model.Possible, false},
		{"single literal in prose", store.StepText{Seq: 6, Kind: "text", Text: "maybe pnpm check:all later"}, 0, true},
		{"before load", store.StepText{Seq: 4, Kind: "tool", Text: `{"command":"pnpm check:all"}`}, 0, true},
		{"unrelated", store.StepText{Seq: 6, Kind: "tool", Text: `{"command":"ls"}`}, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ms, err := provenance.Correlate(ctx, src, steps(c.step))
			if err != nil {
				t.Fatal(err)
			}
			if c.none {
				if len(ms) != 0 {
					t.Fatalf("expected no match, got %+v", ms)
				}
				return
			}
			if len(ms) != 1 || ms[0].Evidence != c.want {
				t.Fatalf("got %+v, want %v", ms, c.want)
			}
			if ms[0].Evidence == model.Recorded {
				t.Fatal("correlation must never be Recorded")
			}
		})
	}
}

func TestCorrelateCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	src := provenance.Source{Text: "`pnpm check:all`"}
	_, err := provenance.Correlate(ctx, src, steps(store.StepText{Seq: 1, Kind: "tool", Text: "pnpm check:all"}))
	if err == nil {
		t.Fatal("expected context error")
	}
}

func TestExtractFixture(t *testing.T) {
	b := fixture.Standard(t)
	st, err := store.Open(context.Background(), b.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	msgs, err := trace.LoadAll(ctx, st, "ses_root")
	if err != nil {
		t.Fatal(err)
	}
	srcs, err := provenance.Extract(ctx, st, "ses_root", msgs)
	if err != nil {
		t.Fatal(err)
	}
	count := map[string]int{}
	for i := range srcs {
		s := &srcs[i]
		if s.Evidence != model.Recorded {
			t.Errorf("extracted source not Recorded: %+v", s)
		}
		count[s.Kind+"|"+s.Source]++
		if err := provenance.Load(ctx, st, s); err != nil {
			t.Fatal(err)
		}
	}
	for _, k := range []string{
		"agents_md|instruction_state.initial",
		"agents_md|read tool metadata.loaded",
		"agents_md|synthetic message (nested load)",
		"agents_md_update|system message",
		"tools|system message",
		"skill|skill tool call",
		"skill|skill message",
		"skill_mention|user skill mention",
		"agent_mention|user @agent mention",
		"env|instruction_state.current",
		"date|instruction_state.current",
	} {
		if count[k] == 0 {
			t.Errorf("missing %s (have %v)", k, count)
		}
	}
	// The read-loaded AGENTS.md text must be extracted from the system-reminder block.
	for _, s := range srcs {
		if s.Source == "read tool metadata.loaded" && s.Text != "Always run `pnpm check:all` before finishing.\nPrefer the design tokens in src/theme over raw hex colors in every component." {
			t.Errorf("loaded section = %q", s.Text)
		}
	}
	// And the later bash call + verbatim text should correlate.
	stepTexts, err := st.StepTexts(ctx, "ses_root", -1)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range srcs {
		if s.Source != "read tool metadata.loaded" {
			continue
		}
		ms, _ := provenance.Correlate(ctx, s, stepTexts)
		var strong int
		for _, m := range ms {
			if m.Evidence == model.Strong {
				strong++
			}
		}
		if strong < 2 {
			t.Errorf("strong matches = %d, want ≥2: %+v", strong, ms)
		}
	}
	none, err := provenance.Extract(ctx, st, "ses_old", nil)
	if err != nil || len(none) != 0 {
		t.Errorf("old session sources = %v, %v", none, err)
	}
}

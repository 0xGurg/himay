package decode_test

import (
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/0xGurg/himay/internal/decode"
	"github.com/0xGurg/himay/internal/model"
	"github.com/0xGurg/himay/internal/store"
	"github.com/0xGurg/himay/internal/trace"
)

func ns(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

func TestModelRefShapes(t *testing.T) {
	cases := map[string]string{
		`{"id":"glm-5.2","providerID":"opencode-go","variant":"max"}`: "opencode-go/glm-5.2:max",
		`{"modelID":"kimi-k2.6","providerID":"opencode-go"}`:          "opencode-go/kimi-k2.6",
		`{"id":"x","variant":"default"}`:                              "x",
		`not json`:                                                    "—",
		``:                                                            "—",
	}
	for in, want := range cases {
		if got := decode.ModelRef(json.RawMessage(in)).String(); got != want {
			t.Errorf("decode.ModelRef(%s) = %q, want %q", in, got, want)
		}
	}
}

func TestTokensMissingIsUnknown(t *testing.T) {
	if decode.Tokens(nil).Known || decode.Tokens(json.RawMessage(`{}`)).Known || decode.Tokens(json.RawMessage(`"x"`)).Known {
		t.Error("missing tokens must be unknown, not zero")
	}
	tk := decode.Tokens(json.RawMessage(`{"input":1,"output":2,"reasoning":3,"cache":{"read":4,"write":5}}`))
	if !tk.Known || tk.Total() != 15 {
		t.Errorf("tokens = %+v", tk)
	}
}

func TestStepErrorShapes(t *testing.T) {
	if e := decode.StepError(json.RawMessage(`{"type":"aborted","message":"Aborted"}`)); e == nil || e.Type != "aborted" {
		t.Errorf("v2 error = %+v", e)
	}
	if e := decode.StepError(json.RawMessage(`{"name":"APIError","data":{"message":"boom"}}`)); e == nil || e.Message != "boom" {
		t.Errorf("legacy error = %+v", e)
	}
	if e := decode.StepError(json.RawMessage(`"oops"`)); e == nil || e.Message == "" {
		t.Errorf("string error lost: %+v", e)
	}
	if decode.StepError(json.RawMessage(`{}`)) != nil {
		t.Error("empty object should be no error")
	}
}

func TestToolDetail(t *testing.T) {
	d := decode.ToolDetail(`{"type":"tool","state":{"status":"completed","input":{"a":1},"content":[{"type":"text","text":"out"},{"type":"file","mime":"image/png"}],"metadata":{"files":[{"diff":"-a\n+b"}],"truncated":true}}}`)
	if d.Output != "out\n[file image/png attachment]" || d.Diff != "-a\n+b" || !d.Truncated {
		t.Errorf("detail = %+v", d)
	}
	if decode.ToolDetail(`garbage`).Output != "garbage" {
		t.Error("malformed detail should surface raw text")
	}
}

func TestInputPreview(t *testing.T) {
	prev := func(tool, in string) string {
		return decode.Item(store.RawItem{Name: ns(tool), Input: ns(in)}).InputPreview
	}
	if p := prev("bash", `{"command":"ls -la","description":"list"}`); p != "ls -la" {
		t.Errorf("bash preview = %q", p)
	}
	if p := prev("task", `{"description":"explore","prompt":"long"}`); p != "explore" {
		t.Errorf("task preview = %q", p)
	}
	if p := prev("x", `{"filePath":"/a/b`); p == "" {
		t.Error("truncated JSON should still preview")
	}
}

// FuzzMessage feeds arbitrary JSON fragments through decoding and trace
// building; it must never panic.
func FuzzMessage(f *testing.F) {
	f.Add("assistant", `{"agent":"a"}`, `{"id":"m"}`, `{"input":1}`, `{"type":"x"}`, `"prev"`, `{"created":1}`)
	f.Add("model-switched", `null`, `[]`, `{}`, `{"attempt":"x"}`, `{"id":1}`, `[1,2]`)
	f.Add("future", `{{`, `"`, `-1`, `{"message":{}}`, `{"location":3}`, `{"created":"x"}`)
	f.Fuzz(func(t *testing.T, typ, a, b, c, d, e, g string) {
		r := store.RawMessage{ID: "m", Type: typ, Valid: true,
			Agent: ns(a), Model: ns(b), Tokens: ns(c), Error: ns(d), Previous: ns(e), Time: ns(g),
			Retry: ns(d), Metadata: ns(a), Files: ns(c), Agents: ns(b), Skills: ns(e), Keys: ns(g),
			ModelSwitchTarget: ns(b), TextPreview: ns(a)}
		m := decode.Message(r)
		it := decode.Item(store.RawItem{MsgID: "m", Type: ns(typ), Name: ns(a), Input: ns(b), Error: ns(d),
			Time: ns(g), ChildModel: ns(c), Loaded: ns(e), Files: ns(a)})
		m.Items = append(m.Items, it)
		_ = trace.Build([]model.Message{m, m})
		_ = decode.ToolDetail(a + b)
		_, _ = decode.ItemText(c)
	})
}

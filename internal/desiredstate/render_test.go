package desiredstate

import (
	"encoding/json"
	"testing"

	"github.com/shelwinnn/agent-fleet/internal/domain"
)

func testMachine(spec string) *domain.Machine {
	return &domain.Machine{
		Metadata: domain.ObjectMeta{Name: "ws-1"},
		Spec:     json.RawMessage(spec),
	}
}

func testProfile(spec string) *domain.AgentProfile {
	return &domain.AgentProfile{
		Metadata: domain.ObjectMeta{Name: "default"},
		Spec:     json.RawMessage(spec),
	}
}

// 确定性摘要（FR-7.2/RD-4）：同输入必同摘要；语义等价的键序差异不改变摘要。
func TestSnapshotDigestDeterministic(t *testing.T) {
	state1 := domain.DesiredState{
		SchemaVersion: "fixture/v1",
		Agents: map[string]domain.AgentDesired{
			"fixture": {Version: "1.2.3", Config: json.RawMessage(`{"model":"m1","provider":{"endpoint":"http://e","apiKeyEnv":"OPENAI_KEY"}}`)},
		},
	}
	state2 := domain.DesiredState{
		SchemaVersion: "fixture/v1",
		Agents: map[string]domain.AgentDesired{
			"fixture": {Version: "1.2.3", Config: json.RawMessage(`{"provider":{"apiKeyEnv":"OPENAI_KEY","endpoint":"http://e"},"model":"m1"}`)},
		},
	}
	d1, err := SnapshotDigest(state1)
	if err != nil {
		t.Fatal(err)
	}
	d2, err := SnapshotDigest(state2)
	if err != nil {
		t.Fatal(err)
	}
	if d1 != d2 {
		t.Fatalf("digest mismatch for semantically equal states:\n%s\n%s", d1, d2)
	}
	if len(d1) != len("sha256:")+64 {
		t.Fatalf("digest format unexpected: %s", d1)
	}
	// 内容变化 → 摘要变化（generation 递增的基石）。
	state3 := state1
	state3.Agents["fixture"] = domain.AgentDesired{Version: "1.2.4", Config: state1.Agents["fixture"].Config}
	d3, _ := SnapshotDigest(state3)
	if d3 == d1 {
		t.Fatal("digest unchanged despite version change")
	}
}

// 渲染：provider 解析只携带 endpoint/apiKeyEnv 名（FR-3.3），overrides 深合并。
func TestRenderResolvesProvidersAndOverrides(t *testing.T) {
	machine := testMachine(`{"profileRef":"default","overrides":{"agents":{"fixture":{"config":{"model":"m2"}}}}}`)
	profile := testProfile(`{
		"agents": {"fixture": {"enabled": true, "version": "1.2.3", "provider": "openai-main",
		                        "config": {"model": "m1", "extra": {"a": 1, "b": 2}}}}
	}`)
	provider := &domain.ModelProvider{
		Metadata: domain.ObjectMeta{Name: "openai-main"},
		Spec:     json.RawMessage(`{"type":"openai-compatible","endpoint":"http://ep","apiKeyEnv":"OPENAI_KEY"}`),
	}
	state, err := Render(RenderInputs{
		Machine: machine, Profile: profile,
		Providers:     map[string]*domain.ModelProvider{"openai-main": provider},
		Skills:        map[string]*domain.Skill{},
		SchemaVersion: "fixture/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	ag := state.Agents["fixture"]
	if ag.Version != "1.2.3" {
		t.Fatalf("version = %q", ag.Version)
	}
	var cfg map[string]any
	if err := json.Unmarshal(ag.Config, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["model"] != "m2" {
		t.Fatalf("override not applied: %v", cfg["model"])
	}
	prov, ok := cfg["provider"].(map[string]any)
	if !ok || prov["endpoint"] != "http://ep" || prov["apiKeyEnv"] != "OPENAI_KEY" {
		t.Fatalf("provider not resolved into config: %v", cfg["provider"])
	}
	// 深合并保留未覆盖键。
	if extra, ok := cfg["extra"].(map[string]any); !ok || extra["a"] != float64(1) || extra["b"] != float64(2) {
		t.Fatalf("deep merge lost sibling keys: %v", cfg["extra"])
	}
}

// 浮动版本禁止进入快照（FR-2.1）；未解析 Skill / 未知 Provider 拒绝渲染（§4.8）。
func TestRenderRejectsFloatingReferences(t *testing.T) {
	cases := []struct {
		name    string
		machine string
		profile string
	}{
		{"latest version", `{"profileRef":"default"}`,
			`{"agents":{"fixture":{"enabled":true,"version":"latest"}}}`},
		{"unknown provider", `{"profileRef":"default"}`,
			`{"agents":{"fixture":{"enabled":true,"version":"1.0","provider":"nope"}}}`},
		{"unresolved skill", `{"profileRef":"default"}`,
			`{"skills":[{"name":"s","skill":"skill-src"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Render(RenderInputs{
				Machine: testMachine(tc.machine), Profile: testProfile(tc.profile),
				Skills: map[string]*domain.Skill{}, Providers: map[string]*domain.ModelProvider{},
				SchemaVersion: "fixture/v1",
			})
			if err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

// 已解析 Skill 进入快照（FR-6.2 精确修订/digest）。
func TestRenderResolvedSkills(t *testing.T) {
	skill := &domain.Skill{
		Metadata: domain.ObjectMeta{Name: "skill-src"},
		Status:   json.RawMessage(`{"resolvedRevision":"abc123","contentDigest":"sha256:aa"}`),
	}
	state, err := Render(RenderInputs{
		Machine:       testMachine(`{"profileRef":"default"}`),
		Profile:       testProfile(`{"skills":[{"name":"superpowers","skill":"skill-src"}]}`),
		Skills:        map[string]*domain.Skill{"skill-src": skill},
		Providers:     map[string]*domain.ModelProvider{},
		SchemaVersion: "fixture/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	sd := state.Skills["superpowers"]
	if sd.Revision != "abc123" || sd.ContentDigest != "sha256:aa" {
		t.Fatalf("skill snapshot wrong: %+v", sd)
	}
}

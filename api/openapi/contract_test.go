// Package openapi 持有 REST 契约 fleet-v1.yaml 的一致性测试（KM-47）：
// 契约不是注释，而是被测试锁定的对齐面——枚举取值与原因码必须与
// internal/domain 的生命周期模型常量一致，T5（前端）与 T4（执行引擎）
// 以此为准。
package openapi

import (
	"os"
	"strings"
	"testing"

	"github.com/shelwinnn/agent-fleet/internal/domain"
	"gopkg.in/yaml.v3"
)

type openAPIDoc struct {
	Paths map[string]any `yaml:"paths"`
	Components struct {
		Schemas map[string]map[string]any `yaml:"schemas"`
	} `yaml:"components"`
}

func loadContract(t *testing.T) openAPIDoc {
	t.Helper()
	b, err := os.ReadFile("fleet-v1.yaml")
	if err != nil {
		t.Fatalf("read fleet-v1.yaml: %v", err)
	}
	var doc openAPIDoc
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("parse fleet-v1.yaml: %v", err)
	}
	return doc
}

func TestLifecycleContractPathsRegistered(t *testing.T) {
	doc := loadContract(t)
	for _, path := range []string{
		"/api/v1/machines/{name}/lifecycle/desired",
		"/api/v1/lifecycle/operations",
		"/api/v1/lifecycle/operations/{opId}",
		"/api/v1/lifecycle/operations/{opId}/retry",
		"/api/v1/lifecycle/capabilities",
	} {
		if _, ok := doc.Paths[path]; !ok {
			t.Errorf("lifecycle path %s missing from contract", path)
		}
	}
}

// enumOf 取 schema 的 enum 字段。
func enumOf(t *testing.T, doc openAPIDoc, schema string) []string {
	t.Helper()
	raw, ok := doc.Components.Schemas[schema]["enum"]
	if !ok {
		t.Fatalf("schema %s has no enum", schema)
	}
	items, ok := raw.([]any)
	if !ok {
		t.Fatalf("schema %s enum is not a list", schema)
	}
	out := make([]string, 0, len(items))
	for _, v := range items {
		out = append(out, v.(string))
	}
	return out
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		seen[s]--
		if seen[s] < 0 {
			return false
		}
	}
	return true
}

// TestLifecycleContractMatchesDomain 锁定契约与域模型的枚举一致（防止单侧漂移）。
func TestLifecycleContractMatchesDomain(t *testing.T) {
	doc := loadContract(t)

	if got, want := enumOf(t, doc, "LifecycleIntent"),
		[]string{domain.LifecycleIntentPresent, domain.LifecycleIntentAbsent, domain.LifecycleIntentUnmanaged}; !sameSet(got, want) {
		t.Errorf("LifecycleIntent enum %v != domain %v", got, want)
	}
	if got, want := enumOf(t, doc, "LifecycleTargetState"),
		[]string{domain.TargetPending, domain.TargetRunning, domain.TargetSucceeded,
			domain.TargetFailed, domain.TargetNotExecuted, domain.TargetUnknown}; !sameSet(got, want) {
		t.Errorf("LifecycleTargetState enum %v != domain %v", got, want)
	}
	if got, want := enumOf(t, doc, "LifecycleOperationType"),
		[]string{domain.LifecycleOpInstall, domain.LifecycleOpUpgrade,
			domain.LifecycleOpUninstall, domain.LifecycleOpCheckUpdate}; !sameSet(got, want) {
		t.Errorf("LifecycleOperationType enum %v != domain %v", got, want)
	}
	// LifecycleCapability 的能力状态是嵌套属性 state 的 enum。
	capState, ok := doc.Components.Schemas["LifecycleCapability"]["properties"].(map[string]any)["state"].(map[string]any)["enum"].([]any)
	if !ok {
		t.Fatal("LifecycleCapability.properties.state enum missing")
	}
	gotStates := make([]string, 0, len(capState))
	for _, v := range capState {
		gotStates = append(gotStates, v.(string))
	}
	if want := []string{domain.CapabilitySupported, domain.CapabilityUnverified, domain.CapabilityBlocked}; !sameSet(gotStates, want) {
		t.Errorf("LifecycleCapability state enum %v != domain %v", gotStates, want)
	}
}

// TestLifecycleReasonsDocumented 保证结果态原因码在契约中有记载（四态各带原因）。
func TestLifecycleReasonsDocumented(t *testing.T) {
	doc := loadContract(t)
	schema, ok := doc.Components.Schemas["LifecycleTargetResult"]
	if !ok {
		t.Fatal("LifecycleTargetResult schema missing")
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("LifecycleTargetResult has no properties")
	}
	reasonSchema, ok := props["reason"].(map[string]any)
	if !ok {
		t.Fatal("LifecycleTargetResult.reason property missing")
	}
	desc, _ := reasonSchema["description"].(string)
	for _, reason := range []string{
		domain.ReasonVerifiedPresent, domain.ReasonVerifiedAbsent,
		domain.ReasonVersionMismatch, domain.ReasonDiscoveryFailed, domain.ReasonOpFailed, domain.ReasonUnsupportedCombo,
		domain.ReasonTargetOffline, domain.ReasonDispatchFailed,
		domain.ReasonResultLost, domain.ReasonNodeDisconnected, domain.ReasonTimeoutNoResult,
	} {
		if !strings.Contains(desc, reason) {
			t.Errorf("reason %q not documented in LifecycleTargetResult.reason", reason)
		}
	}
}

// TestLifecycleRefsResolve 扫描全文 $ref：Lifecycle 相关引用必须可解析。
func TestLifecycleRefsResolve(t *testing.T) {
	doc := loadContract(t)
	b, err := os.ReadFile("fleet-v1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		idx := strings.Index(line, `"#/components/schemas/`)
		if idx < 0 {
			continue
		}
		rest := line[idx+len(`"#/components/schemas/`):]
		end := strings.Index(rest, `"`)
		if end < 0 {
			continue
		}
		name := rest[:end]
		if !strings.HasPrefix(name, "Lifecycle") {
			continue
		}
		if _, ok := doc.Components.Schemas[name]; !ok {
			t.Errorf("dangling ref to components/schemas/%s", name)
		}
	}
}

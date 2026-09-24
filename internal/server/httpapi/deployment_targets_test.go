package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/shelwinnn/agent-fleet/internal/domain"
	"github.com/shelwinnn/agent-fleet/internal/store/sqlite"
)

// TestDeploymentResourceCarriesTargets 断言 §6.1 的 status.targets 真的出现在
// REST 响应里（FR-14.5 第 3 组的唯一数据来源；KM-25 契约缺口 #2）。
func TestDeploymentResourceCarriesTargets(t *testing.T) {
	f := newFixture(t, Config{})
	targets := sqlite.NewDeploymentTargetStore(f.db)

	code, body := f.do(t, http.MethodPost, "/api/v1/deployments",
		`{"metadata":{"name":"rollout-1"},"spec":{"machineNames":["ws-1"],"targetGeneration":1}}`, nil)
	if code != http.StatusCreated {
		t.Fatalf("create deployment = %d: %v", code, body)
	}
	// 资源创建本身不写目标行（目标由 Deployment 控制器维护）：
	// 此时 targets 省略或为空数组都合法（DeploymentStatus.Targets 带 omitempty）。
	status, _ := body["status"].(map[string]any)
	if items, ok := status["targets"].([]any); ok && len(items) != 0 {
		t.Fatalf("新建发布的 targets = %v, want 空", items)
	}

	ctx := context.Background()
	if err := targets.Replace(ctx, "rollout-1", []domain.DeploymentTargetStatus{
		{Machine: "ws-1", Phase: domain.TargetPhaseSuperseded, Reason: domain.SupersededByNewerGeneration},
		{Machine: "ws-2", Phase: domain.TargetPhaseSkipped, Reason: "OperatorSkipped"},
		{Machine: "ws-3", Phase: domain.TargetPhaseFailed, Reason: "VerifyFailed"},
	}); err != nil {
		t.Fatalf("replace targets: %v", err)
	}

	code, one := f.do(t, http.MethodGet, "/api/v1/deployments/rollout-1", "", nil)
	if code != http.StatusOK {
		t.Fatalf("get deployment = %d: %v", code, one)
	}
	got := targetsOf(t, one)
	if len(got) != 3 {
		t.Fatalf("targets = %v, want 3 条", got)
	}
	phases := map[string]string{}
	reasons := map[string]string{}
	for _, item := range got {
		m := item.(map[string]any)
		phases[m["machine"].(string)] = m["phase"].(string)
		if reason, ok := m["reason"].(string); ok {
			reasons[m["machine"].(string)] = reason
		}
	}
	// Superseded/Skipped 与 Failed 必须各自带原因、可区分（FR-14.5）。
	if phases["ws-1"] != domain.TargetPhaseSuperseded || reasons["ws-1"] != domain.SupersededByNewerGeneration {
		t.Fatalf("ws-1 = %v/%v, want Superseded + 原因", phases["ws-1"], reasons["ws-1"])
	}
	if phases["ws-2"] != domain.TargetPhaseSkipped || reasons["ws-2"] != "OperatorSkipped" {
		t.Fatalf("ws-2 = %v/%v, want Skipped + 原因", phases["ws-2"], reasons["ws-2"])
	}
	if phases["ws-3"] != domain.TargetPhaseFailed {
		t.Fatalf("ws-3 = %v, want Failed", phases["ws-3"])
	}

	// 列表路径同样补齐（Overview 的"最近发布"表按列表渲染异常态）。
	code, list := f.do(t, http.MethodGet, "/api/v1/deployments", "", nil)
	if code != http.StatusOK {
		t.Fatalf("list deployments = %d: %v", code, list)
	}
	items, _ := list["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %v, want 1", list["items"])
	}
	if got := targetsOf(t, items[0].(map[string]any)); len(got) != 3 {
		t.Fatalf("列表里的 targets = %v, want 3 条", got)
	}
}

func targetsOf(t *testing.T, obj map[string]any) []any {
	t.Helper()
	status, ok := obj["status"].(map[string]any)
	if !ok {
		t.Fatalf("status 缺失：%v", obj)
	}
	items, ok := status["targets"].([]any)
	if !ok {
		t.Fatalf("status.targets 缺失或不是数组：%v", status)
	}
	return items
}

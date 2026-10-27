package lifecycle

import (
	"context"

	"github.com/kevinelliott/agentmanager/pkg/agent"
	"github.com/kevinelliott/agentmanager/pkg/catalog"
	"github.com/kevinelliott/agentmanager/pkg/detector"
	"github.com/kevinelliott/agentmanager/pkg/installer"
	"github.com/kevinelliott/agentmanager/pkg/platform"
)

// 本文件的探针只服务于 KM-46 的证据采集（单元测试与 cmd/agentmanager-verify），
// 用于在隔离环境里复现上游行为结论；不是生产路径，T4 不得引用。

// UpstreamUpdateDrift 用 catalog 原生的 method 定义（update_cmd =
// `npm update -g <pkg>`，无目标版本参数）直接驱动上游 Manager.Update，
// 返回独立核验后的实际版本。期望证据：无论本次意图固定什么版本，
// Update 都落到注册表 latest——这是引擎拒绝委托 Update 的运行证据。
// execs 是该 agent 的程序入口名（用于核验时的可执行文件定位）。
func UpstreamUpdateDrift(ctx context.Context, pkg string, execs []string) (Outcome, error) {
	plat := platform.Current()
	mgr := installer.NewManager(plat)
	ad := catalog.AgentDef{
		ID:   "evidence-update",
		Name: "evidence-update",
		InstallMethods: map[string]catalog.InstallMethodDef{
			"npm": {
				Method:  "npm",
				Package: pkg,
				Command: "npm install -g " + pkg,
			},
		},
		Detection: catalog.DetectionDef{Executables: execs},
	}
	inst := &agent.Installation{AgentID: ad.ID, Method: agent.MethodNPM}
	res, err := mgr.Update(ctx, inst, ad, ad.InstallMethods["npm"])
	if err != nil {
		return Outcome{}, err
	}
	// 用严格检测独立核验 Update 之后的实际版本（不采信上游 Result）。
	eng := NewNpmEngine(plat)
	detected, err := eng.Detect(ctx, Request{AgentID: ad.ID, Package: pkg, Executables: execs})
	if err != nil {
		return Outcome{}, err
	}
	out := Outcome{
		Op:             OpUpdate,
		Package:        pkg,
		ExecutablePath: res.ExecutablePath,
		UpstreamOutput: res.Output,
	}
	if detected.Installed {
		out.VerifiedVersion = detected.Version
	}
	return out, nil
}

// DetectAllInventory 直接返回上游 DetectAll 的结果（含被吞掉的策略错误）。
// 期望证据：npm 彻底失败时返回 (空, nil)——空 inventory + 无错误，调用方
// 若不作甄别会把“探测失败”误读为“未安装”。
func DetectAllInventory(ctx context.Context, req Request) ([]*agent.Installation, error) {
	det := detector.New(platform.Current())
	return det.DetectAll(ctx, []catalog.AgentDef{npmAgentDef(req, false)})
}

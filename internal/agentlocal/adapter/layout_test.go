// Package adapter_test 承载仓库级布局护栏测试（架构 v1.1.2 §3.6 依赖方向约束：
// "编译期可验证"）。本片新增家族适配器后，护栏必须有可执行的断言，否则
// 一次不小心的 import 就会让控制面依赖家族实现（护栏 #2 / I-1）。
package adapter_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// importsOf 返回某目录下全部 .go 文件（含测试）的 import 路径集合。
func importsOf(t *testing.T, root string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, perr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if perr != nil {
			return perr
		}
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if strings.HasPrefix(p, "github.com/shelwinnn/agent-fleet/") {
				out[path] = append(out[path], p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestDependencyGuardrails 断言 §3.6 的四条依赖方向约束。
func TestDependencyGuardrails(t *testing.T) {
	internal := filepath.Join("..", "..", "..", "internal")
	if _, err := os.Stat(internal); err != nil {
		t.Fatalf("internal dir not found from %s: %v", internal, err)
	}

	checks := []struct {
		name    string
		dir     string
		forbid  []string
		message string
	}{
		{
			name:    "control-plane-must-not-import-agentlocal",
			dir:     filepath.Join(internal, "controller"),
			forbid:  []string{"internal/agentlocal"},
			message: "controller/* 不得 import agentlocal/*（护栏 #2：控制面不依赖节点侧实现）",
		},
		{
			name:    "server-must-not-import-agentlocal",
			dir:     filepath.Join(internal, "server"),
			forbid:  []string{"internal/agentlocal"},
			message: "server/* 不得 import agentlocal/*（护栏 #2）",
		},
		{
			name:    "server-must-not-import-family-adapters",
			dir:     filepath.Join(internal, "server"),
			forbid:  []string{"internal/agentlocal/adapter/"},
			message: "server/* 不得 import 任何家族实现包（§3.6）",
		},
		{
			name:    "agentlocal-must-not-import-control-plane",
			dir:     filepath.Join(internal, "agentlocal"),
			forbid:  []string{"internal/controller/", "internal/server/httpapi", "internal/server/grpcagent"},
			message: "agentlocal/* 不得 import controller/* 或 server/*（§3.6）",
		},
		{
			name:    "domain-must-not-import-runtime-packages",
			dir:     filepath.Join(internal, "domain"),
			forbid:  []string{"internal/agentlocal", "internal/controller", "internal/server", "internal/store"},
			message: "domain 不 import 任何运行时包（§3.6）",
		},
	}
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			for file, imports := range importsOf(t, c.dir) {
				for _, imp := range imports {
					for _, bad := range c.forbid {
						if strings.Contains(imp, bad) {
							t.Errorf("%s imports %s: %s", file, imp, c.message)
						}
					}
				}
			}
		})
	}
}

// TestControlPlaneBinariesDoNotImportFamilyAdapters：控制面二进制（server）
// 同样不得 import 家族实现包或节点侧包（§3.6 护栏 #2 的二进制级表达）。
func TestControlPlaneBinariesDoNotImportFamilyAdapters(t *testing.T) {
	root := filepath.Join("..", "..", "..", "cmd", "agent-fleet-server")
	if _, err := os.Stat(root); err != nil {
		t.Skipf("control-plane binary not present: %v", err)
	}
	for file, imports := range importsOf(t, root) {
		for _, imp := range imports {
			if strings.Contains(imp, "internal/agentlocal") {
				t.Errorf("%s imports %s: 控制面二进制不得链接节点侧/家族适配器实现", file, imp)
			}
		}
	}
}

// TestFamilyAdaptersAreMutuallyIndependent：适配器之间互不 import
// （§3.6：新适配器 = 新增 adapter/<family> 包 + 注册）。
func TestFamilyAdaptersAreMutuallyIndependent(t *testing.T) {
	base := filepath.Join("..", "..", "..", "internal", "agentlocal", "adapter")
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	// fixture 是测试用语义适配器，kit 是共享机制包（非家族实现）。
	shared := map[string]bool{"fixture": true, "kit": true}
	var families []string
	for _, e := range entries {
		if e.IsDir() && !shared[e.Name()] {
			families = append(families, e.Name())
		}
	}
	if len(families) == 0 {
		t.Fatal("no family adapter packages found")
	}
	for _, family := range families {
		for file, imports := range importsOf(t, filepath.Join(base, family)) {
			for _, imp := range imports {
				for _, other := range families {
					if other == family {
						continue
					}
					if strings.HasSuffix(imp, "/adapter/"+other) {
						t.Errorf("%s imports sibling family adapter %s: 家族实现之间必须互不依赖", file, imp)
					}
				}
			}
		}
	}
}

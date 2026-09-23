package fixture

import (
	"encoding/json"
	"fmt"
	"path/filepath"
)

// ManagedFiles 声明 fixture 适配器写入内容的文件（相对 home；§5.3 备份契约）。
func (a *Adapter) ManagedFiles(home string) ([]string, error) {
	return []string{
		filepath.Join(ID, "config.json"),
		filepath.Join(ID, "version"),
	}, nil
}

// ExtractManaged 从文件内容提取受管键集与值（§5.3 契约 1：备份粒度记录）。
func (a *Adapter) ExtractManaged(content []byte) (map[string]any, error) {
	cfg := map[string]any{}
	if len(content) == 0 {
		return map[string]any{}, nil
	}
	if err := json.Unmarshal(content, &cfg); err != nil {
		// 非 JSON（如 version 文本文件）：无受管键。
		return map[string]any{}, nil
	}
	return projectionOf(cfg), nil
}

// MergeManaged 落实受管字段级回退（§5.3 契约 3：存在外部编辑时不得整文件
// 还原）——只把受管键写回备份值，保留全部未托管键的当前值；写后重新解析验证。
func (a *Adapter) MergeManaged(home, relPath string, managed map[string]any) error {
	if relPath != filepath.Join(ID, "config.json") {
		return fmt.Errorf("fixture: %s is not a managed-key-restorable file", relPath)
	}
	// 受管字段级回退要求当前文件可解析；不可解析即显式失败（RestoreConflict）。
	if _, err := a.readConfig(home); err != nil {
		return fmt.Errorf("current config unparseable: %w", err)
	}
	return a.mergeWriteConfig(home, managed)
}

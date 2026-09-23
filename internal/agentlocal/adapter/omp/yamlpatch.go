package omp

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// yamlpatch 用 yaml.v3 的 Node 树写受管键：Node 保留 HeadComment/LineComment/
// FootComment，因此未托管内容与注释在合并写后仍在（§16.1）。整树重新编码会
// 规整缩进与键序，但语义与注释不丢失。

// parseYAML 解析 YAML 文档（空文档 → 空映射节点）。
func parseYAML(doc string) (*yaml.Node, error) {
	if strings.TrimSpace(doc) == "" {
		return &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}, nil
	}
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(doc), &root); err != nil {
		return nil, err
	}
	if root.Kind == 0 { // 空文档（只有注释）
		root = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
	}
	if len(root.Content) == 0 {
		root.Content = []*yaml.Node{{Kind: yaml.MappingNode}}
	}
	return &root, nil
}

// mappingRoot 返回文档的根映射节点。
func mappingRoot(root *yaml.Node) (*yaml.Node, error) {
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return nil, fmt.Errorf("yaml: document root is not a mapping")
	}
	node := root.Content[0]
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("yaml: document root is not a mapping")
	}
	return node, nil
}

// lookupPath 按点分路径读取标量值（不存在返回 nil）。
func lookupPath(root *yaml.Node, path string) any {
	m, err := mappingRoot(root)
	if err != nil {
		return nil
	}
	cur := m
	for _, key := range strings.Split(path, ".") {
		next := mapValue(cur, key)
		if next == nil {
			return nil
		}
		cur = next
	}
	var out any
	if err := cur.Decode(&out); err != nil {
		return nil
	}
	return out
}

func mapValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

// setPath 写入点分路径的标量值，缺失的中间映射就地创建。
func setPath(root *yaml.Node, path string, value any) error {
	m, err := mappingRoot(root)
	if err != nil {
		return err
	}
	keys := strings.Split(path, ".")
	cur := m
	for _, key := range keys[:len(keys)-1] {
		next := mapValue(cur, key)
		if next == nil {
			keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
			valNode := &yaml.Node{Kind: yaml.MappingNode}
			cur.Content = append(cur.Content, keyNode, valNode)
			cur = valNode
			continue
		}
		if next.Kind != yaml.MappingNode {
			return fmt.Errorf("yaml: %q is not a mapping, refusing to overwrite", key)
		}
		cur = next
	}
	leaf := keys[len(keys)-1]
	encoded := &yaml.Node{}
	if err := encoded.Encode(value); err != nil {
		return fmt.Errorf("yaml: encode %s: %w", path, err)
	}
	if existing := mapValue(cur, leaf); existing != nil {
		// 只替换值节点，保留该键上的注释。
		existing.Kind = encoded.Kind
		existing.Tag = encoded.Tag
		existing.Value = encoded.Value
		existing.Style = encoded.Style
		return nil
	}
	cur.Content = append(cur.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: leaf}, encoded)
	return nil
}

// patchYAML 写入多个受管键路径并返回新文档。
func patchYAML(doc string, sets map[string]any) (string, error) {
	root, err := parseYAML(doc)
	if err != nil {
		return "", err
	}
	for _, path := range sortedKeys(sets) {
		if err := setPath(root, path, sets[path]); err != nil {
			return "", err
		}
	}
	out, err := encodeYAML(root)
	if err != nil {
		return "", err
	}
	return out, nil
}

func encodeYAML(root *yaml.Node) (string, error) {
	var sb strings.Builder
	enc := yaml.NewEncoder(&sb)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return sb.String(), nil
}

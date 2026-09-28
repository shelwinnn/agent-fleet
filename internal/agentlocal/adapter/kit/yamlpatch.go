package kit

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// YAML Node 树合并（批次二片 C 从 omp 下沉到 kit，避免家族间复制第二份实现）：
// 用 yaml.v3 的 Node 写受管键，Node 保留 HeadComment/LineComment/FootComment，
// 因此未托管内容与注释在合并写后仍在（架构 v1.1.2 §16.1）。整树重新编码会规整
// 缩进与空行，但键序与注释不丢失。
//
// 调用方只按叶子键路径读写（如 model.default、mcp_servers.<name>.command），
// 绝不整树 Decode：同文档内的密钥面（delegation.api_key、auxiliary.*.api_key、
// secrets.bitwarden.*、dashboard.basic_auth.*、HTTP_PROXY/HTTPS_PROXY）因此不会被
// 读入比较或写入期望状态（护栏：密钥面逐字节保留）。

// ParseYAML 解析 YAML 文档（空文档 → 空映射节点）。
func ParseYAML(doc string) (*yaml.Node, error) {
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
	if root == nil || root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return nil, fmt.Errorf("yaml: document root is not a mapping")
	}
	node := root.Content[0]
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("yaml: document root is not a mapping")
	}
	return node, nil
}

// LookupYAML 按点分路径读取值（不存在返回 nil）。只解码该子树，不触碰兄弟键。
func LookupYAML(root *yaml.Node, path string) any {
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

// SetYAML 写入点分路径的值，缺失的中间映射就地创建（叶子可为标量/映射/序列）。
func SetYAML(root *yaml.Node, path string, value any) error {
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
		// 整节点替换（含 Content）：只改 Kind/Tag/Value/Style 会把旧子节点
		// 原样编码回去——写映射/序列时表现为"改不动"（换 endpoint/model 永不收敛）。
		// 键节点上的注释（Head/Line/Foot）仍保留。
		head, line, foot := existing.HeadComment, existing.LineComment, existing.FootComment
		*existing = *encoded
		existing.HeadComment, existing.LineComment, existing.FootComment = head, line, foot
		return nil
	}
	cur.Content = append(cur.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: leaf}, encoded)
	return nil
}

// PatchYAML 写入多个受管键路径并返回新文档（确定性：按键排序）。
func PatchYAML(doc string, sets map[string]any) (string, error) {
	root, err := ParseYAML(doc)
	if err != nil {
		return "", err
	}
	for _, path := range sortedKeys(sets) {
		if err := SetYAML(root, path, sets[path]); err != nil {
			return "", err
		}
	}
	out, err := EncodeYAML(root)
	if err != nil {
		return "", err
	}
	return out, nil
}

// EncodeYAML 把 Node 树编码回 YAML 文本（缩进 2）。
func EncodeYAML(root *yaml.Node) (string, error) {
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

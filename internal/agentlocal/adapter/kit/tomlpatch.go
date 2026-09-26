package kit

// tomlpatch 以**行级手术**方式写入受管键与受管表：受管键之外的内容（含注释、
// 空行、键序）逐字节保留。§16.1 要求"解析 → 只补写适配器自有键 → 保留未知键"；
// 用整文件重新序列化会丢掉用户注释，故此处按表边界就地替换。
//
// 【批次二下沉】本文件原为 codex 家族私有（批次一，commit 后经复核轮修复）。
// 批次二 Grok 同样需要 TOML 行级手术，按批次一复核轮"共享机制下沉 kit"的先例
// 下沉到这里（不写第二/第三份实现）：codex 与 grok 共用同一实现。
//
// 新增两种粒度（批次二 §6 缺口 1 提到的"表内键级"）：
//   - Tables 整表替换：Fleet 独占该表（如 codex 的 [model_providers.fleet]）；
//   - Keys   表内**指定键**写入：表里还有未托管键时必须保留（如 Grok 的
//     [models] 里除 default 外还有 allowed_models / extra_headers）。
//
// 安全网：调用方在 patch 后必须重新解析；解析失败即中止写入（原子写 + 校验），
// 因此最坏结果是显式失败，而不是写出损坏配置。

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// tableHeader 匹配 `[a.b]` 或 `[[a.b]]` 形式的表头（允许行尾注释）。
var tableHeader = regexp.MustCompile(`^\s*(\[\[?)([^\[\]]+)(\]\]?)\s*(#.*)?$`)

// ParseTOML 解析 TOML 文档为 map（空文档 → 空 map）。
func ParseTOML(doc string) (map[string]any, error) {
	out := map[string]any{}
	if strings.TrimSpace(doc) == "" {
		return out, nil
	}
	if err := toml.Unmarshal([]byte(doc), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// TOMLPatch 描述一次行级手术。四类操作按 Remove → Scalars → Keys → Tables 的
// 顺序应用（先删除过期表，再写根区标量、既有表内的受管键，最后整表写入/新建；
// Keys 先于 Tables 保证父表先于其子表出现，避免写出"父表在子表之后"的非法序）。
type TOMLPatch struct {
	// Scalars 只改根区（第一个表头之前）的同名键。
	Scalars map[string]any
	// Tables 整表替换/新建（表头 + 表体全部由适配器决定）。
	Tables map[string]map[string]any
	// Keys 写既有表内的指定键，保留该表里其它（未托管）键；表不存在则新建。
	Keys map[string]map[string]any
	// Remove 删除这些表的表头与其表体（直到下一个表头）。用于清理不再受管的
	// 子表（如 Grok 去掉 envRefs 后的 [mcp_servers.<name>.env]）。按表名精确匹配。
	Remove []string
}

// PatchTOML 应用行级手术，返回新文档；未受管内容逐字节保留。
func PatchTOML(doc string, patch TOMLPatch) (string, error) {
	if len(patch.Scalars) == 0 && len(patch.Tables) == 0 && len(patch.Keys) == 0 && len(patch.Remove) == 0 {
		return doc, nil
	}
	if _, err := ParseTOML(doc); err != nil {
		return "", fmt.Errorf("existing document does not parse: %w", err)
	}
	lines := scanLines(doc)

	// 0) 删除过期表（表头 + 表体，直到下一个表头）。
	if len(patch.Remove) != 0 {
		remove := map[string]bool{}
		for _, name := range patch.Remove {
			remove[name] = true
		}
		kept := make([]tomlLine, 0, len(lines))
		skipping := false
		for _, l := range lines {
			if l.header != "" {
				skipping = remove[l.header]
			}
			if skipping {
				continue
			}
			kept = append(kept, l)
		}
		lines = kept
	}

	// 1) 根区标量。
	lines, err := patchRootScalars(lines, patch.Scalars)
	if err != nil {
		return "", err
	}

	// 2) 既有表内的受管键（保留表内其它键）。
	for _, name := range sortedKeys(patch.Keys) {
		lines, err = patchTableKeys(lines, name, patch.Keys[name])
		if err != nil {
			return "", err
		}
	}

	// 3) 整表替换/新建。
	for _, name := range sortedKeys(patch.Tables) {
		lines, err = patchWholeTable(lines, name, patch.Tables[name])
		if err != nil {
			return "", err
		}
	}
	return strings.Join(lineTexts(lines), "\n"), nil
}

// patchRootScalars 只改根区（第一个表头之前）的同名标量行；不存在则在根区末尾插入。
func patchRootScalars(lines []tomlLine, scalars map[string]any) ([]tomlLine, error) {
	if len(scalars) == 0 {
		return lines, nil
	}
	rootEnd := len(lines)
	for i, l := range lines {
		if l.header != "" {
			rootEnd = i
			break
		}
	}
	for _, key := range sortedKeys(scalars) {
		text, err := marshalTomlValue(key, scalars[key])
		if err != nil {
			return nil, fmt.Errorf("marshal %s: %w", key, err)
		}
		pattern := regexp.MustCompile(`^\s*` + regexp.QuoteMeta(key) + `\s*=`)
		replaced := false
		for i := 0; i < rootEnd; i++ {
			if pattern.MatchString(lines[i].text) {
				lines[i].text = text
				replaced = true
				break
			}
		}
		if !replaced {
			insert := append([]tomlLine{{text: text}}, lines[rootEnd:]...)
			lines = append(lines[:rootEnd], insert...)
			rootEnd++
		}
	}
	return lines, nil
}

// patchTableKeys 在既有表 name 内写入 keys 里的每个键：命中同名行即原位替换，
// 否则插入表体末尾；表不存在则新建 [name] 表。表内其它键与注释原样保留。
func patchTableKeys(lines []tomlLine, name string, keys map[string]any) ([]tomlLine, error) {
	if len(keys) == 0 {
		return lines, nil
	}
	for _, key := range sortedKeys(keys) {
		text, err := marshalTomlValue(key, keys[key])
		if err != nil {
			return nil, fmt.Errorf("marshal %s.%s: %w", name, key, err)
		}
		start := -1
		for i, l := range lines {
			if l.header == name && !l.arrayHeader {
				start = i
				break
			}
		}
		if start < 0 {
			out := strings.Join(lineTexts(lines), "\n")
			if !strings.HasSuffix(out, "\n") {
				out += "\n"
			}
			if strings.TrimSpace(out) != "" {
				out += "\n"
			}
			out += "[" + name + "]\n" + text + "\n"
			lines = scanLines(strings.TrimSuffix(out, "\n"))
			continue
		}
		end := len(lines)
		for i := start + 1; i < len(lines); i++ {
			if lines[i].header != "" {
				end = i
				break
			}
		}
		pattern := regexp.MustCompile(`^\s*` + regexp.QuoteMeta(key) + `\s*=`)
		replaced := false
		for i := start + 1; i < end; i++ {
			if pattern.MatchString(lines[i].text) {
				lines[i].text = text
				replaced = true
				break
			}
		}
		if !replaced {
			insert := append([]tomlLine{{text: text}}, lines[end:]...)
			lines = append(lines[:end], insert...)
		}
	}
	return lines, nil
}

// patchWholeTable 整表替换 name（表头 + 表体），表名不存在则在文末追加。
func patchWholeTable(lines []tomlLine, name string, table map[string]any) ([]tomlLine, error) {
	body, err := renderTableBody(table)
	if err != nil {
		return nil, err
	}
	start := -1
	for i, l := range lines {
		if l.header == name && !l.arrayHeader {
			start = i
			break
		}
	}
	if start < 0 {
		out := strings.Join(lineTexts(lines), "\n")
		if !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		if strings.TrimSpace(out) != "" {
			out += "\n"
		}
		out += "[" + name + "]\n" + body + "\n"
		return scanLines(strings.TrimSuffix(out, "\n")), nil
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if lines[i].header != "" {
			end = i
			break
		}
	}
	replacement := []tomlLine{{text: "[" + name + "]", header: name}}
	for _, b := range strings.Split(body, "\n") {
		replacement = append(replacement, tomlLine{text: b})
	}
	return append(lines[:start], append(replacement, lines[end:]...)...), nil
}

// marshalTomlValue 生成 `key = value` 文本（键序固定、单行；值必须是适配器
// 自己构造的简单类型）。不用编码器的原因：其字面量风格（单引号）可读性差且
// 与用户文件惯例不一致；此处只用 TOML 基本字符串与数组，转义规则受控。
func marshalTomlValue(key string, value any) (string, error) {
	text, err := tomlValue(value)
	if err != nil {
		return "", fmt.Errorf("%s: %w", key, err)
	}
	return key + " = " + text, nil
}

func tomlValue(value any) (string, error) {
	switch v := value.(type) {
	case string:
		return quoteTOMLString(v)
	case []string:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			q, err := quoteTOMLString(item)
			if err != nil {
				return "", err
			}
			parts = append(parts, q)
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	case bool:
		return strconv.FormatBool(v), nil
	case int:
		return strconv.Itoa(v), nil
	case int64:
		return strconv.FormatInt(v, 10), nil
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64), nil
	default:
		return "", fmt.Errorf("unsupported TOML value type %T", value)
	}
}

// quoteTOMLString 生成 TOML 基本字符串（双引号），拒绝控制字符。
func quoteTOMLString(s string) (string, error) {
	if strings.ContainsAny(s, "\n\r\x00") {
		return "", fmt.Errorf("value contains control characters and cannot be written as a basic string")
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`, nil
}

type tomlLine struct {
	text string
	// header 非空表示该行是表头（值为表名，如 "mcp_servers.serena"）。
	header string
	// arrayHeader 为真表示 `[[...]]`。
	arrayHeader bool
}

// scanLines 把文档切成行，标注每行的表头归属（跳过多行字符串内部）。
func scanLines(doc string) []tomlLine {
	raw := strings.Split(doc, "\n")
	out := make([]tomlLine, 0, len(raw))
	inMultiline := ""
	for _, line := range raw {
		l := tomlLine{text: line}
		trimmed := strings.TrimSpace(line)
		switch {
		case inMultiline != "":
			if strings.Contains(line, inMultiline) {
				inMultiline = ""
			}
		case strings.HasPrefix(trimmed, `"""`):
			if strings.Count(trimmed, `"""`) < 2 {
				inMultiline = `"""`
			}
		case strings.HasPrefix(trimmed, "'''"):
			if strings.Count(trimmed, "'''") < 2 {
				inMultiline = "'''"
			}
		default:
			if m := tableHeader.FindStringSubmatch(line); m != nil {
				l.header = strings.TrimSpace(m[2])
				l.arrayHeader = m[1] == "[["
			}
		}
		out = append(out, l)
	}
	return out
}

func renderTableBody(table map[string]any) (string, error) {
	keys := sortedKeys(table)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		text, err := marshalTomlValue(k, table[k])
		if err != nil {
			return "", fmt.Errorf("marshal %s: %w", k, err)
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, "\n"), nil
}

func lineTexts(lines []tomlLine) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, l.text)
	}
	return out
}

func sortedKeys[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

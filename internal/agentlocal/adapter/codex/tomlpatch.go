package codex

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// tomlpatch 以**行级手术**方式写入受管键与受管表：受管键之外的内容（含注释、
// 空行、键序）逐字节保留。§16.1 要求"解析 → 只补写适配器自有键 → 保留未知键"；
// 用整文件重新序列化会丢掉用户注释，故此处按表边界就地替换。
//
// 安全网：调用方在 patch 后必须重新解析；解析失败即中止写入（原子写 + 校验），
// 因此最坏结果是显式失败，而不是写出损坏配置。

// tableHeader 匹配 `[a.b]` 或 `[[a.b]]` 形式的表头（允许行尾注释）。
var tableHeader = regexp.MustCompile(`^\s*(\[\[?)([^\[\]]+)(\]\]?)\s*(#.*)?$`)

// parseTOML 解析 TOML 文档为 map（空文档 → 空 map）。
func parseTOML(doc string) (map[string]any, error) {
	out := map[string]any{}
	if strings.TrimSpace(doc) == "" {
		return out, nil
	}
	if err := toml.Unmarshal([]byte(doc), &out); err != nil {
		return nil, err
	}
	return out, nil
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

// patchTOML 写入顶层标量键与受管表，返回新文档。
//
//   - 标量键只改根区（第一个表头之前）的同名行；不存在则在根区末尾插入；
//   - 受管表整表替换（表头行 + 表体），表名不存在则在文末追加；
//   - 其它内容逐字节保留。
func patchTOML(doc string, scalars map[string]any, tables map[string]map[string]any) (string, error) {
	if len(scalars) == 0 && len(tables) == 0 {
		return doc, nil
	}
	if _, err := parseTOML(doc); err != nil {
		return "", fmt.Errorf("existing document does not parse: %w", err)
	}
	lines := scanLines(doc)

	// 1) 顶层标量。
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
			return "", fmt.Errorf("marshal %s: %w", key, err)
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

	// 2) 受管表：整表替换或追加。
	for _, name := range sortedKeys(tables) {
		body, err := renderTableBody(tables[name])
		if err != nil {
			return "", err
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
		replacement := []tomlLine{{text: "[" + name + "]", header: name}}
		for _, b := range strings.Split(body, "\n") {
			replacement = append(replacement, tomlLine{text: b})
		}
		lines = append(lines[:start], append(replacement, lines[end:]...)...)
	}
	return strings.Join(lineTexts(lines), "\n"), nil
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

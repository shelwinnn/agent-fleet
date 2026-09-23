// 本文件实现 OpenSSH include 导出（FR-12.6、架构 §7.7、spec §13.5）：把 Fleet 自己
// 持有显式连接字段的机器渲染成 ~/.ssh/agent-fleet.conf，供 Codex Desktop Remote SSH
// 一类工具从 OpenSSH 配置发现主机（spec §40）。渲染是纯函数（同输入必同输出，§32.1），
// 本文件只产出文本与路径，**不写任何文件**，也绝不改写操作者主 SSH 配置。

package sshtransport

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/shelwinnn/agent-fleet/internal/domain"
)

const (
	// IncludeDirective 是操作者主 OpenSSH 配置（~/.ssh/config）需要包含的一行，
	// 供 UI/CLI 提示用。MVP 的三个动作（preview / 导出下载 / 显式确认后安装
	// include 文件）都不触碰主配置——绝不静默改写（FR-12.6、§7.7）。
	IncludeDirective = "Include ~/.ssh/agent-fleet.conf"

	// includeGeneratedHeader 是产物首行：生成来源 + 请勿手工编辑。
	includeGeneratedHeader = "# 由 agent-fleet 生成，请勿手工编辑（FR-12.6）"
	// includeDirectiveHint 是产物第二行：提示主配置需要的那一行。
	includeDirectiveHint = "# 主配置需包含一行：" + IncludeDirective

	// includeFieldIndent 是 Host 段内字段缩进（两个空格，§13.5 示例形态）。
	includeFieldIndent = "  "
	// hostPatternForbiddenChars 是 OpenSSH Host 行里有特殊含义的字符：模式通配（* ?）、
	// 取反（!）、注释（#）、多模式分隔（,）。机器名直接进 Host 行，含这些字符
	// 就不再是"这一台机器"，必须拒绝而不是静默降级（FR-12.6）。
	hostPatternForbiddenChars = "*?!#,"
)

// SkippedHost 记录未被导出到 include 文件的机器及原因（FR-12.6）。
type SkippedHost struct {
	Name   string
	Reason string
}

// IncludeRender 是一次 include 渲染的结果。Content 是产物全文（不含生成时间，
// 以便逐字节 diff 与校验）；GeneratedAt 仅供 UI/审计展示，不参与渲染。
type IncludeRender struct {
	Content     string
	Included    []string
	Skipped     []SkippedHost
	GeneratedAt time.Time
}

// renderedHost 是待输出的一个 Host 段：先收集、后按机器名排序，保证输出顺序确定。
type renderedHost struct {
	name  string
	block string
}

// RenderInclude 渲染 include 文件全文（FR-12.6、架构 §7.7、spec §13.5）。
//
// 纳入条件：spec.ssh 存在且 hostName 非空——HostName 是 OpenSSH Host 段里唯一
// 真正必需的字段，User/Port/ProxyJump/IdentityFile 均可选。仅经 hostAlias 导入的
// 机器记入 Skipped：它们已在操作者自己的 OpenSSH 配置里，不重复导出。
// 不做 managementMode 过滤：agentd 机器的 SSH 字段仍用于 bootstrap/repair（FR-1.2），
// 导出它们对"从 OpenSSH 配置发现主机"同样有意义。
//
// 确定性（§32.1、FR-7.2 同一精神）：Host 段按机器名排序，字段顺序固定为
// HostName/User/Port/ProxyJump/IdentityFile，空值不输出（Port=0 视为未设置）。
//
// 安全（FR-12.6、§29.2）：任何字段值（含机器名）含控制字符、或机器名含 OpenSSH
// Host 模式字符时，返回包了 domain.ErrInvalid 的错误且不产出任何 Content——
// 否则可注入任意 ssh_config 指令。解析 spec 失败同样报错，不静默跳过整台机器。
func RenderInclude(machines []*domain.Machine, generatedAt time.Time) (*IncludeRender, error) {
	hosts := make([]renderedHost, 0, len(machines))
	skipped := make([]SkippedHost, 0, len(machines))
	for _, m := range machines {
		if m == nil {
			continue // 空条目（调用方缺陷）无可渲染信息，不产出 Host 段
		}
		name := m.Metadata.Name
		if err := validateHostPatternName(name); err != nil {
			return nil, err
		}
		var core domain.MachineCoreSpec
		if len(m.Spec) != 0 {
			if err := json.Unmarshal(m.Spec, &core); err != nil {
				return nil, fmt.Errorf("%w: machine %q spec: %v", domain.ErrInvalid, name, err)
			}
		}
		ssh := core.SSH
		if ssh.HostName == "" {
			skipped = append(skipped, SkippedHost{Name: name, Reason: includeSkipReason(ssh)})
			continue
		}
		block, err := renderIncludeHostBlock(name, ssh)
		if err != nil {
			return nil, err
		}
		hosts = append(hosts, renderedHost{name: name, block: block})
	}
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].name < hosts[j].name })
	sort.Slice(skipped, func(i, j int) bool { return skipped[i].Name < skipped[j].Name })

	included := make([]string, 0, len(hosts))
	var b strings.Builder
	b.WriteString(includeGeneratedHeader)
	b.WriteString("\n")
	b.WriteString(includeDirectiveHint)
	b.WriteString("\n")
	for i, h := range hosts {
		if i > 0 {
			b.WriteString("\n") // Host 段之间空一行（§13.5 示例）
		}
		b.WriteString(h.block)
		included = append(included, h.name)
	}
	return &IncludeRender{
		Content:     b.String(),
		Included:    included,
		Skipped:     skipped,
		GeneratedAt: generatedAt,
	}, nil
}

// InstallTarget 返回 include 文件的安装目标路径（FR-12.6、§13.5）：
// <home>/.ssh/agent-fleet.conf。本函数只计算路径——安装/更新是操作者显式确认后的
// 独立动作，且只写这一个文件，不触碰 ~/.ssh/config。
func InstallTarget(home string) string {
	return filepath.Join(home, ".ssh", "agent-fleet.conf")
}

// renderIncludeHostBlock 渲染单个 Host 段：字段顺序固定，空值不输出，缩进两个空格（§13.5）。
func renderIncludeHostBlock(name string, ssh domain.MachineSSHSpec) (string, error) {
	for _, f := range []struct{ field, value string }{
		{"ssh.hostName", ssh.HostName},
		{"ssh.user", ssh.User},
		{"ssh.proxyJump", ssh.ProxyJump},
		{"ssh.identityFile", ssh.IdentityFile},
	} {
		if err := validateIncludeValue(f.field, f.value); err != nil {
			return "", err
		}
	}
	if ssh.Port < 0 || ssh.Port > 65535 {
		return "", fmt.Errorf("%w: machine %q ssh.port %d out of range 0-65535 (0 = unset)",
			domain.ErrInvalid, name, ssh.Port)
	}

	var b strings.Builder
	b.WriteString("Host " + name + "\n")
	b.WriteString(includeFieldIndent + "HostName " + ssh.HostName + "\n")
	if ssh.User != "" {
		b.WriteString(includeFieldIndent + "User " + ssh.User + "\n")
	}
	if ssh.Port != 0 {
		b.WriteString(includeFieldIndent + "Port " + strconv.Itoa(ssh.Port) + "\n")
	}
	if ssh.ProxyJump != "" {
		b.WriteString(includeFieldIndent + "ProxyJump " + ssh.ProxyJump + "\n")
	}
	if ssh.IdentityFile != "" {
		// 只输出路径字符串本身：本模块从不读取私钥文件，服务端也不把私钥内容入库
		//（FR-12.2、§29.2）。
		b.WriteString(includeFieldIndent + "IdentityFile " + ssh.IdentityFile + "\n")
	}
	return b.String(), nil
}

// validateHostPatternName 校验机器名能否作为 OpenSSH Host 模式（FR-12.6）。
// 机器名非法视为数据损坏：直接拒绝整次渲染，不静默跳过（§32.1 同精神）。
func validateHostPatternName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: machine name is empty, cannot render a Host pattern", domain.ErrInvalid)
	}
	if err := validateIncludeValue("machine name", name); err != nil {
		return err
	}
	if strings.ContainsAny(name, hostPatternForbiddenChars) || strings.IndexFunc(name, unicode.IsSpace) >= 0 {
		return fmt.Errorf("%w: machine name %q contains OpenSSH Host pattern characters (whitespace or any of %q)",
			domain.ErrInvalid, name, hostPatternForbiddenChars)
	}
	return nil
}

// validateIncludeValue 拒绝任何会把一行拆成多行或带不可见控制字符的值：
// \n、\r、NUL 及其它控制字符都是 ssh_config 注入向量（FR-12.6）。
func validateIncludeValue(field, value string) error {
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: %s %q contains control character %q; refusing to render it into the OpenSSH include (FR-12.6)",
				domain.ErrInvalid, field, value, r)
		}
	}
	return nil
}

// includeSkipReason 给出机器未纳入导出的原因（FR-12.6、§7.7）。hostName 为空时才会调用。
func includeSkipReason(ssh domain.MachineSSHSpec) string {
	switch {
	case ssh.HostAlias != "":
		// 经既有 hostAlias 导入：已在操作者自己的 OpenSSH 配置里，不重复导出。
		return "imported via hostAlias (already in operator config)"
	case ssh.User != "" || ssh.Port != 0 || ssh.ProxyJump != "" || ssh.IdentityFile != "":
		// 有显式字段但缺 HostName：不产出无法工作的 Host 段，如实报告。
		return "ssh.hostName is required to render a Host block"
	default:
		return "no explicit ssh connection fields"
	}
}

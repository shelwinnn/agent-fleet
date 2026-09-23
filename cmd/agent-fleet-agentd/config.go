package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config 是 agentd 的引导配置（§5.2：~/.config/agent-fleet/agentd.yaml，
// 含控制面地址、CA 证书路径、间隔配置——bootstrap 时经 SSH 下发，第 6 片；
// 本片由操作者手工放置）。
type Config struct {
	// Server 是控制面 gRPC 地址 host:port（§4.1 advertiseURL 的 gRPC 端点）。
	Server string `yaml:"server"`
	// MachineID 是本机在控制面的 Machine 名称（身份绑定，§7.5）。
	MachineID string `yaml:"machine_id"`
	// CACert 是校验控制面服务端证书的 Fleet CA 证书路径（FR-11.4：enroll 必须校验服务端）。
	CACert string `yaml:"ca_cert"`
	// DataDir 是节点数据目录（默认 ~/.local/share/agent-fleet，§10.2）。
	DataDir string `yaml:"data_dir"`
	// ServerName 可选：TLS ServerName 覆盖（server 地址与证书 SAN 不一致时）。
	ServerName string `yaml:"server_name"`
	// Token 仅 enroll 阶段使用；不入日志。留空时 enroll 打印指纹并等待签发 token。
	Token string `yaml:"token"`
	// 间隔覆盖（≤0 时采用服务端 Welcome 回传值或默认值）。
	HeartbeatIntervalSeconds int `yaml:"heartbeat_interval_seconds"`
	InventoryIntervalSeconds int `yaml:"inventory_interval_seconds"`
}

// DefaultConfigPath 返回 ~/.config/agent-fleet/agentd.yaml（§5.2）。
func DefaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "agentd.yaml"
	}
	return filepath.Join(home, ".config", "agent-fleet", "agentd.yaml")
}

// DefaultDataDir 返回 ~/.local/share/agent-fleet（§10.2 节点布局）。
func DefaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "agent-fleet-node"
	}
	return filepath.Join(home, ".local", "share", "agent-fleet")
}

// LoadConfig 读取并规范化配置；flags 中非空的值覆盖文件值。
func LoadConfig(path, server, machine, caCert, dataDir, token string) (*Config, error) {
	cfg := &Config{DataDir: DefaultDataDir()}
	if b, err := os.ReadFile(path); err == nil {
		if err := yaml.Unmarshal(b, cfg); err != nil {
			return nil, fmt.Errorf("parse config %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	if server != "" {
		cfg.Server = server
	}
	if machine != "" {
		cfg.MachineID = machine
	}
	if caCert != "" {
		cfg.CACert = caCert
	}
	if token != "" {
		cfg.Token = token
	}
	if cfg.DataDir == "" || dataDir != "" {
		if dataDir != "" {
			cfg.DataDir = dataDir
		}
	}
	expandHome := func(p string) string {
		if p == "~" || strings.HasPrefix(p, "~/") {
			home, err := os.UserHomeDir()
			if err == nil {
				return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
			}
		}
		return p
	}
	cfg.CACert = expandHome(cfg.CACert)
	cfg.DataDir = expandHome(cfg.DataDir)
	if cfg.DataDir == "" {
		cfg.DataDir = DefaultDataDir()
	}
	return cfg, nil
}

// ValidateForEnroll 校验 enroll 所需字段。
func (c *Config) ValidateForEnroll() error {
	if c.Server == "" {
		return fmt.Errorf("config: server is required")
	}
	if c.MachineID == "" {
		return fmt.Errorf("config: machine_id is required")
	}
	if c.CACert == "" {
		return fmt.Errorf("config: ca_cert is required (verify the server certificate, FR-11.4)")
	}
	return nil
}

// ValidateForDaemon 校验 daemon 所需字段。
func (c *Config) ValidateForDaemon() error {
	if err := c.ValidateForEnroll(); err != nil {
		return err
	}
	if _, err := os.Stat(c.clientCertPath()); err != nil {
		return fmt.Errorf("client certificate missing at %s (run `agent-fleet-agentd enroll` first): %w",
			c.clientCertPath(), err)
	}
	return nil
}

// pkiDir 返回节点 pki 目录（§10.2：pki/{client.crt, client.key(0600), ca.crt}）。
func (c *Config) pkiDir() string         { return filepath.Join(c.DataDir, "pki") }
func (c *Config) clientKeyPath() string  { return filepath.Join(c.pkiDir(), "client.key") }
func (c *Config) clientCertPath() string { return filepath.Join(c.pkiDir(), "client.crt") }
func (c *Config) csrPath() string        { return filepath.Join(c.pkiDir(), "client.csr") }

func (c *Config) heartbeatInterval() time.Duration {
	if c.HeartbeatIntervalSeconds > 0 {
		return time.Duration(c.HeartbeatIntervalSeconds) * time.Second
	}
	return 0 // 交给 Welcome / 默认值
}

func (c *Config) inventoryInterval() time.Duration {
	if c.InventoryIntervalSeconds > 0 {
		return time.Duration(c.InventoryIntervalSeconds) * time.Second
	}
	return 0
}

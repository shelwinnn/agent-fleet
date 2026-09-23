package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"os"
	"time"

	fleetv1 "github.com/shelwinnn/agent-fleet/api/proto/fleet/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// runEnroll 实现 bootstrap 签发（§7.5 十步中的节点侧 5–9 步）：
//  1. 生成本地私钥（0600）与 CSR；
//  2. 无 token 时：打印公钥指纹（供操作者在控制面签发绑定该指纹的一次性 token）后退出；
//  3. 有 token 时：经 TLS（校验服务端证书，FR-11.4）调用 Enroll，把签发的客户端证书落盘 0600。
//
// token 只经 flag/配置传给 gRPC 请求，不打印、不落日志（KM-22 边界）。
func runEnroll(cfg *Config, log *slog.Logger) error {
	if err := cfg.ValidateForEnroll(); err != nil {
		return err
	}
	csrPEM, fingerprint, err := ensureKeyAndCSR(cfg)
	if err != nil {
		return err
	}

	if cfg.Token == "" {
		fmt.Printf("machine_id: %s\n", cfg.MachineID)
		fmt.Printf("csr: %s\n", cfg.csrPath())
		fmt.Printf("public key fingerprint: %s\n", fingerprint)
		fmt.Println("next: POST /api/v1/machines/" + cfg.MachineID + "/enroll-token" +
			` with body {"csrPubKeySha256": "` + fingerprint + `"}`)
		fmt.Println("then put the token into agentd.yaml (token:) or pass --token and re-run enroll.")
		return nil
	}

	tlsCfg, err := loadClientTLSForEnroll(cfg)
	if err != nil {
		return err
	}
	conn, err := grpc.NewClient(cfg.Server,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	if err != nil {
		return fmt.Errorf("dial %s: %w", cfg.Server, err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := fleetv1.NewFleetEnrollmentServiceClient(conn)
	resp, err := client.Enroll(ctx, &fleetv1.EnrollRequest{
		MachineId: cfg.MachineID,
		Token:     cfg.Token,
		CsrPem:    csrPEM,
	})
	if err != nil {
		return fmt.Errorf("enroll rejected: %w", err)
	}
	if err := storeClientCert(cfg, resp.GetCertPem()); err != nil {
		return err
	}
	// 冗余保存 CA 证书（响应携带；与 §7.5 时序一致）。
	_ = resp.GetCaPem()
	log.Info("enrolled; client certificate stored (0600)",
		"machine_id", cfg.MachineID,
		"cert_path", cfg.clientCertPath(),
		"not_after_unix", resp.GetNotAfterUnix())
	// 消费后立刻从内存丢弃 token。
	cfg.Token = ""
	return nil
}

// loadClientTLSForEnroll 构建 enroll 阶段的 TLS 配置：此时还没有客户端证书
// （token 才是身份证明），但必须校验服务端证书（FR-11.4：无客户端证书不等于允许明文）。
func loadClientTLSForEnroll(cfg *Config) (*tls.Config, error) {
	caPEM, err := os.ReadFile(cfg.CACert)
	if err != nil {
		return nil, fmt.Errorf("read ca cert %s: %w", cfg.CACert, err)
	}
	pool := x509Pool(caPEM)
	if pool == nil {
		return nil, fmt.Errorf("no valid certificate in %s", cfg.CACert)
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    pool,
		ServerName: cfg.ServerName,
	}, nil
}

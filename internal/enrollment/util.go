package enrollment

import (
	"crypto/rand"
	"io"
	"math/big"
	"net"
)

// randReader 可在测试中替换的随机源。
var randReader io.Reader = rand.Reader

// randomSerial 生成长度足够的随机证书序列号（≥128-bit）。
func randomSerial() *big.Int {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	n, err := rand.Int(randReader, limit)
	if err != nil {
		panic("enrollment: crypto/rand unavailable: " + err.Error())
	}
	return n
}

// loopbackIPs 服务端证书的 SAN（本机监听场景）。
func loopbackIPs() []net.IP {
	return []net.IP{
		net.IPv4(127, 0, 0, 1),
		net.ParseIP("::1"),
	}
}

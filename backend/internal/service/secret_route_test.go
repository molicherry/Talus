package service

import (
	"context"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/pkg/crypto"
	"golang.org/x/crypto/ssh"
)

func TestCurrentSecretsAuthenticateSSHAndSOCKS(t *testing.T) {
	address, hostKey := startExecSSHServer(t, func(_ string, channel ssh.Channel, _ <-chan struct{}) {
		_, _ = io.WriteString(channel, "current-secret-ok")
		_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
		_ = channel.Close()
	})
	svc, source := routeTestFixture(t, address, hostKey)
	salt := []byte("0123456789abcdef")
	current, err := svc.credSvc.masterKey.ForRecord(crypto.VersionCurrent, salt, crypto.PurposeSSH)
	if err != nil {
		t.Fatal(err)
	}
	password, _ := current.Encrypt("password", []byte("password"))
	credential := source.servers[1].Credential
	credential.CryptoVersion, credential.EncryptedPassword = crypto.VersionCurrent, &password
	for _, proxied := range []bool{false, true} {
		if proxied {
			proxy, _ := startServiceSOCKSProxy(t, address, "operator", "proxy-password")
			host, portText, _ := net.SplitHostPort(proxy)
			port, _ := strconv.Atoi(portText)
			cipher, _ := svc.credSvc.masterKey.ForRecord(crypto.VersionCurrent, salt, crypto.PurposeSOCKS5)
			secret, _ := cipher.Encrypt("password", []byte("proxy-password"))
			source.configs[1] = &model.ServerConnectionConfig{CryptoVersion: crypto.VersionCurrent, ServerID: 1, Mode: "socks5", SocksHost: host, SocksPort: port, SocksAuthType: "username_password", SocksUsername: "operator", EncryptedPassword: &secret, Salt: salt}
		}
		result, err := svc.Exec(context.Background(), 1, "test-current-secret", 2*time.Second)
		if err != nil || result == nil || result.Stdout != "current-secret-ok" {
			t.Fatalf("proxied=%v: %v", proxied, err)
		}
	}
}

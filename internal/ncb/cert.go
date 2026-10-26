// Package ncb 提供 ncb 的營運動作：放自簽憑證，或走 Let's Encrypt 簽發。
package ncb

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flysangel/appctl/internal/podman"
)

const (
	nginxContainer   = "ncb-nginx"
	certbotContainer = "ncb-certbot"
	certbotVolume    = "ncb-certbot"
	selfSignedDays   = 825
)

// Options 是 Cert 的輸入。
type Options struct {
	ACME    bool   // true 時走 Let's Encrypt，false 時放自簽憑證
	Email   string // ACME 必填
	Staging bool   // ACME 用 staging 環境；CLI 預設 true
	Force   bool   // 自簽時允許覆蓋卷裡已有的憑證檔
}

// Cert 依 default.conf 的第一個 server_name 放憑證，再 reload nginx。要 root。
func Cert(ctx context.Context, env podman.Env, root string, opts Options) error {
	domain, err := ServerName(filepath.Join(root, "services", "ncb", "podman", "default.conf"))
	if err != nil {
		return err
	}
	if opts.ACME {
		if strings.HasSuffix(strings.ToLower(domain), ".local") {
			return fmt.Errorf("%s 以 .local 結尾，是 mDNS 保留網域，Let's Encrypt 不會簽發；請改用自簽（不加 --acme），或把 server_name 換成公網網域", domain)
		}
		if opts.Email == "" {
			return errors.New("--acme 要加 --email <信箱>")
		}
	}
	if env.IsRoot == nil || !env.IsRoot() {
		return errors.New("ncb cert 要 root 權限，請改用 sudo appctl ncb cert")
	}
	if opts.ACME {
		return acme(ctx, env, domain, opts)
	}
	return selfSigned(ctx, env, domain, opts.Force)
}

// ServerName 回傳 nginx 設定檔第一個 server_name 的第一個值，忽略註解行。
func ServerName(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "#") {
			continue
		}
		rest, ok := strings.CutPrefix(line, "server_name")
		if !ok || rest == "" || (rest[0] != ' ' && rest[0] != '\t') {
			continue
		}
		rest, _, _ = strings.Cut(rest, "#")
		fields := strings.Fields(strings.TrimSuffix(strings.TrimSpace(rest), ";"))
		if len(fields) > 0 {
			return strings.TrimSuffix(fields[0], ";"), nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("%s 找不到 server_name", path)
}

func say(env podman.Env, format string, a ...any) {
	if env.Out != nil {
		fmt.Fprintf(env.Out, format+"\n", a...)
	}
}

func selfSigned(ctx context.Context, env podman.Env, domain string, force bool) error {
	out, err := env.Runner.Run(ctx, "podman", []string{"volume", "inspect", certbotVolume, "--format", "{{.Mountpoint}}"}, nil)
	if err != nil {
		return fmt.Errorf("查不到卷 %s，請先 sudo appctl apply ncb --track podman：%w", certbotVolume, err)
	}
	mount := strings.TrimSpace(string(out))
	if mount == "" {
		return fmt.Errorf("卷 %s 沒有 Mountpoint", certbotVolume)
	}
	dir := filepath.Join(mount, "live", domain)
	certPath, keyPath := filepath.Join(dir, "fullchain.pem"), filepath.Join(dir, "privkey.pem")
	if !force {
		for _, p := range []string{certPath, keyPath} {
			if _, err := os.Lstat(p); err == nil {
				return fmt.Errorf("%s 已存在，不覆蓋；確定要換掉時加 --force", p)
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	certPEM, keyPEM, err := genSelfSigned(domain)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := writeFile(certPath, certPEM, 0o644); err != nil {
		return err
	}
	if err := writeFile(keyPath, keyPEM, 0o600); err != nil {
		return err
	}
	say(env, "Success: 已放自簽憑證到 %s（有效 %d 天，瀏覽器會警告不受信任）", dir, selfSignedDays)
	reload(ctx, env)
	return nil
}

// writeFile 先刪舊檔再建新檔，權限不受既有檔影響，也不跟著符號連結寫到別處。
func writeFile(path string, data []byte, mode os.FileMode) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

func genSelfSigned(domain string) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	tpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: domain},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(selfSignedDays * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	if ip := net.ParseIP(domain); ip != nil {
		tpl.IPAddresses = []net.IP{ip}
	} else {
		tpl.DNSNames = []string{domain}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

func acme(ctx context.Context, env podman.Env, domain string, opts Options) error {
	args := []string{"exec", certbotContainer, "certbot", "certonly", "--webroot", "-w", "/etc/letsencrypt/www",
		"-d", domain, "--email", opts.Email, "--agree-tos"}
	if opts.Staging {
		args = append(args, "--staging")
		say(env, "Info: 用 Let's Encrypt staging 環境，簽出的憑證瀏覽器不信任；確認流程可行後再加 --no-staging")
	}
	if _, err := env.Runner.Run(ctx, "podman", args, nil); err != nil {
		return fmt.Errorf("certbot 簽發失敗：%w", err)
	}
	say(env, "Success: certbot 已簽發 %s 的憑證", domain)
	reload(ctx, env)
	return nil
}

// reload 讓 nginx 載入新憑證。nginx 還沒起來時只提示，不算失敗：憑證檔已經放好。
func reload(ctx context.Context, env podman.Env) {
	if _, err := env.Runner.Run(ctx, "podman", []string{"exec", nginxContainer, "nginx", "-s", "reload"}, nil); err != nil {
		say(env, "Info: 無法 reload %s（%v）；nginx 要有憑證才起得來，請跑 sudo appctl apply ncb --track podman", nginxContainer, err)
		return
	}
	say(env, "Success: 已 reload %s", nginxContainer)
}

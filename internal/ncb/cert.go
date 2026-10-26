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
	"github.com/flysangel/appctl/internal/run"
)

const (
	nginxContainer   = "ncb-nginx"
	certbotContainer = "ncb-certbot"
	certbotVolume    = "ncb-certbot"
	selfSignedDays   = 825
)

// Options 是 Cert 的輸入。
type Options struct {
	ACME       bool   // true 時走 Let's Encrypt，false 時放自簽憑證
	Email      string // ACME 必填
	Force      bool   // 自簽時允許覆蓋卷裡已有的憑證檔
	Production bool   // ACME 用正式環境；零值是 staging，避免誤打正式環境撞 rate limit
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

// volumeMount 回傳 ncb-certbot 卷在主機上的路徑。
func volumeMount(ctx context.Context, env podman.Env) (string, error) {
	out, err := env.Runner.Run(ctx, "podman", []string{"volume", "inspect", certbotVolume, "--format", "{{.Mountpoint}}"}, nil)
	if err != nil {
		return "", fmt.Errorf("查不到卷 %s，請先 sudo appctl apply ncb --track podman：%w", certbotVolume, err)
	}
	mount := strings.TrimSpace(string(out))
	if mount == "" {
		return "", fmt.Errorf("卷 %s 沒有 Mountpoint", certbotVolume)
	}
	return mount, nil
}

func selfSigned(ctx context.Context, env podman.Env, domain string, force bool) error {
	mount, err := volumeMount(ctx, env)
	if err != nil {
		return err
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
	if err := writeFile(keyPath, keyPEM, 0o600); err != nil {
		return err
	}
	if err := writeFile(certPath, certPEM, 0o644); err != nil {
		return err
	}
	say(env, "Success: 已放自簽憑證到 %s（有效 %d 天，瀏覽器會警告不受信任）", dir, selfSignedDays)
	return reload(ctx, env)
}

// writeFile 在同目錄建暫存檔（O_EXCL，先設好權限）再 rename 蓋過去：
// 不跟著符號連結寫到別處，中途失敗也不會留下半個檔。
func writeFile(path string, data []byte, mode os.FileMode) error {
	tmp, err := stage(path, data, mode)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func stage(path string, data []byte, mode os.FileMode) (string, error) {
	tmp := path + ".tmp"
	os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return "", err
	}
	_, werr := f.Write(data)
	cerr := f.Close()
	if err := errors.Join(werr, cerr, os.Chmod(tmp, mode)); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return tmp, nil
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

// moveSelfSignedAside 在 live/<domain>/ 存在、但 renewal/<domain>.conf 不存在時（不是 certbot 的 lineage，
// 是 selfSigned 放的），把它搬到 live/<domain>.selfsigned/（取代舊的）。certbot 遇到已存在的 live 目錄
// 會拒絕建 lineage。回傳搬過去的路徑；沒搬時回空字串。
func moveSelfSignedAside(mount, domain string) (string, error) {
	live := filepath.Join(mount, "live", domain)
	if _, err := os.Lstat(live); errors.Is(err, os.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	if _, err := os.Lstat(filepath.Join(mount, "renewal", domain+".conf")); err == nil {
		return "", nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	aside := live + ".selfsigned"
	if err := os.RemoveAll(aside); err != nil {
		return "", err
	}
	if err := os.Rename(live, aside); err != nil {
		return "", err
	}
	return aside, nil
}

func acme(ctx context.Context, env podman.Env, domain string, opts Options) error {
	mount, err := volumeMount(ctx, env)
	if err != nil {
		return err
	}
	aside, err := moveSelfSignedAside(mount, domain)
	if err != nil {
		return fmt.Errorf("移開自簽憑證失敗：%w", err)
	}
	if aside != "" {
		say(env, "Info: 已把自簽憑證移到 %s，讓 certbot 建立 %s 的憑證", aside, domain)
	}
	args := []string{"exec", certbotContainer, "certbot", "certonly", "--webroot", "-w", "/etc/letsencrypt/www",
		"-d", domain, "--cert-name", domain, "--email", opts.Email, "--agree-tos"}
	if !opts.Production {
		args = append(args, "--staging")
		say(env, "Info: 用 Let's Encrypt staging 環境，簽出的憑證瀏覽器不信任；確認流程可行後再加 --no-staging（正式環境）")
	}
	if _, err := env.Runner.Run(ctx, "podman", args, nil); err != nil {
		err = fmt.Errorf("certbot 簽發失敗：%w", err)
		if aside != "" {
			// 搬回自簽憑證，nginx 才能繼續用它
			live := filepath.Join(mount, "live", domain)
			if rerr := os.Rename(aside, live); rerr != nil {
				return errors.Join(err, fmt.Errorf("自簽憑證搬不回 %s，還在 %s：%w", live, aside, rerr))
			}
			say(env, "Info: 已把自簽憑證搬回 %s", live)
		}
		return err
	}
	say(env, "Success: certbot 已簽發 %s 的憑證", domain)
	return reload(ctx, env)
}

// nginxRunning 查 ncb-nginx 是否在跑。exists 只有結束碼 1 算不存在，其他非零照錯誤回。
func nginxRunning(ctx context.Context, env podman.Env) (bool, error) {
	if _, err := env.Runner.Run(ctx, "podman", []string{"container", "exists", nginxContainer}, nil); err != nil {
		if code, ok := run.ExitCode(err); ok && code == 1 {
			return false, nil
		}
		return false, fmt.Errorf("查不出 %s 是否存在：%w", nginxContainer, err)
	}
	out, err := env.Runner.Run(ctx, "podman", []string{"inspect", "--format", "{{.State.Running}}", nginxContainer}, nil)
	if err != nil {
		return false, fmt.Errorf("查不出 %s 是否在跑：%w", nginxContainer, err)
	}
	return strings.TrimSpace(string(out)) == "true", nil
}

// reload 讓 nginx 載入新憑證。nginx 沒在跑時只提示：憑證檔已經放好，apply 時 nginx 會讀到。
// nginx 在跑但 reload 失敗是真的錯誤（例如新憑證讓 nginx -t 失敗），回錯並帶 stderr。
func reload(ctx context.Context, env podman.Env) error {
	running, err := nginxRunning(ctx, env)
	if err != nil {
		return fmt.Errorf("憑證檔已放好，但無法 reload：%w", err)
	}
	if !running {
		say(env, "Info: %s 沒在跑，沒有 reload；nginx 要有憑證才起得來，已部署過就跑 sudo systemctl restart %s.service，還沒部署就跑 sudo appctl apply ncb --track podman", nginxContainer, nginxContainer)
		return nil
	}
	if _, err := env.Runner.Run(ctx, "podman", []string{"exec", nginxContainer, "nginx", "-s", "reload"}, nil); err != nil {
		return fmt.Errorf("憑證檔已放好，但 %s reload 失敗：%w", nginxContainer, err)
	}
	say(env, "Success: 已 reload %s", nginxContainer)
	return nil
}

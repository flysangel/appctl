// Package quay 提供 quay 的營運動作：建立第一位 superuser。
package quay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/flysangel/appctl/internal/gmm"
)

// DefaultTimeout 是等 quay health 通過的上限；quay 首次啟動要跑 DB migration，要等很久。
const DefaultTimeout = 600 * time.Second

const defaultInterval = 5 * time.Second

// Options 是 Init 的輸入。
type Options struct {
	Root     string        // repo 根目錄
	BaseURL  string        // quay 網址，例如 http://127.0.0.1:8080
	Timeout  time.Duration // 等 health 的上限；0 時用 DefaultTimeout
	Interval time.Duration // 輪詢間隔；0 時用 5 秒，測試換小
	Out      io.Writer     // Info: 與 Success:
}

// Init 等 quay health 通過，再用 SUPER_USERS 的第一個帳號建立第一位 superuser。
// 使用者表已經不是空的時印 Info 並回 nil，所以可以重複跑。
func Init(ctx context.Context, opts Options) error {
	user, pass, email, err := loadCreds(opts.Root)
	if err != nil {
		return err
	}
	timeout, interval := opts.Timeout, opts.Interval
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if interval <= 0 {
		interval = defaultInterval
	}
	base := strings.TrimRight(opts.BaseURL, "/")
	hc := &http.Client{Timeout: 30 * time.Second}
	if err := waitHealth(ctx, hc, base, timeout, interval); err != nil {
		return err
	}
	body, err := json.Marshal(map[string]string{"username": user, "password": pass, "email": email})
	if err != nil {
		return err
	}
	const path = "/api/v1/user/initialize"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("POST %s 連不上：%v", path, urlCause(err))
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		fmt.Fprintf(opts.Out, "Success: 已建立 quay 第一位 superuser %s\n", user)
		return nil
	case isAlreadyInitialized(resp.StatusCode, respBody):
		fmt.Fprintln(opts.Out, "Info: quay 已經初始化")
		return nil
	}
	return fmt.Errorf("POST %s 回 %d", path, resp.StatusCode)
}

// isAlreadyInitialized 判斷使用者表不是空的。
// 注意：這個回應形狀（400 加 "non-empty database" 字樣）依 Quay 文件寫成，
// 還沒對真的 quay 驗證過，真環境步驟要確認實際回應與 quay 版本。
func isAlreadyInitialized(status int, body []byte) bool {
	return status == http.StatusBadRequest && strings.Contains(strings.ToLower(string(body)), "non-empty database")
}

func urlCause(err error) error {
	type unwrapper interface{ Unwrap() error }
	if u, ok := err.(unwrapper); ok && u.Unwrap() != nil {
		return u.Unwrap()
	}
	return err
}

func waitHealth(ctx context.Context, hc *http.Client, base string, timeout, interval time.Duration) error {
	deadline := time.Now().Add(timeout)
	last := "尚未回應"
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/health/instance", nil)
		if err != nil {
			return err
		}
		if resp, err := hc.Do(req); err != nil {
			last = fmt.Sprint(urlCause(err))
		} else {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			last = fmt.Sprintf("回 %d", resp.StatusCode)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().Add(interval).After(deadline) {
			return fmt.Errorf("等 quay health 超過 %s（最後一次：%s）", timeout, last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

// loadCreds 讀 SUPER_USERS 的第一個帳號，以及 secret.env 的密碼與信箱。
func loadCreds(root string) (user, pass, email string, err error) {
	dir := filepath.Join(root, "services", "quay", "secret")
	raw, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		return "", "", "", fmt.Errorf("讀不到 quay 的 config.yaml：%w", err)
	}
	var cfg struct {
		SuperUsers []string `yaml:"SUPER_USERS"`
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return "", "", "", fmt.Errorf("config.yaml 解析失敗：%w", err)
	}
	if len(cfg.SuperUsers) == 0 || cfg.SuperUsers[0] == "" {
		return "", "", "", errors.New("config.yaml 的 SUPER_USERS 是空的")
	}
	env, err := gmm.ReadConf(filepath.Join(dir, "secret.env"))
	if err != nil {
		return "", "", "", fmt.Errorf("讀不到 quay 的 secret.env：%w", err)
	}
	for _, k := range []string{"QUAY_SUPERUSER_PASSWORD", "QUAY_SUPERUSER_EMAIL"} {
		if env[k] == "" {
			return "", "", "", fmt.Errorf("secret.env 缺 %s", k)
		}
	}
	return cfg.SuperUsers[0], env["QUAY_SUPERUSER_PASSWORD"], env["QUAY_SUPERUSER_EMAIL"], nil
}

package gmm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// apiError 是一次 API 呼叫的失敗。訊息只帶方法、路徑與狀態碼，不帶 body。
type apiError struct{ msg string }

func (e *apiError) Error() string { return e.msg }

// client 呼叫 guacamole REST API。token 放 Guacamole-Token 標頭，不進 URL。
type client struct {
	server string
	api    string
	token  string
	http   *http.Client
}

func newClient(server, dataSource string) *client {
	server = strings.TrimRight(server, "/")
	return &client{
		server: server,
		api:    server + "/api/session/data/" + dataSource,
		http:   &http.Client{Timeout: 30 * time.Second},
	}
}

// send 送出請求，狀態碼不是 2xx 就回錯；out 非 nil 且 body 非空時解碼 JSON。
func (c *client) send(req *http.Request, out any) error {
	where := req.Method + " " + req.URL.Path
	resp, err := c.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return &apiError{fmt.Sprintf("%s 連不上：%v", where, err)}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &apiError{fmt.Sprintf("%s 讀回應失敗：%v", where, err)}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &apiError{fmt.Sprintf("%s 回 %d", where, resp.StatusCode)}
	}
	if out == nil || len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return &apiError{fmt.Sprintf("%s 回應不是預期的 JSON：%v", where, err)}
	}
	return nil
}

// login 取 token。密碼只放在表單 body。
func (c *client) login(ctx context.Context, user, password string) error {
	form := url.Values{"username": {user}, "password": {password}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.server+"/api/tokens", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var resp struct {
		AuthToken string `json:"authToken"`
	}
	if err := c.send(req, &resp); err != nil {
		return err
	}
	if resp.AuthToken == "" {
		return &apiError{"取不到 authToken"}
	}
	c.token = resp.AuthToken
	return nil
}

// call 呼叫資料源底下的 path。body 非 nil 時送 JSON。
func (c *client) call(ctx context.Context, method, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.api+"/"+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Guacamole-Token", c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.send(req, out)
}

// list 取一種資源的列表：identifier 對物件。
func (c *client) list(ctx context.Context, kind string) (map[string]map[string]any, error) {
	out := map[string]map[string]any{}
	if err := c.call(ctx, http.MethodGet, kind, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

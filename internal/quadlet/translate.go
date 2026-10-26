// Package quadlet 把 podman 軌的渲染產物（k8s YAML）翻成 Quadlet 單元。
// 形狀見設計文件〈5.3 翻譯器〉：每個服務一個 pod（<svc>-pod.service，一般 systemd 單元），
// 每個容器一個 .container 用 --pod 加入，每個 PVC 一個 .volume。
package quadlet

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/flysangel/appctl/internal/setting"
)

// File.Dir 的值：單元要寫到哪個目錄由部署端決定，這裡只標種類。
const (
	DirQuadlet = "quadlet" // .container、.volume：交給 Quadlet 產生器
	DirSystemd = "systemd" // <svc>-pod.service：一般 systemd 單元
)

// ConfigRoot 是 ConfigMap 檔在主機上的根目錄。
const ConfigRoot = "/var/lib/appctl"

// File 是一個要寫到主機的檔。Units 的 Name 是檔名、Dir 是 DirQuadlet 或 DirSystemd；
// ConfigFiles 的 Name 是主機上的絕對路徑、Dir 是空字串。
type File struct {
	Name    string
	Content []byte
	Dir     string
}

// Secret 是一個 k8s Secret，Data 是 base64 解碼後的值。
// 每個鍵對應一個 podman secret，名字用 PodmanSecretName(Bundle.Service, Name, 鍵)。
type Secret struct {
	Name string
	Data map[string][]byte
}

// Bundle 是一個服務翻譯後的全部產物。Volumes 是 .volume 單元的 VolumeName。
type Bundle struct {
	Service     string
	Units       []File
	ConfigFiles []File
	Secrets     []Secret
	Volumes     []string
}

// PodmanSecretName 回傳 k8s Secret 的一個鍵對應的 podman secret 名：<svc>_<secret>_<鍵>。
// podman secret 整台主機共用，名字不能跨服務或跨 Secret 撞到。用 _ 分隔不會有歧義：
// k8s Secret 名是 DNS subdomain，不含 _；服務名由 Translate 擋掉 _；
// 所以第一個 _ 前是服務名、第二個 _ 前是 Secret 名，其餘是鍵（鍵可以含 _）。
func PodmanSecretName(svc, secret, key string) string {
	return svc + "_" + secret + "_" + key
}

// ConfigDir 回傳 ConfigMap 在主機上的目錄，底下每個鍵一個檔。
func ConfigDir(svc, configMap string) string {
	return path.Join(ConfigRoot, svc, "configmap", configMap)
}

var (
	k8sVarRef = regexp.MustCompile(`\$\([A-Za-z_][A-Za-z0-9_]*\)`)
	quantity  = regexp.MustCompile(`^([0-9]+)(Ki|Mi|Gi|Ti|k|M|G|T)?$`)
	sizeUnits = map[string]int64{"": 1, "k": 1e3, "M": 1e6, "G": 1e9, "T": 1e12,
		"Ki": 1 << 10, "Mi": 1 << 20, "Gi": 1 << 30, "Ti": 1 << 40}
)

type volume struct {
	pvc       string // persistentVolumeClaim.claimName 或 volumeClaimTemplates 名
	configMap string
	secret    string
	emptyDir  bool
	memory    bool
	sizeLimit string
	users     int
}

type translator struct {
	svc        string
	errs       []string
	configMaps map[string]map[string]string
	secrets    map[string]map[string][]byte
	volumes    map[string]*volume
}

func (t *translator) fail(format string, a ...any) {
	t.errs = append(t.errs, fmt.Sprintf(format, a...))
}

// Translate 把一個服務的渲染產物翻成 Quadlet 單元。s 是 setting.conf，決定 pod 的主機埠，不能是 nil 或空。
// 白名單外的欄位與無法翻譯的值全部列出後一起回錯。
func Translate(svc string, docs []map[string]any, s *setting.Setting) (*Bundle, error) {
	if bad := checkWhitelist(docs); len(bad) > 0 {
		return nil, fmt.Errorf("%s 有翻譯器不支援的欄位或 kind：\n  %s", svc, strings.Join(bad, "\n  "))
	}
	if strings.Contains(svc, "_") {
		return nil, fmt.Errorf("服務名 %q 不能含 _：podman secret 名用 _ 分隔服務名、Secret 名與鍵", svc)
	}
	if s == nil {
		return nil, fmt.Errorf("%s 缺少 setting.conf：podman 軌一定要有 [ports]，否則主機埠會消失", svc)
	}
	t := &translator{svc: svc, configMaps: map[string]map[string]string{},
		secrets: map[string]map[string][]byte{}, volumes: map[string]*volume{}}
	b := &Bundle{Service: svc}

	var workloads []map[string]any
	for _, d := range docs {
		md, _ := d["metadata"].(map[string]any)
		name, _ := md["name"].(string)
		data, _ := d["data"].(map[string]any)
		switch d["kind"] {
		case "ConfigMap":
			cm := map[string]string{}
			for k, v := range data {
				cm[k], _ = v.(string)
			}
			t.configMaps[name] = cm
		case "Secret":
			if typ, _ := d["type"].(string); typ != "" && typ != "Opaque" {
				t.fail("Secret/%s：只支援 type Opaque，收到 %s", name, typ)
			}
			if strings.Contains(name, "_") {
				t.fail("Secret/%s：名字不能含 _（k8s 也不允許）", name)
			}
			sec := Secret{Name: name, Data: map[string][]byte{}}
			for k, v := range data {
				str, _ := v.(string)
				raw, err := base64.StdEncoding.DecodeString(str)
				if err != nil {
					t.fail("Secret/%s 的 %s 不是合法的 base64：%v", name, k, err)
				}
				sec.Data[k] = raw
			}
			t.secrets[name] = sec.Data
			b.Secrets = append(b.Secrets, sec)
		default:
			workloads = append(workloads, d)
		}
	}
	if len(workloads) != 1 {
		return nil, fmt.Errorf("%s 要剛好一個工作負載（Pod、Deployment、DaemonSet 或 StatefulSet），收到 %d 個", svc, len(workloads))
	}
	w := workloads[0]
	if err := setting.Validate(s, docs); err != nil {
		return nil, err
	}

	spec, _ := w["spec"].(map[string]any)
	pod := spec
	var templates []string
	if w["kind"] != "Pod" {
		if n, ok := spec["replicas"].(float64); ok && n > 1 {
			t.fail("%s：podman 軌只跑一份，replicas 是 %v", docID(w), n)
		}
		tpl, _ := spec["template"].(map[string]any)
		pod, _ = tpl["spec"].(map[string]any)
		vcts, _ := spec["volumeClaimTemplates"].([]any)
		for _, v := range vcts {
			vm, _ := v.(map[string]any)
			md, _ := vm["metadata"].(map[string]any)
			name, _ := md["name"].(string)
			templates = append(templates, name)
		}
	}

	restart := "always"
	switch rp, _ := pod["restartPolicy"].(string); rp {
	case "", "Always":
	case "OnFailure":
		restart = "on-failure"
	case "Never":
		restart = "no"
	default:
		t.fail("%s：不認得的 restartPolicy %q", docID(w), rp)
	}

	var volUnits []File
	addPVC := func(name, claim string) {
		t.volumes[name] = &volume{pvc: claim}
		b.Volumes = append(b.Volumes, claim)
		volUnits = append(volUnits, File{Name: claim + ".volume", Dir: DirQuadlet,
			Content: []byte("[Volume]\nVolumeName=" + Escape(claim) + "\n")})
	}
	vols, _ := pod["volumes"].([]any)
	for _, v := range vols {
		vm, _ := v.(map[string]any)
		name, _ := vm["name"].(string)
		if pvc, ok := vm["persistentVolumeClaim"].(map[string]any); ok {
			claim, _ := pvc["claimName"].(string)
			addPVC(name, claim)
		} else if cm, ok := vm["configMap"].(map[string]any); ok {
			n, _ := cm["name"].(string)
			if _, ok := t.configMaps[n]; !ok {
				t.fail("卷 %s 引用的 ConfigMap %s 不在渲染產物裡", name, n)
			}
			t.volumes[name] = &volume{configMap: n}
		} else if sec, ok := vm["secret"].(map[string]any); ok {
			n, _ := sec["secretName"].(string)
			if _, ok := t.secrets[n]; !ok {
				t.fail("卷 %s 引用的 Secret %s 不在渲染產物裡", name, n)
			}
			t.volumes[name] = &volume{secret: n}
		} else if ed, ok := vm["emptyDir"].(map[string]any); ok {
			medium, _ := ed["medium"].(string)
			size, _ := ed["sizeLimit"].(string)
			if medium != "" && medium != "Memory" {
				t.fail("卷 %s：emptyDir 的 medium 只支援 Memory，收到 %q", name, medium)
			}
			if medium == "" && size != "" {
				t.fail("卷 %s：emptyDir 的 sizeLimit 只支援 medium: Memory", name)
			}
			t.volumes[name] = &volume{emptyDir: true, memory: medium == "Memory", sizeLimit: size}
		} else {
			t.fail("卷 %s 沒有可翻譯的來源", name)
		}
	}
	for _, name := range templates {
		addPVC(name, name)
	}

	units := []File{t.podUnit(s)}
	containers, _ := pod["containers"].([]any)
	for _, c := range containers {
		cm, _ := c.(map[string]any)
		units = append(units, t.containerUnit(cm, restart))
	}
	for name, v := range t.volumes {
		if v.emptyDir && v.users > 1 {
			t.fail("卷 %s：emptyDir 不能跨容器共用（podman 端每個容器各有一份）", name)
		}
	}
	units = append(units, volUnits...)
	b.Units = units

	var cmNames []string
	for n := range t.configMaps {
		cmNames = append(cmNames, n)
	}
	sort.Strings(cmNames)
	for _, n := range cmNames {
		for _, k := range sortedKeys(t.configMaps[n]) {
			b.ConfigFiles = append(b.ConfigFiles, File{Name: path.Join(ConfigDir(svc, n), k),
				Content: []byte(t.configMaps[n][k])})
		}
	}

	if len(t.errs) > 0 {
		sort.Strings(t.errs)
		return nil, fmt.Errorf("%s 無法翻譯：\n  %s", svc, strings.Join(t.errs, "\n  "))
	}
	return b, nil
}

func (t *translator) podUnit(s *setting.Setting) File {
	var cmd strings.Builder
	cmd.WriteString("/usr/bin/podman pod create --replace --name " + Escape(t.svc))
	for _, p := range s.Ports {
		fmt.Fprintf(&cmd, " -p %d:%d/%s", p.HostPort, p.ContainerPort, strings.ToLower(p.Protocol))
	}
	content := fmt.Sprintf(`[Unit]
Description=%[1]s pod

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=%[2]s
ExecStop=/usr/bin/podman pod rm -f %[1]s
`, Escape(t.svc), cmd.String())
	return File{Name: t.svc + "-pod.service", Dir: DirSystemd, Content: []byte(content)}
}

func (t *translator) containerUnit(c map[string]any, restart string) File {
	cname, _ := c["name"].(string)
	full := t.svc + "-" + cname
	if cname == "pod" {
		t.fail("容器名不能叫 pod：%s-pod.container 產生的 %s-pod.service 會和 pod 服務撞名", t.svc, t.svc)
	}
	podSvc := t.svc + "-pod.service"
	var u strings.Builder
	line := func(k, v string) { u.WriteString(k + "=" + v + "\n") }

	u.WriteString("[Unit]\n")
	line("Description", Escape(full))
	line("Requires", podSvc)
	line("After", podSvc)
	u.WriteString("\n[Container]\n")
	line("ContainerName", Escape(full))
	img, _ := c["image"].(string)
	line("Image", Escape(img))
	if pp, ok := c["imagePullPolicy"].(string); ok {
		switch pp {
		case "IfNotPresent":
			line("Pull", "missing")
		case "Always":
			line("Pull", "always")
		case "Never":
			line("Pull", "never")
		default:
			t.fail("容器 %s：不認得的 imagePullPolicy %q", cname, pp)
		}
	}
	line("PodmanArgs", "--pod="+Escape(t.svc))
	if sc, ok := c["securityContext"].(map[string]any); ok {
		if p, _ := sc["privileged"].(bool); p {
			line("PodmanArgs", "--privileged")
		}
	}

	command := strs(c["command"])
	args := strs(c["args"])
	t.checkVarRefs(cname, "command", command)
	t.checkVarRefs(cname, "args", args)
	t.checkWords(cname, "command", command, false)
	t.checkWords(cname, "args", args, true)
	if len(command) > 0 {
		// podman 4.9.3 的 Quadlet 沒有 Entrypoint= 鍵；k8s 的 command 對應 --entrypoint，
		// 用 JSON 陣列形式才能帶多個字。
		var buf strings.Builder
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(command)
		line("PodmanArgs", words([]string{"--entrypoint=" + strings.TrimSuffix(buf.String(), "\n")}))
	}
	if len(args) > 0 {
		line("Exec", words(args))
	}

	envs, _ := c["env"].([]any)
	for _, e := range envs {
		em, _ := e.(map[string]any)
		name, _ := em["name"].(string)
		if vf, ok := em["valueFrom"].(map[string]any); ok {
			ref, _ := vf["secretKeyRef"].(map[string]any)
			sn, _ := ref["name"].(string)
			key, _ := ref["key"].(string)
			if data, ok := t.secrets[sn]; !ok {
				t.fail("容器 %s 的環境變數 %s 引用的 Secret %s 不在渲染產物裡", cname, name, sn)
			} else if _, ok := data[key]; !ok {
				t.fail("容器 %s 的環境變數 %s 引用的 Secret %s 沒有鍵 %s", cname, name, sn, key)
			}
			line("Secret", Escape(PodmanSecretName(t.svc, sn, key)+",type=env,target="+name))
			continue
		}
		val, _ := em["value"].(string)
		t.checkVarRefs(cname, "env "+name, []string{val})
		line("Environment", quote(Escape(name+"="+val)))
	}

	mounts, _ := c["volumeMounts"].([]any)
	for _, m := range mounts {
		mm, _ := m.(map[string]any)
		vname, _ := mm["name"].(string)
		mpath, _ := mm["mountPath"].(string)
		sub, _ := mm["subPath"].(string)
		v, ok := t.volumes[vname]
		if !ok {
			t.fail("容器 %s 掛載的卷 %s 沒有宣告", cname, vname)
			continue
		}
		if sub != "" && v.configMap == "" && v.secret == "" {
			t.fail("容器 %s 掛載卷 %s：subPath 只支援 configMap 與 secret 卷", cname, vname)
			continue
		}
		switch {
		case v.pvc != "":
			line("Volume", Escape(v.pvc+".volume:"+mpath))
		case v.configMap != "":
			src := ConfigDir(t.svc, v.configMap)
			if sub != "" {
				if _, ok := t.configMaps[v.configMap][sub]; !ok {
					t.fail("容器 %s 掛載卷 %s：ConfigMap %s 沒有鍵 %s", cname, vname, v.configMap, sub)
				}
				src = path.Join(src, sub)
			}
			line("Volume", Escape(src+":"+mpath+":ro"))
		case v.secret != "":
			keys := sortedKeys(t.secrets[v.secret])
			if sub != "" {
				if _, ok := t.secrets[v.secret][sub]; !ok {
					t.fail("容器 %s 掛載卷 %s：Secret %s 沒有鍵 %s", cname, vname, v.secret, sub)
				}
				line("Secret", Escape(PodmanSecretName(t.svc, v.secret, sub)+",type=mount,target="+mpath))
				continue
			}
			// podman 把每個鍵各自掛成一個檔；k8s 是把整個目錄換掉，映像在該目錄的其他檔這裡會留著。
			for _, k := range keys {
				line("Secret", Escape(PodmanSecretName(t.svc, v.secret, k)+",type=mount,target="+path.Join(mpath, k)))
			}
		case v.memory:
			v.users++
			opt := ""
			if v.sizeLimit != "" {
				n, err := bytesOf(v.sizeLimit)
				if err != nil {
					t.fail("卷 %s：%v", vname, err)
				}
				opt = ":size=" + strconv.FormatInt(n, 10)
			}
			line("Tmpfs", Escape(mpath+opt))
		case v.emptyDir:
			v.users++
			// 只寫容器路徑：匿名卷，Quadlet 的 --rm 會在容器刪除時一起刪，近似 emptyDir 的生命週期。
			line("Volume", Escape(mpath))
		}
	}

	u.WriteString("\n[Service]\n")
	line("Restart", restart)
	u.WriteString("\n[Install]\n")
	line("WantedBy", "multi-user.target")
	return File{Name: full + ".container", Dir: DirQuadlet, Content: []byte(u.String())}
}

// checkWords 擋 Quadlet 會弄壞的字（podman 4.9.3 實測）：空字串會讓 Quadlet 截斷整行或其後的字，
// 而且不報錯；單獨一個 ; 解引號後裸寫進 ExecStart，systemd 當成命令分隔。
// command 整個包在 --entrypoint 的 JSON 裡是一個字，; 不受影響，只擋空字串。
func (t *translator) checkWords(cname, field string, vals []string, semicolon bool) {
	for i, v := range vals {
		if v == "" {
			t.fail("容器 %s 的 %s 第 %d 個元素是空字串，Quadlet 會靜默截斷", cname, field, i+1)
		}
		if semicolon && v == ";" {
			t.fail("容器 %s 的 %s 第 %d 個元素是單獨的 ;，systemd 會當成命令分隔", cname, field, i+1)
		}
	}
}

// checkVarRefs 擋 k8s 的 $(VAR) 展開與 $$ 跳脫：podman 不做這兩件事，照搬會改變指令的意思。
// $(jq -r ...) 這類不是合法變數名的寫法，k8s 原樣保留，這裡也放行。
func (t *translator) checkVarRefs(cname, field string, vals []string) {
	for _, v := range vals {
		for _, ref := range k8sVarRef.FindAllString(v, -1) {
			t.fail("容器 %s 的 %s 有 k8s 變數展開 %s，podman 不支援", cname, field, ref)
		}
		if strings.Contains(v, "$$") {
			t.fail("容器 %s 的 %s 有 $$：k8s 會把它縮成 $，podman 不會，請改寫", cname, field)
		}
	}
}

func bytesOf(q string) (int64, error) {
	m := quantity.FindStringSubmatch(q)
	if m == nil {
		return 0, fmt.Errorf("sizeLimit %q 的格式不支援，只收整數加 Ki/Mi/Gi/Ti/k/M/G/T", q)
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, err
	}
	return n * sizeUnits[m[2]], nil
}

func strs(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, x := range list {
		switch s := x.(type) {
		case string:
			out = append(out, s)
		case float64:
			out = append(out, strconv.FormatFloat(s, 'f', -1, 64))
		default:
			out = append(out, fmt.Sprint(s))
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

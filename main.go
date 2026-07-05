package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"
)

var configPath = filepath.Join(".", "xray_config.json")

func cleanupConfig() {
	os.Remove(configPath)
}

func fatalf(format string, args ...interface{}) {
	cleanupConfig()
	log.Fatalf(format, args...)
}

func fatal(args ...interface{}) {
	cleanupConfig()
	log.Fatal(args...)
}

func maskCredentials(s string) string {
	if len(s) <= 8 {
		return s[:2] + "..." + s[len(s)-2:]
	}
	return s[:4] + "..." + s[len(s)-4:]
}

type logLevel int

const (
	LevelDebug logLevel = iota
	LevelInfo
	LevelWarn
	LevelError
)

type levelFilter struct {
	console io.Writer
	file    io.Writer
	level   logLevel
}

func (f *levelFilter) Write(p []byte) (n int, err error) {
	f.console.Write(p)

	msg := string(p)
	msgLower := strings.ToLower(msg)
	msgLevel := LevelInfo

	if strings.Contains(msg, "❌") || strings.Contains(msgLower, "[fatal]") || strings.Contains(msgLower, "[error]") {
		msgLevel = LevelError
	} else if strings.Contains(msg, "⚠") || strings.Contains(msgLower, "[warning]") {
		msgLevel = LevelWarn
	} else if strings.Contains(msgLower, "[debug]") {
		msgLevel = LevelDebug
	}

	if msgLevel >= f.level {
		return f.file.Write(p)
	}
	return len(p), nil
}

func initLogging() func() {
	if os.Getenv("LOG_ENABLED") != "true" {
		return func() {}
	}

	logFile := os.Getenv("LOG_FILE")
	if logFile == "" {
		logFile = "xray-runner.log"
	}

	levelStr := strings.ToLower(os.Getenv("LOG_LEVEL"))
	level := LevelInfo
	switch levelStr {
	case "debug":
		level = LevelDebug
	case "info":
		level = LevelInfo
	case "warn", "warning":
		level = LevelWarn
	case "error":
		level = LevelError
	}

	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("⚠️ Не удалось открыть файл лога: %v", err)
		return func() {}
	}

	origStdout := os.Stdout
	origStderr := os.Stderr

	rOut, wOut, err := os.Pipe()
	if err != nil {
		log.Printf("?? �� ������� ������� pipe ��� stdout: %v", err)
		return func() {}
	}
	os.Stdout = wOut
	go func() {
		io.Copy(io.MultiWriter(origStdout, f), rOut)
	}()

	rErr, wErr, err := os.Pipe()
	if err != nil {
		log.Printf("?? �� ������� ������� pipe ��� stderr: %v", err)
		return func() {}
	}
	os.Stderr = wErr
	lf := &levelFilter{console: origStderr, file: f, level: level}
	go func() {
		io.Copy(lf, rErr)
	}()

	return func() {
		wOut.Close()
		wErr.Close()
		f.Close()
		os.Stdout = origStdout
		os.Stderr = origStderr
	}
}

func main() {
	// 1. Загружаем VLESS-ссылку из .env
	if err := godotenv.Load(); err != nil {
		fatal("❌ Ошибка загрузки .env файла. Убедись, что он существует.")
	}

	defer initLogging()()

	defer func() {
		if r := recover(); r != nil {
			cleanupConfig()
			panic(r)
		}
	}()

	vlessURL := os.Getenv("VLESS_URL")
	if vlessURL == "" {
		fatal("❌ Переменная VLESS_URL не найдена в .env")
	}

	// 2. Парсинг URI
	u, err := url.Parse(vlessURL)
	if err != nil {
		fatalf("❌ Некорректная ссылка: %v", err)
	}
	printURLDetails(u)

	// 3. DNS-резолв сервера
	resolveServer(hostFromURL(u))

	// 4. Генерируем конфиг Xray в зависимости от протокола
	var config map[string]interface{}
	switch u.Scheme {
	case "vless":
		host, portStr, err := net.SplitHostPort(u.Host)
		if err != nil {
			fatalf("❌ Не удалось разделить host и port: %v", err)
		}
		config = buildVlessConfig(host, portStr, u.User.Username(), u.Query())
	case "ss":
		config = buildSSConfig(u)
	default:
		fatalf("❌ Неподдерживаемый протокол: %s (vless, ss)", u.Scheme)
	}

	// 5. Сохраняем во временный JSON
	file, err := os.Create(configPath)
	if err != nil {
		fatalf("❌ Не удалось создать файл конфига: %v", err)
	}

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(config); err != nil {
		fatalf("❌ Ошибка сериализации JSON: %v", err)
	}
	file.Close()

	fmt.Println("✅ Конфиг сгенерирован -> xray_config.json")
	printConfigSummary(config)
	fmt.Println("── Routing ──────────────────────────────")
	printRoutingRules(config)

	// 6. Сохраняем состояние системного прокси, чтобы восстановить при выходе
	oldProxy := readProxyState()
	defer func() {
		if oldProxy.valid {
			writeProxyState(oldProxy)
		}
		os.Remove(configPath)
	}()

	fmt.Println("🚀 Запуск ядра Xray...")

	// 7. Умный поиск бинарника
	binaryName := "xray"
	if runtime.GOOS == "windows" {
		binaryName = "xray.exe"
	}

	exePath, err := os.Executable()
	var binaryPath string
	if err == nil {
		exeDir := filepath.Dir(exePath)
		binaryPath = filepath.Join(exeDir, binaryName)
		fmt.Printf("  🔍 %s ", binaryPath)
		if _, statErr := os.Stat(binaryPath); os.IsNotExist(statErr) {
			fmt.Println("❌ не найден")
			binaryPath = filepath.Join(".", binaryName)
			fmt.Printf("  🔍 %s ", binaryPath)
			if _, statErr2 := os.Stat(binaryPath); os.IsNotExist(statErr2) {
				fmt.Println("❌ не найден")
			} else {
				fmt.Println("✅ найден")
			}
		} else {
			fmt.Println("✅ найден")
		}
	} else {
		binaryPath = filepath.Join(".", binaryName)
		fmt.Printf("  🔍 %s ", binaryPath)
		if _, statErr := os.Stat(binaryPath); os.IsNotExist(statErr) {
			fmt.Println("❌ не найден")
		} else {
			fmt.Println("✅ найден")
		}
	}

	// 8. Запускаем Xray
	cmd := exec.Command(binaryPath, "run", "-c", configPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		fatalf("❌ Не удалось запустить Xray (путь: %s). Ошибка: %v", binaryPath, err)
	}

	fmt.Printf("🟢 Xray запущен с PID: %d\n", cmd.Process.Pid)

	fmt.Println("── Диагностика ───────────────────────────")
	socksPort, httpPort := checkPorts(config)
	fmt.Printf("  Порты: SOCKS5 127.0.0.1:%d  HTTP 127.0.0.1:%d\n", socksPort, httpPort)

	// Мониторинг процесса Xray (горутина)
	go monitorXray(cmd)

	// Ожидание появления портов
	fmt.Print("  ⏳ Ожидание портов Xray... ")
	if waitForPort(socksPort, "SOCKS5", 10*time.Second) {
		fmt.Println("✅ SOCKS5 доступен")
	} else {
		fmt.Println("❌ SOCKS5 не отвечает за 10с")
	}
	if waitForPort(httpPort, "HTTP", 5*time.Second) {
		fmt.Println("  ✅ HTTP-прокси доступен")
	} else {
		fmt.Println("  ❌ HTTP-прокси не отвечает")
	}

	// Тестовый запрос через прокси
	testProxyConnection(httpPort)

	// 9. Включаем системный HTTP-прокси
	enableHTTPProxy(httpPort)
	verifySystemProxy(httpPort)
	fmt.Println("──────────────────────────────────────────")

	// 10. Graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt)
	<-sigChan

	fmt.Println("\n🛑 Останавливаем Xray...")
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		_ = cmd.Process.Kill()
	}
	time.Sleep(500 * time.Millisecond)

	fmt.Println("👋 До встречи!")
}

func maskIfNeeded(s string) string {
	if os.Getenv("MASK_CREDENTIALS") == "false" {
		return s
	}
	return maskCredentials(s)
}

func printURLDetails(u *url.URL) {
	fmt.Println("── URL ──────────────────────────────────")
	fmt.Printf("  Протокол:  %s\n", u.Scheme)
	host, port, _ := net.SplitHostPort(u.Host)
	fmt.Printf("  Сервер:    %s\n", host)
	fmt.Printf("  Порт:      %s\n", port)
	if u.Scheme == "vless" {
		fmt.Printf("  UUID:      %s\n", maskIfNeeded(u.User.Username()))
		q := u.Query()
		if v := q.Get("type"); v != "" {
			fmt.Printf("  Transport: %s\n", v)
		}
		if v := q.Get("security"); v != "" {
			fmt.Printf("  Security:  %s\n", v)
		}
		if v := q.Get("sni"); v != "" {
			fmt.Printf("  SNI:       %s\n", v)
		}
		if v := q.Get("fp"); v != "" {
			fmt.Printf("  Fingerprint: %s\n", v)
		}
		if v := q.Get("pbk"); v != "" {
			fmt.Printf("  PublicKey: %s\n", maskIfNeeded(v))
		}
		if v := q.Get("sid"); v != "" {
			fmt.Printf("  ShortID:   %s\n", maskIfNeeded(v))
		}
		if v := q.Get("flow"); v != "" {
			fmt.Printf("  Flow:      %s\n", v)
		}
		if v := q.Get("host"); v != "" {
			fmt.Printf("  Host:      %s\n", v)
		}
		if v := q.Get("path"); v != "" {
			fmt.Printf("  Path:      %s\n", v)
		}
		if v := q.Get("alpn"); v != "" {
			fmt.Printf("  ALPN:      %s\n", v)
		}
	}
	fmt.Println("─────────────────────────────────────────")
}

func hostFromURL(u *url.URL) string {
	host, _, err := net.SplitHostPort(u.Host)
	if err != nil {
		return u.Host
	}
	return host
}

func resolveServer(host string) {
	fmt.Print("🔍 DNS-резолв сервера... ")
	addrs, err := net.LookupHost(host)
	if err != nil {
		fmt.Printf("❌ ОШИБКА: %v\n", err)
		return
	}
	if len(addrs) == 0 {
		fmt.Println("❌ Нет записей A/AAAA")
		return
	}
	fmt.Printf("✅ %s → %s\n", host, strings.Join(addrs, ", "))
}

func readTemplate() map[string]interface{} {
	templatePath := filepath.Join(".", "template.json")
	templateBytes, err := os.ReadFile(templatePath)
	if err != nil {
		fatalf("❌ Ошибка чтения template.json: %v", err)
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal(templateBytes, &cfg); err != nil {
		fatalf("❌ Ошибка парсинга template.json: %v", err)
	}
	return cfg
}

func mergeConfig(tc map[string]interface{}, proxyOutbound map[string]interface{}) map[string]interface{} {
	var finalOutbounds []interface{}
	if to, ok := tc["outbounds"].([]interface{}); ok {
		finalOutbounds = append(finalOutbounds, proxyOutbound)
		finalOutbounds = append(finalOutbounds, to...)
	} else {
		finalOutbounds = []interface{}{
			proxyOutbound,
			map[string]interface{}{"tag": "direct", "protocol": "freedom"},
			map[string]interface{}{"tag": "block", "protocol": "blackhole"},
		}
	}

	if routing, ok := tc["routing"].(map[string]interface{}); ok {
		if rules, ok := routing["rules"].([]interface{}); ok {
			routing["rules"] = append(rules, map[string]interface{}{
				"type": "field", "outboundTag": "proxy", "network": "tcp,udp",
			})
		}
	}

	xrayLevel := os.Getenv("XRAY_LOG_LEVEL")
	if xrayLevel == "" {
		xrayLevel = "warning"
	}

	return map[string]interface{}{
		"log":       map[string]interface{}{"loglevel": xrayLevel},
		"dns":       tc["dns"],
		"inbounds":  tc["inbounds"],
		"outbounds": finalOutbounds,
		"routing":   tc["routing"],
	}
}

func buildVlessConfig(host, portStr, uuid string, q url.Values) map[string]interface{} {
	tc := readTemplate()
	port, err := strconv.Atoi(portStr)
	if err != nil {
		fatalf("❌ Некорректный порт: %s", portStr)
	}

	network := q.Get("type")
	if network == "" {
		network = "tcp"
	}
	ss := map[string]interface{}{"network": network}

	switch network {
	case "ws":
		ws := map[string]interface{}{}
		if p := q.Get("path"); p != "" {
			ws["path"] = p
		}
		if h := q.Get("host"); h != "" {
			ws["headers"] = map[string]interface{}{"Host": h}
		}
		if len(ws) > 0 {
			ss["wsSettings"] = ws
		}
	case "grpc":
		grpc := map[string]interface{}{}
		if svc := q.Get("serviceName"); svc != "" {
			grpc["serviceName"] = svc
		}
		if q.Get("mode") == "multi" {
			grpc["multiMode"] = true
		}
		if auth := q.Get("authority"); auth != "" {
			grpc["authority"] = auth
		}
		if len(grpc) > 0 {
			ss["grpcSettings"] = grpc
		}
	}

	security := q.Get("security")
	switch security {
	case "reality":
		ss["security"] = "reality"
		rs := map[string]interface{}{
			"serverName": q.Get("sni"),
			"publicKey":  q.Get("pbk"),
			"shortId":    q.Get("sid"),
		}
		if fp := q.Get("fp"); fp != "" {
			rs["fingerprint"] = fp
		}
		ss["realitySettings"] = rs
	case "tls":
		ss["security"] = "tls"
		tls := map[string]interface{}{}
		sni := q.Get("sni")
		if sni == "" {
			sni = q.Get("host")
		}
		if sni != "" {
			tls["serverName"] = sni
		}
		if fp := q.Get("fp"); fp != "" {
			tls["fingerprint"] = fp
		}
		if alpn := q.Get("alpn"); alpn != "" {
			tls["alpn"] = strings.Split(alpn, ",")
		}
		if len(tls) > 0 {
			ss["tlsSettings"] = tls
		}
	}

	us := map[string]interface{}{"id": uuid, "encryption": "none"}
	if flow := q.Get("flow"); flow != "" {
		us["flow"] = flow
	}

	proxyOutbound := map[string]interface{}{
		"tag":      "proxy",
		"protocol": "vless",
		"settings": map[string]interface{}{
			"vnext": []map[string]interface{}{
				{"address": host, "port": port, "users": []map[string]interface{}{us}},
			},
		},
		"streamSettings": ss,
	}

	return mergeConfig(tc, proxyOutbound)
}

func buildSSConfig(u *url.URL) map[string]interface{} {
	tc := readTemplate()

	host, portStr, err := net.SplitHostPort(u.Host)
	if err != nil {
		fatalf("❌ Не удалось разделить host и port в ss://: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		fatalf("❌ Некорректный порт в ss://: %s", portStr)
	}

	b64 := u.User.Username()
	if m := len(b64) % 4; m != 0 {
		b64 += strings.Repeat("=", 4-m)
	}
	decoded, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		fatalf("❌ Не удалось декодировать метод:пароль в ss://: %v", err)
	}
	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 {
		fatalf("❌ Некорректный формат метод:пароль в ss://: %s", string(decoded))
	}

	proxyOutbound := map[string]interface{}{
		"tag":      "proxy",
		"protocol": "shadowsocks",
		"settings": map[string]interface{}{
			"servers": []map[string]interface{}{
				{"address": host, "port": port, "method": parts[0], "password": parts[1], "level": 0},
			},
		},
	}

	return mergeConfig(tc, proxyOutbound)
}

func printConfigSummary(config map[string]interface{}) {
	if outbounds, ok := config["outbounds"].([]interface{}); ok && len(outbounds) > 0 {
		if proxy, ok := outbounds[0].(map[string]interface{}); ok {
			proto, _ := proxy["protocol"].(string)
			ss, _ := proxy["streamSettings"].(map[string]interface{})
			fmt.Printf("📡 Outbound: %s", proto)
			if ss != nil {
				if net, ok := ss["network"].(string); ok {
					fmt.Printf(" | transport: %s", net)
				}
				if sec, ok := ss["security"].(string); ok {
					fmt.Printf(" | security: %s", sec)
				}
			}
			if settings, ok := proxy["settings"].(map[string]interface{}); ok {
				if proto == "vless" {
					if vnext, ok := settings["vnext"].([]interface{}); ok && len(vnext) > 0 {
						if first, ok := vnext[0].(map[string]interface{}); ok {
							addr, _ := first["address"].(string)
							port, _ := first["port"].(float64)
							fmt.Printf(" | server: %s:%.0f", addr, port)
						}
					}
				}
				if proto == "shadowsocks" {
					if servers, ok := settings["servers"].([]interface{}); ok && len(servers) > 0 {
						if first, ok := servers[0].(map[string]interface{}); ok {
							addr, _ := first["address"].(string)
							port, _ := first["port"].(float64)
							method, _ := first["method"].(string)
							fmt.Printf(" | server: %s:%.0f | method: %s", addr, port, method)
						}
					}
				}
			}
			fmt.Println()
		}
	}
}

func printRoutingRules(config map[string]interface{}) {
	if routing, ok := config["routing"].(map[string]interface{}); ok {
		if rules, ok := routing["rules"].([]interface{}); ok {
			fmt.Printf("  Маршрутов: %d\n", len(rules))
			for i, r := range rules {
				if rule, ok := r.(map[string]interface{}); ok {
					tag, _ := rule["outboundTag"].(string)
					domains, _ := rule["domain"].([]interface{})
					ips, _ := rule["ip"].([]interface{})
					network, _ := rule["network"].(string)
					var parts []string
					if len(domains) > 0 {
						parts = append(parts, fmt.Sprintf("%d доменов", len(domains)))
					}
					if len(ips) > 0 {
						parts = append(parts, fmt.Sprintf("%d IP-сетей", len(ips)))
					}
					if network != "" {
						parts = append(parts, network)
					}
					desc := strings.Join(parts, ", ")
					if desc == "" {
						desc = "catch-all"
					}
					fmt.Printf("    %d. → %-7s  %s\n", i+1, tag, desc)
				}
			}
		}
	}
}

func waitForPort(port int, label string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

func checkPorts(config map[string]interface{}) (socksPort, httpPort int) {
	socksPort = 10808
	httpPort = 10809
	if inbounds, ok := config["inbounds"].([]interface{}); ok && len(inbounds) > 0 {
		if first, ok := inbounds[0].(map[string]interface{}); ok {
			if p, ok := first["port"].(float64); ok {
				socksPort = int(p)
			}
		}
		if len(inbounds) > 1 {
			if second, ok := inbounds[1].(map[string]interface{}); ok {
				if p, ok := second["port"].(float64); ok {
					httpPort = int(p)
				}
			}
		}
	}
	return
}

func monitorXray(cmd *exec.Cmd) {
	err := cmd.Wait()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
				log.Printf("⚠️ Xray завершился с кодом %d (неожиданно!)", status.ExitStatus())
			} else {
				log.Printf("⚠️ Xray завершился с ошибкой: %v", err)
			}
		}
	}
}

func testProxyConnection(httpPort int) {
	proxyURL := fmt.Sprintf("http://127.0.0.1:%d", httpPort)
	fmt.Printf("  🌐 Тестовый запрос через HTTP-прокси %s... ", proxyURL)

	transport := &http.Transport{
		Proxy: func(req *http.Request) (*url.URL, error) {
			return url.Parse(proxyURL)
		},
		DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
	}
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}

	resp, err := client.Get("https://www.google.com/generate_204")
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		return
	}
	resp.Body.Close()

	if resp.StatusCode == 204 || resp.StatusCode == 200 {
		fmt.Printf("✅ (HTTP %d)\n", resp.StatusCode)
	} else {
		fmt.Printf("⚠️  (HTTP %d)\n", resp.StatusCode)
	}
}

func verifySystemProxy(httpPort int) {
	enabled := readProxyState()
	if !enabled.enabled {
		fmt.Println("  ⚠️ Системный прокси НЕ включён (реестр не подтвердил)")
		return
	}
	expected := fmt.Sprintf("127.0.0.1:%d", httpPort)
	actual := enabled.server
	if actual != expected {
		fmt.Printf("  ⚠️ Системный прокси: %s (ожидалось %s)\n", actual, expected)
		return
	}
	fmt.Printf("  ✅ Системный прокси 127.0.0.1:%d подтверждён\n", httpPort)
}

const regKey = `HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings`

type proxyState struct {
	enabled   bool
	server    string
	overrides string
	valid     bool
}

func readProxyState() proxyState {
	var s proxyState

	out, err := exec.Command("reg", "query", regKey, "/v", "ProxyEnable").Output()
	if err != nil {
		return s
	}
	s.valid = true
	s.enabled = strings.Contains(string(out), "0x1")

	s.server = queryRegString("ProxyServer")
	s.overrides = queryRegString("ProxyOverride")

	return s
}

func queryRegString(name string) string {
	out, err := exec.Command("reg", "query", regKey, "/v", name).Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "REG_SZ") {
			parts := strings.SplitN(line, "REG_SZ", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1])
			}
		}
	}
	return ""
}

func writeProxyState(s proxyState) {
	if s.enabled {
		execReg("add", regKey, "/v", "ProxyEnable", "/t", "REG_DWORD", "/d", "1", "/f")
		if s.server != "" {
			execReg("add", regKey, "/v", "ProxyServer", "/t", "REG_SZ", "/d", s.server, "/f")
		}
		if s.overrides != "" {
			execReg("add", regKey, "/v", "ProxyOverride", "/t", "REG_SZ", "/d", s.overrides, "/f")
		}
	} else {
		execReg("add", regKey, "/v", "ProxyEnable", "/t", "REG_DWORD", "/d", "0", "/f")
	}
}

func execReg(args ...string) error {
	if out, err := exec.Command("reg", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("reg %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return nil
}

func enableHTTPProxy(httpPort int) {
	overrides := queryRegString("ProxyOverride")
	if overrides == "" {
		overrides = "<-loopback>"
	} else if !strings.Contains(overrides, "<-loopback>") {
		overrides += ";-loopback>"
	}

	if err := execReg("add", regKey, "/v", "ProxyEnable", "/t", "REG_DWORD", "/d", "1", "/f"); err != nil {
		log.Printf("⚠ Не удалось включить прокси: %v", err)
		return
	}
	execReg("add", regKey, "/v", "ProxyServer", "/t", "REG_SZ", "/d", fmt.Sprintf("127.0.0.1:%d", httpPort), "/f")
	execReg("add", regKey, "/v", "ProxyOverride", "/t", "REG_SZ", "/d", overrides, "/f")
	fmt.Println("✅ Системный прокси включён (127.0.0.1:" + strconv.Itoa(httpPort) + ")")
}

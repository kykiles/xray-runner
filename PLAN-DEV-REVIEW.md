# Проверка внешнего ревью `dev` и план исправлений

Ревью другой модели: `xray-runner-dev-code-review.md`, коммит `6637eac` (ветка `dev` после сбора исправлений H10). Здесь проверено, верны ли её находки. Её предложения по исправлению не использовались; план ниже составлен заново.

## Итог

Все 7 находок подтверждаются кодом. Для пяти из них (1, 2, 3, 5, 6) это показали временные тесты на `6637eac`; их удалили, в коммит они не вошли. Находки 4 и 7 подтверждены чтением кода. Ошибок в описаниях нет. Есть два преувеличения (в находках 1 и 4) и три места, где проблема шире, чем написано (находки 2, 5 и 6).

| № | Находка | Вердикт | Приоритет: их → мой | Как проверено |
|---|---|---|---|---|
| 1 | Правило проверки обходит выбор процессов в Windows SPLIT | верно | P1 → **P2** | тест: первое правило — `full:www.google.com → balancer` без `process` и `inboundTag` |
| 2 | Второй `freedom` принимается за туннель | верно, **и хуже** | P1 → **P1** | тест: выбранные программы уходят целиком в `bypass` (напрямую); со вторым `blackhole` — в `adblock` (связи нет) |
| 3 | Проверка связи целится в первый outbound | верно | P1 → **P1/P2** | тест: при `freedom` первым проверочное правило идёт в `direct` |
| 4 | Служба не видит гео-базы подписки | верно | P2 → **P2** | чтение: `service/run.go`, `servetun.go`, `service/install.go` |
| 5 | Несколько балансировщиков превращаются в последний | верно, **и шире** | P2 → **P2** | тест: US и DE превращаются в одно правило `process → DE` без доменов |
| 6 | Обход VPN определяется по имени тега | верно, **и шире** | P2 → **P3** | тест: `HasBypassRules` = false для `bypass` (freedom) |
| 7 | Неудачный откат прокси не повторяется | верно | P2 → **P2/P3** | чтение: `session.go` (`restoreSystemProxy`), `system/proxy*.go` |

### Уточнения по находкам

1. **Правило проверки в Windows SPLIT.** Цепочка верна. В TUN включён sniffing (`routeOnly`, `http/tls/quic`), так что домен из SNI попадает под правило. Но это не утечка мимо VPN, а наоборот: в VPN уходит лишний трафик невыбранных программ, и только к трём хостам из `HEALTH_CHECK_URL`. Правда, `www.google.com` среди них самый посещаемый. Ревью не упоминает, что то же правило действует и в обычном TUN: для этих хостов оно перебивает прямые правила панели у всех программ. Проверка в TUN и так ходит через свой вход `probe` (A10), поэтому правило можно ограничить им.
2. **Второй `freedom`.** Ревью описывает только уход в `bypass`, то есть напрямую, с настоящим адресом. Это утечка для выбранных программ. Но тот же дефект с blackhole хуже: если последним туннельным по мнению кода правилом окажется `adblock` (второй `blackhole`), выбранные программы отправляются в blackhole целиком и теряют связь.
3. **Первый outbound.** Касается только целого профиля без балансировщика (`MergeProfile`). В `MergeProfileSingle` выбранный сервер ставится первым, так что там проблемы нет. Ревью не упоминает замер пинга профиля (`buildProfileBenchConfig`): у него та же ошибка. Последствие описано верно: ложное «ок» и нет перезапуска после трёх неудачных проверок. Но панели почти всегда ставят прокси первым, поэтому вероятность низкая.
4. **Гео-базы службы.** Верно: служба вызывает `SetGeoAssets(<папка своего ядра>)` и сама проверяет конфиг (`finalizeConfig` → `CheckGeoLists`). `XRAY_LOCATION_ASSET` она не задаёт, а `service install` копирует только базы рядом с ядром. Преувеличено, что причина «маскируется»: `CheckGeoLists` называет папку и недостающие списки. Но ошибка не объясняет, почему в PROXY всё работает и что делать. Это регрессия H10: раньше `sudo` + TUN работал на базах панели, а теперь при установленной службе программа сама идёт через неё.
5. **Балансировщики.** Кроме потери условий есть второй дефект того же корня: меняется порядок правил. Локальные правила оказываются раньше туннельных. Например, `yandex.ru → proxy` стояло перед `geosite:ru → direct`. После преобразования `yandex.ru` у выбранной программы идёт напрямую.
6. **Статус TUN.** Не видит и два других случая. Первый: первый outbound — `freedom`, и всё, что не попало ни под одно правило, идёт напрямую. Второй: `blackhole` с нестандартным тегом (`reject`, `adblock`). Последствие — только надпись на экране, поэтому P3.
7. **Откат прокси.** Верно. Есть смягчения: на Windows `forceDisable` выключает наш прокси, а следующий запуск (`warnDeadLoopbackProxy`) снимает наш адрес на мёртвом порту. На Linux запасного пути нет (`forceDisable` пустой), и исходная настройка пользователя (PAC, свой прокси) теряется. Ревью не упоминает ловушку при исправлении: если повтор отложен до следующей сессии, её `bringUpProxy` не должна снимать снимок заново. Иначе «исходной» станет наша же настройка.

## Сравнение с нашим ревью H10 и исправлениями

- `REVIEW-H10.md` проверял только службу и IPC (ветку `claude/h10-service`). Внешнее ревью шире: маршрутизация TUN/SPLIT, проверка связи, статус, откат прокси. Пересекается с нашим только находка 4. Наша находка 18 говорила, что базы службы застывают на момент установки. Исправление `71e3880` добавило в README только совет повторить `service install` после `u`. Гео-базы панели, которые лежат в кэше пользователя, службе недоступны в принципе, и этот случай мы пропустили.
- Находки 1, 2, 3, 5 и 6 не вызваны исправлениями H10. Этот код старше клона (стадии ADR-0002/0003, A03, A10). Находка 7 появилась в `d850e46d` от 20.09, тоже до H10. Находка 4 заложена в дизайн службы (`186fae7`).
- Исправления H10 эти места не трогают. С планом ниже не конфликтуют: все шаги проверены поверх `6637eac`.
- Ревью пишет, что `go test` не запускался. Я запускал: на `6637eac` `go test ./...` зелёный, а все изменения из плана проверены на отдельной копии. Проверки: `go build` и `go vet` для Linux и `GOOS=windows`, golangci-lint v1.64.8 для обеих ОС, `go test ./...`, `go test -race` для `xraycfg`, `app` и `service`. Всё чисто. Код в шагах ниже — ровно тот, что прошёл эти проверки.

---

# План исправлений (для исполнителя)

## Правила

1. Работать в ветке, которую укажет пользователь, от `dev` (`6637eac` или новее).
2. Шаги выполнять **строго по порядку**: шаг 2 использует функции из шага 1, тест шага 3 — поведение шага 1.
3. Один шаг — один коммит. Сообщение коммита — по-русски, в стиле репозитория (`fix: …`), текст дан в каждом шаге.
4. Менять только перечисленные в шаге файлы. Существующие тесты не удалять и не ослаблять. Исключение — две правки ожидаемых значений в шаге 1, они даны дословно.
5. Код переносить **дословно**, вместе с комментариями. Комментарии в коде — по-английски, строки для пользователя — по-русски, как в остальном коде.
6. После каждого шага выполнить проверку шага. Если что-то красное, не подгонять тесты, а сверить свой код с планом.
7. Go: модуль требует go1.26.8. Если локальный `go` старше, он сам скачает toolchain (`GOTOOLCHAIN=auto`), ничего делать не нужно.

Проверка после каждого шага:

```bash
gofmt -l internal/                      # должно быть пусто
go vet ./... && GOOS=windows go vet ./...
go test ./internal/xraycfg/ ./internal/app/ ./internal/service/
```

Линтер (один раз собрать, как в CI: v1.64.8, собранный go1.26.8):

```bash
GOTOOLCHAIN=go1.26.8 GOBIN=/tmp/lint go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.64.8
/tmp/lint/golangci-lint run ./... && GOOS=windows /tmp/lint/golangci-lint run ./...
```

---

## Шаг 1. SPLIT: все локальные outbound-ы и правила панели в их порядке (находки 2 и 5)

**Суть.** Локальными считаются **все** outbound-ы с протоколом `freedom`, `blackhole` или `dns`, а не первый каждого вида. Правила панели остаются на своих местах. Локальные не меняются, туннельные (`balancerTag` или нелокальный `outboundTag`) получают ограничение `process`. После них идёт правило «выбранные процессы → первый outbound», если это сервер (xray отправляет туда всё, что не совпало ни с одним правилом). Последним — прямое правило для всех остальных.

**Что изменится для пользователя, кроме исправления:** профиль, где нет ни одного правила в туннель, но прокси стоит первым (он работает «по умолчанию»), раньше отказывал в SPLIT с `ErrNoTunnelTarget`, а теперь работает: выбранные программы идут в этот прокси.

**Файлы:** `internal/xraycfg/split.go`, `internal/xraycfg/split_test.go`.

### 1.1. `split.go`: импорты

Заменить блок `import (...)` на:

```go
import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)
```

### 1.2. `split.go`: заменить кусок файла

Удалить всё **от** строки `// ErrNoTunnelTarget means the config has nothing…` **до** строки `func ruleOutboundTag(` (её не трогать). В этот кусок входят старые `ErrNoTunnelTarget`, `ApplySplitRouting`, `directOutboundTag`, `localOutbounds` и `isLocalTag`. На его место вставить:

```go
// ErrNoTunnelTarget means the config has nothing the listed processes could be
// sent to: no rule leading into the tunnel, and no server as the first outbound.
var ErrNoTunnelTarget = errors.New("в конфиге не найден outbound туннеля")

// ApplySplitRouting rewrites a finished config so that only the named processes
// travel the tunnel and everything else goes out directly.
//
// The panel's rules stay where they were, in their order. Those leading
// somewhere local — a freedom, blackhole or dns outbound, whatever its tag —
// are kept as they are. Those leading into the tunnel are narrowed to the
// listed processes: left as they were, the panel's catch-all would send the
// whole system through the tunnel no matter which process opened the
// connection. After them the listed processes go where unmatched traffic goes —
// the first outbound — and everything else goes direct. So a listed process
// goes exactly where a TUN session would have sent it, balancer by balancer
// and domain by domain.
func ApplySplitRouting(raw json.RawMessage, processes []string) (json.RawMessage, error) {
	if len(processes) == 0 {
		return nil, errors.New("список процессов пуст")
	}

	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	outbounds, err := readOutbounds(cfg["outbounds"])
	if err != nil {
		return nil, err
	}
	local := map[string]bool{}
	directTag := ""
	for _, o := range outbounds {
		if o.Tag == "" || !isLocalProtocol(o.Protocol) {
			continue
		}
		local[o.Tag] = true
		if o.Protocol == "freedom" && directTag == "" {
			directTag = o.Tag
		}
	}
	// Unmatched traffic takes the first outbound. When that is the tunnel, the
	// listed processes are sent there once the panel's rules are through.
	defaultTag := ""
	if len(outbounds) > 0 && outbounds[0].Tag != "" && !local[outbounds[0].Tag] {
		defaultTag = outbounds[0].Tag
	}
	if directTag == "" {
		// A panel without a freedom outbound has no way out except the tunnel,
		// which is the one thing the mode must not do with unlisted traffic.
		withDirect, err := appendDirectOutbound(cfg["outbounds"])
		if err != nil {
			return nil, err
		}
		cfg["outbounds"] = withDirect
		directTag = directOutboundTag
		local[directTag] = true
	}

	routing := map[string]json.RawMessage{}
	if len(cfg["routing"]) > 0 {
		if err := json.Unmarshal(cfg["routing"], &routing); err != nil {
			return nil, fmt.Errorf("routing: %w", err)
		}
	}
	var rules []map[string]json.RawMessage
	if len(routing["rules"]) > 0 {
		if err := json.Unmarshal(routing["rules"], &rules); err != nil {
			return nil, fmt.Errorf("routing rules: %w", err)
		}
	}

	out := make([]map[string]json.RawMessage, 0, len(rules)+2)
	tunnel := false
	for _, r := range rules {
		if len(r["balancerTag"]) == 0 {
			tag, err := ruleOutboundTag(r)
			if err != nil {
				return nil, err
			}
			if local[tag] {
				out = append(out, r)
				continue
			}
			// A rule with no tag at all rides the default outbound: there is
			// nothing to narrow, and keeping it would emit a rule xray rejects.
			if tag == "" {
				continue
			}
		}
		narrowed, ok, err := forProcesses(r, processes)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		out = append(out, narrowed)
		tunnel = true
	}
	if defaultTag != "" {
		tag, err := json.Marshal(defaultTag)
		if err != nil {
			return nil, err
		}
		rest, err := splitRule(processes, "outboundTag", tag)
		if err != nil {
			return nil, err
		}
		out = append(out, rest)
		tunnel = true
	}
	if !tunnel {
		return nil, ErrNoTunnelTarget
	}
	catchAll, err := catchAllRule(directTag)
	if err != nil {
		return nil, err
	}

	if routing["rules"], err = json.Marshal(append(out, catchAll)); err != nil {
		return nil, fmt.Errorf("routing rules: %w", err)
	}
	if cfg["routing"], err = json.Marshal(routing); err != nil {
		return nil, fmt.Errorf("routing: %w", err)
	}
	return json.Marshal(cfg)
}

// directOutboundTag is the tag of the freedom outbound added to a config that
// has none.
const directOutboundTag = "direct"

// outboundInfo is what the routing rewrites need to know of an outbound.
type outboundInfo struct {
	Tag      string `json:"tag"`
	Protocol string `json:"protocol"`
}

func readOutbounds(raw json.RawMessage) ([]outboundInfo, error) {
	var outbounds []outboundInfo
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &outbounds); err != nil {
			return nil, fmt.Errorf("outbounds: %w", err)
		}
	}
	return outbounds, nil
}

// isLocalProtocol reports whether an outbound of this protocol resolves
// traffic without the tunnel: freedom (direct), blackhole (blocked) and dns
// (answered by xray itself).
func isLocalProtocol(protocol string) bool {
	switch protocol {
	case "freedom", "blackhole", "dns":
		return true
	}
	return false
}

// forProcesses narrows a tunnel rule to the listed processes. A rule that names
// processes of its own keeps those of them that are listed; ok is false when
// none are, and the rule is dropped.
func forProcesses(r map[string]json.RawMessage, processes []string) (map[string]json.RawMessage, bool, error) {
	names := processes
	if len(r["process"]) > 0 {
		own, err := stringList(r["process"])
		if err != nil {
			return nil, false, fmt.Errorf("routing rule process: %w", err)
		}
		names = nil
		for _, p := range own {
			if slices.ContainsFunc(processes, func(l string) bool { return strings.EqualFold(l, strings.TrimSpace(p)) }) {
				names = append(names, strings.TrimSpace(p))
			}
		}
		if len(names) == 0 {
			return nil, false, nil
		}
	}
	procs, err := json.Marshal(names)
	if err != nil {
		return nil, false, err
	}
	narrowed := maps.Clone(r)
	narrowed["process"] = procs
	return narrowed, true, nil
}
```

`ruleOutboundTag`, `appendDirectOutbound`, `splitRule` и `catchAllRule` остаются как были. `stringList` уже есть в `geodata.go` того же пакета.

### 1.3. `split_test.go`: две правки ожидаемого числа правил

Правило «выбранные процессы → первый outbound» добавляется всегда, когда первый outbound — сервер. Поэтому в двух тестах становится на одно правило больше. Больше ничего в старых тестах не менять.

В `TestApplySplitRoutingKeepsLocalRulesAndAimsAtBalancer` заменить фрагмент от `if len(rules) != 4 {` до `last := rules[3]` включительно на:

```go
	if len(rules) != 5 {
		t.Fatalf("got %d rules, want 5: %v", len(rules), rules)
	}
	if rules[0]["outboundTag"] != "block" || rules[1]["outboundTag"] != "direct" {
		t.Errorf("panel's local rules not kept in order: %v", rules[:2])
	}

	// The panel's catch-all follows them, narrowed to the listed process.
	procs, ok := rules[2]["process"].([]any)
	if !ok || len(procs) != 1 || procs[0] != "Telegram.exe" {
		t.Fatalf("process rule = %v, want the listed process", rules[2])
	}
	if rules[2]["balancerTag"] != "balancer" || rules[2]["network"] != "tcp,udp" {
		t.Errorf("process rule = %v, want the panel's catch-all into the balancer", rules[2])
	}
	// Then the listed process's default: the first outbound.
	if rules[3]["outboundTag"] != "proxy-a" || rules[3]["process"] == nil {
		t.Errorf("default rule = %v, want the listed process to proxy-a", rules[3])
	}

	// And everything unmatched — every process outside the list — goes direct.
	last := rules[4]
```

В `TestApplySplitRoutingAimsAtOutboundTag` заменить фрагмент от `if len(rules) != 2 {` до конца функции на:

```go
	if len(rules) != 3 {
		t.Fatalf("got %d rules, want the narrowed rule, the default and the catch-all: %v", len(rules), rules)
	}
	if rules[0]["outboundTag"] != "proxy" || len(rules[0]["process"].([]any)) != 2 {
		t.Errorf("process rule = %v, want both processes routed to proxy", rules[0])
	}
	if rules[1]["outboundTag"] != "proxy" || len(rules[1]["process"].([]any)) != 2 {
		t.Errorf("default rule = %v, want both processes routed to proxy", rules[1])
	}
	if rules[2]["outboundTag"] != "direct" {
		t.Errorf("catch-all = %v, want direct", rules[2])
	}
}
```

### 1.4. `split_test.go`: новые тесты в конец файла

```go
// ruleWith finds the first rule whose key holds value, failing the test when
// there is none.
func ruleWith(t *testing.T, rules []map[string]any, key, value string) map[string]any {
	t.Helper()
	for _, r := range rules {
		if r[key] == value {
			return r
		}
	}
	t.Fatalf("no rule with %s=%s in %v", key, value, rules)
	return nil
}

// A second freedom outbound under its own tag is as local as the first: a rule
// into it is kept for everyone and never becomes where the listed processes go.
func TestApplySplitRoutingSecondFreedomIsLocal(t *testing.T) {
	cfg := `{
      "outbounds": [
        {"tag": "proxy", "protocol": "vless"},
        {"tag": "direct", "protocol": "freedom"},
        {"tag": "bypass", "protocol": "freedom"}
      ],
      "routing": {"rules": [
        {"domain": ["example.com"], "outboundTag": "proxy"},
        {"network": "tcp,udp", "outboundTag": "bypass"}
      ]}
    }`
	raw, err := ApplySplitRouting(json.RawMessage(cfg), []string{"Telegram.exe"})
	if err != nil {
		t.Fatalf("ApplySplitRouting: %v", err)
	}
	rules := rulesOf(t, raw)
	if rules[0]["outboundTag"] != "proxy" || rules[0]["process"] == nil {
		t.Errorf("rule 0 = %v, want example.com to proxy for the listed process", rules[0])
	}
	if rules[1]["outboundTag"] != "bypass" || rules[1]["process"] != nil {
		t.Errorf("rule 1 = %v, want the panel's bypass rule kept as it was", rules[1])
	}
	for _, r := range rules {
		if r["process"] != nil && r["outboundTag"] == "bypass" {
			t.Errorf("listed processes sent to the second freedom outbound: %v", r)
		}
	}
}

// Same for a second blackhole: aiming the listed processes at it would cut them
// off altogether.
func TestApplySplitRoutingSecondBlackholeIsLocal(t *testing.T) {
	cfg := `{
      "outbounds": [
        {"tag": "proxy", "protocol": "vless"},
        {"tag": "direct", "protocol": "freedom"},
        {"tag": "block", "protocol": "blackhole"},
        {"tag": "adblock", "protocol": "blackhole"}
      ],
      "routing": {"rules": [
        {"network": "tcp,udp", "outboundTag": "proxy"},
        {"domain": ["geosite:category-ads"], "outboundTag": "adblock"}
      ]}
    }`
	raw, err := ApplySplitRouting(json.RawMessage(cfg), []string{"Telegram.exe"})
	if err != nil {
		t.Fatalf("ApplySplitRouting: %v", err)
	}
	for _, r := range rulesOf(t, raw) {
		if r["process"] != nil && r["outboundTag"] == "adblock" {
			t.Errorf("listed processes sent to the second blackhole: %v", r)
		}
	}
}

// Two balancers for two sets of domains: each set keeps its balancer, in the
// panel's order, and the panel's direct rule stays behind them.
func TestApplySplitRoutingKeepsEveryBalancerRule(t *testing.T) {
	cfg := `{
      "outbounds": [
        {"tag": "us1", "protocol": "vless"},
        {"tag": "de1", "protocol": "vless"},
        {"tag": "direct", "protocol": "freedom"}
      ],
      "routing": {
        "balancers": [{"tag": "US", "selector": ["us"]}, {"tag": "DE", "selector": ["de"]}],
        "rules": [
          {"domain": ["netflix.com"], "balancerTag": "US"},
          {"domain": ["spiegel.de"], "balancerTag": "DE"},
          {"domain": ["geosite:ru"], "outboundTag": "direct"}
        ]
      }
    }`
	raw, err := ApplySplitRouting(json.RawMessage(cfg), []string{"Telegram.exe"})
	if err != nil {
		t.Fatalf("ApplySplitRouting: %v", err)
	}
	rules := rulesOf(t, raw)
	if len(rules) != 5 {
		t.Fatalf("got %d rules, want 5: %v", len(rules), rules)
	}
	want := []struct{ key, tag, domain string }{
		{"balancerTag", "US", "netflix.com"},
		{"balancerTag", "DE", "spiegel.de"},
		{"outboundTag", "direct", "geosite:ru"},
	}
	for i, w := range want {
		r := rules[i]
		d, _ := r["domain"].([]any)
		if r[w.key] != w.tag || len(d) != 1 || d[0] != w.domain {
			t.Errorf("rule %d = %v, want %s → %s", i, r, w.domain, w.tag)
		}
		if (w.key == "balancerTag") != (r["process"] != nil) {
			t.Errorf("rule %d = %v: only the tunnel rules are narrowed to the process", i, r)
		}
	}
	if rules[3]["outboundTag"] != "us1" || rules[3]["process"] == nil {
		t.Errorf("default rule = %v, want the listed process to the first outbound", rules[3])
	}
	if rules[4]["outboundTag"] != "direct" || rules[4]["process"] != nil {
		t.Errorf("catch-all = %v, want everything else direct", rules[4])
	}
}

// A panel with no rule into the tunnel relies on the first outbound: the
// listed processes go there instead of the whole thing being refused.
func TestApplySplitRoutingDefaultsToFirstOutbound(t *testing.T) {
	cfg := `{
      "outbounds": [
        {"tag": "proxy", "protocol": "vless"},
        {"tag": "direct", "protocol": "freedom"}
      ],
      "routing": {"rules": [{"domain": ["geosite:ru"], "outboundTag": "direct"}]}
    }`
	raw, err := ApplySplitRouting(json.RawMessage(cfg), []string{"Telegram.exe"})
	if err != nil {
		t.Fatalf("ApplySplitRouting: %v", err)
	}
	r := ruleWith(t, rulesOf(t, raw), "outboundTag", "proxy")
	if r["process"] == nil {
		t.Errorf("default rule = %v, want it narrowed to the listed process", r)
	}
}

// A panel rule naming processes of its own keeps only the listed ones, and
// goes when none of them is listed.
func TestApplySplitRoutingIntersectsRuleProcesses(t *testing.T) {
	cfg := `{
      "outbounds": [
        {"tag": "proxy", "protocol": "vless"},
        {"tag": "direct", "protocol": "freedom"}
      ],
      "routing": {"rules": [
        {"process": ["Telegram.exe", "chrome.exe"], "domain": ["a.example"], "outboundTag": "proxy"},
        {"process": ["firefox.exe"], "domain": ["b.example"], "outboundTag": "proxy"}
      ]}
    }`
	raw, err := ApplySplitRouting(json.RawMessage(cfg), []string{"telegram.exe"})
	if err != nil {
		t.Fatalf("ApplySplitRouting: %v", err)
	}
	rules := rulesOf(t, raw)
	procs, _ := rules[0]["process"].([]any)
	if len(procs) != 1 || procs[0] != "Telegram.exe" {
		t.Errorf("rule 0 = %v, want its process list cut to Telegram.exe", rules[0])
	}
	for _, r := range rules {
		if d, _ := r["domain"].([]any); len(d) == 1 && d[0] == "b.example" {
			t.Errorf("a rule for unlisted processes only survived: %v", r)
		}
	}
}
```

**Проверка:** общая проверка из раздела «Правила».
**Коммит:** `fix: split на Windows сохраняет правила панели для выбранных процессов (находки 2 и 5)`

---

## Шаг 2. Проверка связи идёт через сервер, а не через первый outbound (находка 3)

**Суть.** Без балансировщика проверочное правило целится в первый outbound, **который не `freedom`, не `blackhole` и не `dns`**. Если таких нет, конфиг не меняется, как и раньше, когда целиться не во что.

**Файлы:** `internal/xraycfg/probe.go`, `internal/xraycfg/probe_test.go`.

### 2.1. `probe.go`

В `PrependProbeRule` заменить ветку `switch`:

```go
	case firstOutboundTag(cfg["outbounds"]) != "":
		aim["outboundTag"] = firstOutboundTag(cfg["outbounds"])
```

на

```go
	case firstTunnelOutboundTag(cfg["outbounds"]) != "":
		aim["outboundTag"] = firstTunnelOutboundTag(cfg["outbounds"])
```

В комментарии над `PrependProbeRule` заменить три строки:

```go
// balancer is where its traffic actually goes, and without one the first
// outbound is what unmatched traffic falls through to. Either way the probe
// follows the same path as the user's own traffic.
```

на

```go
// balancer is where its traffic actually goes, and without one the first
// outbound through a server — unmatched traffic falls through to the first
// outbound, and a panel that puts direct first still sends its traffic to a
// server by rules. Either way the probe measures the tunnel.
```

В конец файла добавить (`firstOutboundTag` **не удалять**: её использует `firstTag` для балансировщиков):

```go
// firstTunnelOutboundTag reads the tag of the first outbound that goes through
// a server: a freedom, blackhole or dns outbound put first by the panel is not
// the tunnel, and a probe aimed at it would read "ок" off the plain internet.
func firstTunnelOutboundTag(arr json.RawMessage) string {
	outbounds, err := readOutbounds(arr)
	if err != nil {
		return ""
	}
	for _, o := range outbounds {
		if o.Tag != "" && !isLocalProtocol(o.Protocol) {
			return o.Tag
		}
	}
	return ""
}
```

### 2.2. `probe_test.go`: в конец файла

```go
// A panel may put its direct outbound first and reach the server by rules: the
// probe must still go to the server, not out direct.
func TestPrependProbeRuleSkipsLocalFirstOutbound(t *testing.T) {
	raw := json.RawMessage(`{
		"outbounds": [{"tag": "direct", "protocol": "freedom"}, {"tag": "block", "protocol": "blackhole"}, {"tag": "proxy", "protocol": "vless"}],
		"routing": {"rules": [{"network": "tcp,udp", "outboundTag": "proxy"}]}
	}`)
	out, err := PrependProbeRule(raw, []string{"full:www.google.com"})
	if err != nil {
		t.Fatalf("PrependProbeRule() = %v, want nil", err)
	}
	if got := rules(t, out); got[0]["outboundTag"] != "proxy" {
		t.Errorf("probe rule = %v, want it aimed at proxy", got[0])
	}
}

// Nothing but local outbounds: there is no tunnel to aim at, so no rule.
func TestPrependProbeRuleLeavesLocalOnlyConfigAlone(t *testing.T) {
	raw := json.RawMessage(`{"outbounds": [{"tag": "direct", "protocol": "freedom"}]}`)
	out, err := PrependProbeRule(raw, []string{"full:www.google.com"})
	if err != nil {
		t.Fatalf("PrependProbeRule() = %v, want nil", err)
	}
	if string(out) != string(raw) {
		t.Errorf("config = %s, want it unchanged", out)
	}
}
```

**Проверка:** общая. **Коммит:** `fix: проверка связи целится в сервер, а не в первый outbound профиля (находка 3)`

---

## Шаг 3. В TUN правило проверки действует только на вход `probe` (находка 1)

**Суть.** У `PrependProbeRule` появляется необязательный параметр: теги входов, которыми ограничиваются правила. В TUN (включая SPLIT поверх TUN на Windows и сессию через службу) передаётся `probe`. В PROXY ограничения нет: там проверка идёт через вход `http`, общий с трафиком пользователя. Замер пинга (`benchmark.go`) не меняется: параметр variadic, старые вызовы компилируются как есть.

**Файлы:** `internal/xraycfg/probe.go`, `internal/xraycfg/probe_test.go`, `internal/app/session.go`, `internal/app/tun_probe_test.go`.

### 3.1. `probe.go`

Строку `func PrependProbeRule(raw json.RawMessage, hosts []string) (json.RawMessage, error) {` заменить на (комментарий добавляется в конец существующего комментария над функцией, сразу после строки `// own aimed at the same place (F04).`):

```go
//
// inbounds, when given, limits the probe rules to the traffic of those inbounds.
// A tun session passes its probe inbound: every process's traffic arrives
// through the tun inbound, and a rule matching the probe hosts from any inbound
// would send an unlisted browser's www.google.com through the tunnel, ahead of
// the split's process rules and the panel's own direct ones.
func PrependProbeRule(raw json.RawMessage, hosts []string, inbounds ...string) (json.RawMessage, error) {
```

Сразу **после** закрывающей `}` блока `switch { … }` (выбор `aim`) и **перед** комментарием `// One rule per kind, and none at all…` вставить:

```go
	if len(inbounds) > 0 {
		aim["inboundTag"] = inbounds
	}

```

### 3.2. `session.go`

Все **три** вызова

```go
xraycfg.PrependProbeRule(raw, probeHosts(a.cfg.CheckURLs()))
```

заменить на

```go
xraycfg.PrependProbeRule(raw, probeHosts(a.cfg.CheckURLs()), a.probeInbounds()...)
```

(два в `buildModeSource`, один в `singleServerFromProfile`; `grep -n "PrependProbeRule" internal/app/session.go` должен показать три строки с `a.probeInbounds()...`).

Прямо перед комментарием `// withSplitRouting rewrites a finished config…` добавить:

```go
// probeInbounds is where the probe rules apply. A tun session's probes go
// through the probe inbound (A10), and only there: every process's traffic
// comes in through the tun inbound, and a probe rule without this limit would
// send any program's connection to a probe host into the tunnel — in split, an
// unlisted one's too. In proxy mode the probe shares the http inbound with the
// user's own traffic, so there is nothing to limit the rules to.
func (a *App) probeInbounds() []string {
	if a.tunMode() {
		return []string{xraycfg.ProbeInboundTag}
	}
	return nil
}

```

### 3.3. `probe_test.go`: в конец файла

```go
// With an inbound given, every probe rule is limited to it; without, none is.
func TestPrependProbeRuleLimitsToInbound(t *testing.T) {
	raw := json.RawMessage(`{"outbounds": [{"tag": "proxy", "protocol": "vless"}]}`)
	out, err := PrependProbeRule(raw, []string{"full:www.google.com", "1.1.1.1"}, ProbeInboundTag)
	if err != nil {
		t.Fatalf("PrependProbeRule() = %v, want nil", err)
	}
	got := rules(t, out)
	if len(got) != 2 {
		t.Fatalf("rules = %v, want a domain rule and an ip rule", got)
	}
	for _, r := range got {
		in, _ := r["inboundTag"].([]any)
		if len(in) != 1 || in[0] != ProbeInboundTag {
			t.Errorf("probe rule = %v, want inboundTag [%s]", r, ProbeInboundTag)
		}
	}

	out, err = PrependProbeRule(raw, []string{"full:www.google.com"})
	if err != nil {
		t.Fatalf("PrependProbeRule() = %v, want nil", err)
	}
	if r := rules(t, out)[0]; r["inboundTag"] != nil {
		t.Errorf("probe rule = %v, want no inboundTag without an inbound", r)
	}
}

// Split over tun: the probe rules go first, and must not catch an unlisted
// process's connection to a probe host on the tun inbound.
func TestProbeRuleDoesNotBypassSplit(t *testing.T) {
	raw, err := ApplySplitRouting(json.RawMessage(panelConfig), []string{"Telegram.exe"})
	if err != nil {
		t.Fatalf("ApplySplitRouting: %v", err)
	}
	raw, err = PrependProbeRule(raw, []string{"full:www.google.com"}, ProbeInboundTag)
	if err != nil {
		t.Fatalf("PrependProbeRule: %v", err)
	}
	for _, r := range rulesOf(t, raw) {
		if r["outboundTag"] == "direct" || r["outboundTag"] == "block" {
			continue
		}
		if r["process"] == nil && r["inboundTag"] == nil {
			t.Errorf("rule %v sends traffic into the tunnel whatever process opened it", r)
		}
	}
}
```

### 3.4. `tun_probe_test.go`: в конец файла

```go
// probeRules returns the rules aimed at the probe hosts of the default check
// URLs.
func probeRules(t *testing.T, raw json.RawMessage) []map[string]any {
	t.Helper()
	var cfg struct {
		Routing struct {
			Rules []map[string]any `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("config is not valid JSON: %v", err)
	}
	var out []map[string]any
	for _, r := range cfg.Routing.Rules {
		d, _ := r["domain"].([]any)
		if len(d) > 0 && d[0] == "full:www.google.com" {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		t.Fatalf("no probe rule in %v", cfg.Routing.Rules)
	}
	return out
}

// In tun the probe rules apply to the probe inbound only: on the tun inbound
// they would catch every program's connection to a probe host.
func TestBuildSessionConfig_TunProbeRuleOnProbeInbound(t *testing.T) {
	a := newTemplateApp(t)
	a.mode = "tun"
	raw, _, err := a.buildSessionConfig(splitTarget())
	if err != nil {
		t.Fatalf("buildSessionConfig: %v", err)
	}
	for _, r := range probeRules(t, raw) {
		in, _ := r["inboundTag"].([]any)
		if len(in) != 1 || in[0] != "probe" {
			t.Errorf("probe rule = %v, want inboundTag [probe]", r)
		}
	}
}

// In proxy mode the probe comes in through the http inbound like everything
// else, so its rule stays unlimited.
func TestBuildSessionConfig_ProxyProbeRuleUnlimited(t *testing.T) {
	a := newTemplateApp(t)
	raw, _, err := a.buildSessionConfig(splitTarget())
	if err != nil {
		t.Fatalf("buildSessionConfig: %v", err)
	}
	for _, r := range probeRules(t, raw) {
		if r["inboundTag"] != nil {
			t.Errorf("probe rule = %v, want no inboundTag in proxy mode", r)
		}
	}
}
```

**Проверка:** общая. **Коммит:** `fix: в TUN правило проверки связи действует только на её вход (находка 1)`

---

## Шаг 4. Статус TUN распознаёт обход по протоколу outbound-а (находка 6)

**Суть.** Тег правила сверяется с протоколом объявленного outbound-а (`freedom`, `blackhole` — обход). Имена `direct`, `block` и `blocked` учитываются, только если такого outbound-а в конфиге нет: на этом держатся старые тесты без `outbounds`. Первый outbound `freedom` или `blackhole` тоже считается обходом: туда идёт всё, что не совпало с правилами. Это осторожная оценка: надпись «есть исключения» при прямом outbound-е первым и правиле-«ловушке» в прокси безвреднее ложного «весь трафик через VPN».

**Файлы:** `internal/xraycfg/directrules.go`, `internal/xraycfg/directrules_test.go`. Тип `outboundInfo` уже добавлен в шаге 1.

### 4.1. `directrules.go`: заменить файл целиком

```go
package xraycfg

import "encoding/json"

// HasBypassRules reports whether the config routes anything past the proxy —
// to a freedom (direct) or blackhole (block) outbound, whatever its tag.
//
// This exists so the status screen can stop claiming more than it does (A03).
// The client no longer hardcodes a bypass list, but a panel profile routinely
// ships one — .ru domains and the IP-checking sites among them — and
// MergeProfile keeps it on purpose. A screen reading "весь трафик через VPN"
// over such a profile tells the user something the config contradicts, and the
// user finds out by seeing their provider's address on an IP checker.
//
// A tag is judged by the protocol of the outbound it names; the names "direct",
// "block" and "blocked" count only when no outbound in the config carries them.
// A first outbound that is itself direct or blocking counts too: everything no
// rule matches goes there.
func HasBypassRules(raw json.RawMessage) bool {
	var cfg struct {
		Outbounds []outboundInfo `json:"outbounds"`
		Routing   struct {
			Rules []struct {
				OutboundTag string `json:"outboundTag"`
				BalancerTag string `json:"balancerTag"`
			} `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return false
	}
	protocols := map[string]string{}
	for _, o := range cfg.Outbounds {
		if _, seen := protocols[o.Tag]; !seen {
			protocols[o.Tag] = o.Protocol
		}
	}
	if len(cfg.Outbounds) > 0 && bypassProtocol(cfg.Outbounds[0].Protocol) {
		return true
	}
	for _, r := range cfg.Routing.Rules {
		// A rule aimed at a balancer goes through the proxy outbounds it groups,
		// so it is not a bypass whatever the balancer is called.
		if r.BalancerTag != "" || r.OutboundTag == "" {
			continue
		}
		if p, declared := protocols[r.OutboundTag]; declared {
			if bypassProtocol(p) {
				return true
			}
			continue
		}
		switch r.OutboundTag {
		case "direct", "block", "blocked":
			return true
		}
	}
	return false
}

// bypassProtocol reports whether an outbound of this protocol takes traffic off
// the tunnel: freedom sends it out directly, blackhole drops it.
func bypassProtocol(protocol string) bool {
	return protocol == "freedom" || protocol == "blackhole"
}
```

### 4.2. `directrules_test.go`

В таблицу `tests` **перед** случаем `name: "no routing section"` вставить:

```go
		{
			// The tag is the panel's to choose; the protocol says what it is.
			name: "freedom under its own tag",
			raw:  `{"outbounds":[{"tag":"proxy","protocol":"vless"},{"tag":"bypass","protocol":"freedom"}],"routing":{"rules":[{"outboundTag":"bypass","domain":["geosite:ru"]}]}}`,
			want: true,
		},
		{
			name: "blackhole under its own tag",
			raw:  `{"outbounds":[{"tag":"proxy","protocol":"vless"},{"tag":"adblock","protocol":"blackhole"}],"routing":{"rules":[{"outboundTag":"adblock","domain":["geosite:category-ads"]}]}}`,
			want: true,
		},
		{
			// A declared outbound is judged by its protocol, not its name.
			name: "a server called direct",
			raw:  `{"outbounds":[{"tag":"direct","protocol":"vless"}],"routing":{"rules":[{"outboundTag":"direct"}]}}`,
			want: false,
		},
		{
			// Everything no rule matches goes to the first outbound.
			name: "direct first outbound",
			raw:  `{"outbounds":[{"tag":"direct","protocol":"freedom"},{"tag":"proxy","protocol":"vless"}],"routing":{"rules":[{"outboundTag":"proxy","domain":["geosite:google"]}]}}`,
			want: true,
		},
		{
			name: "proxy first, rules to proxy only",
			raw:  `{"outbounds":[{"tag":"proxy","protocol":"vless"},{"tag":"direct","protocol":"freedom"}],"routing":{"rules":[{"outboundTag":"proxy","domain":["geosite:google"]}]}}`,
			want: false,
		},
```

**Проверка:** общая. **Коммит:** `fix: статус TUN видит прямые и блокирующие outbound-ы с любым тегом (находка 6)`

---

## Шаг 5. Неудачный откат системного прокси повторяется (находка 7)

**Суть.**
- Если откат не удался, `proxyTouched` остаётся `true` и ставится `proxyRestoreFailed`. Следующий вызов пробует снова. Вызовы уже есть: `releaseSession` сразу после раннего вызова в `teardown`, `cleanup` при выходе. Добавляется ещё один — в начале следующей сессии.
- Перед повтором настройки читаются. Если там включён чужой прокси (не наш адрес), он не перезаписывается, а признак снимается. Выключенный прокси (так его оставляет `forceDisable` на Windows) остаётся «нашим», и исходная настройка восстанавливается.
- Если откат всё ещё не прошёл, следующая сессия **не снимает снимок заново**: на машине сейчас наша настройка, а исходная пользовательская — в старом снимке.

**Файлы:** `internal/app/app.go`, `internal/app/session.go`, `internal/app/release_test.go`.

### 5.1. `app.go`

В `type App struct` сразу после строки `proxyTouched  bool` добавить (затем `gofmt -w internal/app/app.go` — он выровняет соседнее поле `tunRouted`):

```go
	// proxyAddr is the "127.0.0.1:PORT" the session put into the system proxy
	// settings; proxyRestoreFailed says the last restore did not go through, so
	// the next one first checks the settings are still ours to put back.
	proxyAddr          string
	proxyRestoreFailed bool
```

### 5.2. `session.go`

1. Импорт: добавить `"slices"` в блок `import`, если его там нет.
2. Функцию `restoreSystemProxy` (вместе с комментарием над ней) заменить на:

```go
// restoreSystemProxy puts the system proxy setting back as the session found
// it. Split out of releaseSession because teardown runs it early, on its own:
// see the comment there. Doing nothing when there is nothing to undo makes the
// second call a no-op.
//
// A restore that fails leaves the setting marked as ours, so the next call —
// releaseSession, the teardown at exit, the next session's start — tries again
// instead of leaving the machine on a port nothing listens on. Before a retry
// the settings are read: one somebody changed after us is theirs now.
func (a *App) restoreSystemProxy() {
	if !a.proxyTouched {
		return
	}
	if a.proxyRestoreFailed && !a.proxyStillOurs() {
		slog.Warn("системный прокси изменён после нас — повторно не восстанавливаю")
		a.proxyTouched, a.proxyRestoreFailed = false, false
		return
	}
	if err := a.restoreProxy(a.originalProxy); err != nil {
		slog.Warn("failed to restore proxy", "error", err)
		a.proxyRestoreFailed = true
		return
	}
	a.proxyTouched, a.proxyRestoreFailed = false, false
}

// proxyStillOurs reports whether the settings are as a failed restore left
// them: still naming our address, or switched off by the fallback that ran
// after it (ProxyManager.Restore). Either way putting the user's back is ours
// to do. Anything else was set after us.
func (a *App) proxyStillOurs() bool {
	if a.readProxyState == nil {
		return true
	}
	cur := a.readProxyState()
	if !cur.Enabled {
		return true
	}
	return slices.Contains(proxyAddrs(cur.Server), a.proxyAddr)
}
```

3. В `bringUpProxy` строку

```go
	a.originalProxy = system.ReadProxyState()
```

заменить на (комментарий `// One snapshot governs both ends…` над ней оставить)

```go
	// A restore still owed from the last session keeps its snapshot: the
	// settings on the machine are ours, the user's are the ones saved then.
	if !a.proxyTouched {
		a.originalProxy = system.ReadProxyState()
	}
```

4. Там же, после строки `a.proxyTouched = true`, добавить строку:

```go
	a.proxyAddr = ourProxyAddr(ports.http)
```

5. В `runSession` **перед** комментарием `// Before the split is resolved: without the service, split and TUN are` вставить:

```go
	// A system proxy the last session could not put back is tried again first:
	// left as it is, it names a port nothing listens on.
	a.restoreSystemProxy()
```

(`ourProxyAddr` и `proxyAddrs` уже есть в `proxycheck.go`.)

### 5.3. `release_test.go`: в конец файла

Пакет `errors` там уже импортирован, повторно не добавлять.

```go
// A restore that failed is not forgotten: releaseSession, right after the
// early call in teardown, tries again, and once it goes through nothing more
// is written.
func TestRestoreSystemProxyRetriesAfterFailure(t *testing.T) {
	calls := 0
	a := &App{
		restoreProxy: func(system.ProxyState) error {
			calls++
			if calls == 1 {
				return errors.New("gsettings: временно недоступен")
			}
			return nil
		},
		readProxyState: func() system.ProxyState {
			return system.ProxyState{Enabled: true, Server: "127.0.0.1:10809"}
		},
		proxyTouched: true,
		proxyAddr:    "127.0.0.1:10809",
	}

	a.restoreSystemProxy()
	if !a.proxyTouched {
		t.Fatal("proxyTouched cleared by a restore that failed")
	}
	a.releaseSession()
	a.releaseSession()

	if calls != 2 {
		t.Errorf("restore called %d times, want 2 (the failure and one retry)", calls)
	}
	if a.proxyTouched {
		t.Error("proxyTouched still set after the retry went through")
	}
}

// A retry finds a proxy somebody set after us: it is theirs, and not written
// over with the snapshot from before the session.
func TestRestoreSystemProxyLeavesSomebodyElsesProxy(t *testing.T) {
	calls := 0
	a := &App{
		restoreProxy: func(system.ProxyState) error {
			calls++
			return errors.New("отказ")
		},
		readProxyState: func() system.ProxyState {
			return system.ProxyState{Enabled: true, Server: "10.0.0.1:3128"}
		},
		proxyTouched: true,
		proxyAddr:    "127.0.0.1:10809",
	}

	a.restoreSystemProxy()
	a.restoreSystemProxy()

	if calls != 1 {
		t.Errorf("restore called %d times, want 1: the retry must see the proxy is not ours", calls)
	}
	if a.proxyTouched {
		t.Error("proxyTouched still set for a proxy that is not ours any more")
	}
}

// The fallback after a failed restore switched the proxy off: putting the
// user's setting back is still owed.
func TestRestoreSystemProxyRetriesAfterFallbackSwitchedOff(t *testing.T) {
	calls := 0
	a := &App{
		restoreProxy: func(system.ProxyState) error {
			calls++
			if calls == 1 {
				return errors.New("отказ")
			}
			return nil
		},
		readProxyState: func() system.ProxyState { return system.ProxyState{Enabled: false} },
		proxyTouched:   true,
		proxyAddr:      "127.0.0.1:10809",
	}

	a.restoreSystemProxy()
	a.restoreSystemProxy()

	if calls != 2 {
		t.Errorf("restore called %d times, want 2", calls)
	}
	if a.proxyTouched {
		t.Error("proxyTouched still set after the retry went through")
	}
}
```

Старый `TestRestoreSystemProxyRunsOnce` должен остаться зелёным без изменений.

**Проверка:** общая. **Коммит:** `fix: неудачный откат системного прокси повторяется, чужая настройка не перезаписывается (находка 7)`

---

## Шаг 6. Служба объясняет отказ по гео-спискам (находка 4, минимальное исправление)

**Суть.** У `SetGeoAssets` уже есть параметр `why`: текст, который `CheckGeoLists` дописывает к отказу. Служба передавала пустую строку, теперь передаёт объяснение. Плюс README.

**Файлы:** `internal/service/run.go`, `README.md`.

### 6.1. `run.go`

В `setUp` строку `xraycfg.SetGeoAssets(filepath.Dir(binary), "")` заменить на `xraycfg.SetGeoAssets(filepath.Dir(binary), geoNote)`. Перед `// ownCore is the core beside the service's binary.` добавить:

```go
// geoNote follows a refusal over geo lists the service's databases lack. The
// service has only the databases copied in at install: the ones a subscription
// brings sit in the user's cache, out of its reach, and those updated by `u`
// beside the program reach it with the next install (H10).
const geoNote = "TUN через службу проверяет правила по гео-базам из папки службы. " +
	"Гео-базы, которые скачаны для подписки, службе недоступны, а обновлённые клавишей u " +
	"попадают к ней только после повторного `xray-runner service install`. " +
	"Если нужных списков нет и после этого, подключитесь в режиме PROXY " +
	"или запустите программу с правами администратора (root) и XRAY_RUNNER_NO_SERVICE=1"

```

### 6.2. `README.md`, раздел «Служба»

После абзаца, который заканчивается словами «…пока не повторите `service install`.», добавить абзац:

```markdown
Гео-базы, которые панель подписки указывает для своих правил, программа скачивает в ваш
кэш — службе они недоступны. Если правила профиля ссылаются на списки, которых нет в базах
службы, TUN через службу откажет и назовёт эти списки. Тогда подключитесь в режиме PROXY
или запустите программу с правами администратора (root) и `XRAY_RUNNER_NO_SERVICE=1`.
```

В разделе «Маршрутизация по процессам», в пункт **Windows**, после «Программу определяет само ядро xray в момент соединения.» дописать:

```markdown
  Для программ из списка действуют правила профиля в их порядке — прямые домены,
  балансировщики, выбор узла по доменам; остальные программы идут напрямую.
```

**Проверка:** общая. **Коммит:** `fix: служба объясняет отказ по гео-спискам, которых нет в её базах (находка 4)`

---

## Финальная проверка (после шага 6)

```bash
go build ./... && GOOS=windows go build ./...
go vet ./... && GOOS=windows go vet ./...
go test ./...
go test -race ./internal/xraycfg/ ./internal/app/ ./internal/service/
/tmp/lint/golangci-lint run ./... && GOOS=windows /tmp/lint/golangci-lint run ./...
```

Всё должно быть зелёным. На Windows-машине вручную (чек-лист `scripts/windows-test-checklist.md`):
- SPLIT с `apps.txt = Telegram.exe`: браузер открывает `www.google.com` с домашним адресом, а не с адресом VPN (находка 1);
- профиль с двумя балансировщиками: выбранная программа ходит через нужный узел по доменам (находка 5).

---

## Не для исполнителя: решения за пользователем

**Находка 4, полное решение.** Шаг 6 только объясняет отказ. Чтобы TUN через службу работал на базах панели, службе нужно получить эти базы. Возможные варианты, у каждого своя цена:
- программа передаёт службе хеши баз и URL панели, служба скачивает сама (https, предел размера, сверка хеша, разбор `geoListNames`) в свою папку. Минус: служба с правами root/SYSTEM ходит по адресам, которые назвал клиент;
- программа передаёт содержимое баз по IPC кусками. Минус: сообщение ограничено 4 МиБ, а `geosite.dat` весит десятки МиБ; нужен протокол передачи и место под файлы (на Linux служба пишет только в `/run/xray-runner`, это tmpfs);
- если программа сама имеет права на TUN (запущена через `sudo` или от администратора) и используются базы панели, работать без службы, как до H10. Самый дешёвый вариант, но решает только этот случай.

Это архитектурное решение. Его стоит принять до того, как отдавать задачу исполнителю.

**Решение:** второй вариант, передача по IPC. Он работает и без прав, и без сети у службы, а доверия требует не больше, чем правила маршрутизации, которые программа и так задаёт. Предел сообщения обходится кусками по 1 МиБ, место под файлы на Linux — `StateDirectory` (`/var/lib/xray-runner`). Реализовано в этой ветке отдельным коммитом; заодно базы, обновлённые клавишей `u`, доходят до службы без переустановки.

**Остаток находки 3.** Без балансировщика проверка теперь идёт в первый **серверный** outbound. Если правила панели ведут весь трафик в другой сервер, проверка измеряет не его, но это всё равно туннель, а не прямой выход. Точнее может быть только разбор правила-«ловушки» панели. Сейчас это не нужно.

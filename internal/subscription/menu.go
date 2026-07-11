package subscription

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"xray-runner/internal/ui"
)

var supportedProtocols = map[string]bool{
	"vless":     true,
	"vmess":     true,
	"ss":        true,
	"hysteria2": true,
	"hysteria":  true,
}

func ShowMenu(entries []SubEntry, subURL string) *SubEntry {
	reader := bufio.NewReader(os.Stdin)
	var benchmarkResults []BenchmarkResult

	for {
		ui.Title("Серверы")
		for i, e := range entries {
			hostPort := fmt.Sprintf("%s:%d", e.Address, e.Port)
			supported := supportedProtocols[e.Protocol]
			remark := e.Remarks
			if remark != "" {
				remark = " [" + remark + "]"
			}
			line := fmt.Sprintf("%-30s  %-5s %s%s", hostPort, e.Protocol, e.Network, remark)
			if benchmarkResults != nil {
				line += "  " + ui.Dim("▸ "+benchmarkResults[i].String())
			}
			tags := []string{}
			if !supported {
				tags = append(tags, "⚠️")
			}
			ui.Item(i+1, line, tags...)
		}
		ui.Divider()

		if benchmarkResults == nil {
			fmt.Printf("  [1-%d] выбор, 'b' бенчмарк, 'r' обновить, 's' сменить подписку: ", len(entries))
		} else {
			fmt.Printf("  Выберите номер (1-%d): ", len(entries))
		}

		input, err := reader.ReadString('\n')
		if err != nil {
			ui.Error("Ошибка ввода")
			continue
		}

		input = strings.TrimSpace(input)

		if strings.EqualFold(input, "b") {
			ui.Progress("Замер latency")
			start := time.Now()
			benchmarkResults = RunBenchmark(entries, 2*time.Second)
			ui.ClearLine()
			ui.Success(fmt.Sprintf("готово (%.1fs)", time.Since(start).Seconds()))
			continue
		}

		if strings.EqualFold(input, "r") {
			ui.Progress("Обновление подписки")
			newEntries, err := Fetch(subURL)
			if err != nil {
				ui.ClearLine()
				ui.Error(fmt.Sprintf("Ошибка: %v", err))
				continue
			}
			entries = newEntries
			benchmarkResults = nil
			ui.ClearLine()
			ui.Success(fmt.Sprintf("Подписка обновлена: %d серверов", len(entries)))
			continue
		}

		if strings.EqualFold(input, "s") {
			return nil
		}

		idx, err := strconv.Atoi(input)
		if err != nil || idx < 1 || idx > len(entries) {
			ui.Error("Некорректный номер. Попробуйте снова.")
			continue
		}

		selected := &entries[idx-1]
		if !supportedProtocols[selected.Protocol] {
			ui.Warn(fmt.Sprintf("Протокол %s не поддерживается. Выберите другой.", selected.Protocol))
			continue
		}

		ui.Success(fmt.Sprintf("Выбран: %s (%s:%d)", selected.Protocol, selected.Address, selected.Port))
		return selected
	}
}

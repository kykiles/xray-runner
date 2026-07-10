package subscription

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

var supportedProtocols = map[string]bool{
	"vless":     true,
	"vmess":     true,
	"ss":        true,
	"hysteria2": true,
	"hysteria":  true,
}

func ShowMenu(entries []SubEntry) *SubEntry {
	reader := bufio.NewReader(os.Stdin)
	var benchmarkResults []BenchmarkResult

	for {
		fmt.Println("\n── Выбор сервера ──────────────────────")
		for i, e := range entries {
			hostPort := fmt.Sprintf("%s:%d", e.Address, e.Port)
			supported := supportedProtocols[e.Protocol]
			remark := e.Remarks
			if remark != "" {
				remark = " [" + remark + "]"
			}
			line := fmt.Sprintf("  %2d. %-30s  %-5s %s%s", i+1, hostPort, e.Protocol, e.Network, remark)
			if benchmarkResults != nil {
				line += "  ▸ " + benchmarkResults[i].String()
			}
			if !supported {
				line += " ⚠️"
			}
			fmt.Println(line)
		}
		fmt.Print("  ─────────────────────────────────────\n")

		if benchmarkResults == nil {
			fmt.Printf("  [1-%d] выбор, 'b' бенчмарк: ", len(entries))
		} else {
			fmt.Printf("  Выберите номер (1-%d): ", len(entries))
		}

		input, err := reader.ReadString('\n')
		if err != nil {
			fmt.Println("  ❌ Ошибка ввода")
			continue
		}

		input = strings.TrimSpace(input)

		if strings.EqualFold(input, "b") {
			fmt.Print("  ⏳ Замер latency...")
			start := time.Now()
			benchmarkResults = RunBenchmark(entries, 2*time.Second)
			fmt.Printf(" готово (%.1fs)\n", time.Since(start).Seconds())
			continue
		}

		idx, err := strconv.Atoi(input)
		if err != nil || idx < 1 || idx > len(entries) {
			fmt.Println("  ❌ Некорректный номер. Попробуйте снова.")
			continue
		}

		selected := &entries[idx-1]
		if !supportedProtocols[selected.Protocol] {
			fmt.Printf("  ⚠️ Протокол %s не поддерживается. Выберите другой.\n", selected.Protocol)
			continue
		}

		fmt.Printf("  ✅ Выбран: %s (%s:%d)\n", selected.Protocol, selected.Address, selected.Port)
		return selected
	}
}

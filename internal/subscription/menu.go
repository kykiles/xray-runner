package subscription

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
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

	for {
		fmt.Println("\n── Выбор сервера ──────────────────────")
		for i, e := range entries {
			hostPort := fmt.Sprintf("%s:%d", e.Address, e.Port)
			supported := supportedProtocols[e.Protocol]
			remark := e.Remarks
			if remark != "" {
				remark = " [" + remark + "]"
			}
			if supported {
				fmt.Printf("  %2d. %-30s  %-5s %s%s\n", i+1, hostPort, e.Protocol, e.Network, remark)
			} else {
				fmt.Printf("  %2d. %-30s  %-5s ⚠️ неподдерживается%s\n", i+1, hostPort, e.Protocol, remark)
			}
		}
		fmt.Print("  ─────────────────────────────────────\n")
		fmt.Printf("  Выберите номер (1-%d): ", len(entries))

		input, err := reader.ReadString('\n')
		if err != nil {
			fmt.Println("  ❌ Ошибка ввода")
			continue
		}

		input = strings.TrimSpace(input)
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

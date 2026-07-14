package subscription

import (
	"bufio"
	"context"
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

func readStdin(ctx context.Context) (string, error) {
	ch := make(chan string, 1)
	errCh := make(chan error, 1)

	go func() {
		reader := bufio.NewReader(os.Stdin)
		input, err := reader.ReadString('\n')
		if err != nil {
			errCh <- err
		} else {
			ch <- input
		}
	}()

	select {
	case input := <-ch:
		return strings.TrimSpace(input), nil
	case err := <-errCh:
		return "", err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// BenchmarkFunc measures latency for each entry and returns results.
// onResult is called from worker goroutines for progress display.
type BenchmarkFunc func(ctx context.Context, entries []SubEntry, onResult func(BenchmarkResult)) []BenchmarkResult

// refresh re-fetches the subscription (with the same HWID headers as the
// initial load); nil disables the 'r' key.
func ShowMenu(ctx context.Context, entries []SubEntry, refresh func() ([]SubEntry, error), benchmarkFn BenchmarkFunc) *SubEntry {
	var benchmarkResults []BenchmarkResult

	for {
		if ctx.Err() != nil {
			return nil
		}

		ui.ClearScreen()
		ui.Title("Серверы")
		for i, e := range entries {
			hostPort := fmt.Sprintf("%s:%d", e.Address, e.Port)
			supported := supportedProtocols[e.Protocol]
			valid := e.Validate() == nil
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
			} else if !valid {
				tags = append(tags, "⚠ invalid")
			}
			ui.Item(i+1, line, tags...)
		}
		ui.Divider()

		if benchmarkResults == nil {
			fmt.Printf("  [1-%d] выбор, '0' назад, 'b' бенчмарк, 'r' обновить: ", len(entries))
		} else {
			fmt.Printf("  Выберите номер (1-%d), '0' назад: ", len(entries))
		}

		input, err := readStdin(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			ui.Error("Ошибка ввода")
			continue
		}

		if strings.EqualFold(input, "b") {
			ui.Progress("Замер latency")
			start := time.Now()

			if benchmarkFn != nil {
				count := 0
				benchmarkResults = benchmarkFn(ctx, entries, func(r BenchmarkResult) {
					count++
					ui.ClearLine()
					fmt.Printf("  ⏳ Замер latency... %d/%d", count, len(entries))
				})
			} else {
				benchmarkResults = RunBenchmark(entries, 2*time.Second)
			}

			ui.ClearLine()
			ui.Success(fmt.Sprintf("готово (%.1fs)", time.Since(start).Seconds()))
			continue
		}

		if strings.EqualFold(input, "r") {
			if refresh == nil {
				continue
			}
			ui.Progress("Обновление подписки")
			newEntries, err := refresh()
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

		if input == "0" || strings.EqualFold(input, "s") {
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
		if err := selected.Validate(); err != nil {
			ui.Warn(fmt.Sprintf("Запись невалидна: %v. Выберите другую.", err))
			continue
		}

		ui.Success(fmt.Sprintf("Выбран: %s (%s:%d)", selected.Protocol, selected.Address, selected.Port))
		return selected
	}
}

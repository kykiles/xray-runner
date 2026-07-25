# Диагностика зависания сети/CPU в TUN-режиме xray-runner (Windows).
#
# Запуск (из папки с xray-runner.exe, ЖЕЛАТЕЛЬНО во время проблемы, от администратора):
#   powershell -ExecutionPolicy Bypass -File .\diag-windows.ps1
#
# Результат: xray-runner-diag-<дата>.md рядом со скриптом. Секреты (id/password/
# ссылки подписок) из конфига не попадают в отчёт.

$ErrorActionPreference = 'Continue'
$ProgressPreference = 'SilentlyContinue'

$root = Split-Path -Parent $MyInvocation.MyCommand.Path
$out = Join-Path $root ("xray-runner-diag-{0}.md" -f (Get-Date -Format 'yyyy-MM-dd_HH-mm-ss'))
$md = New-Object System.Text.StringBuilder

function Add-Line($text) { [void]$md.AppendLine($text) }

# Sec выполняет блок и складывает его вывод в отчёт как секцию с код-блоком.
# Падение одной команды не должно ронять сбор — ошибка пишется в отчёт как есть.
function Sec($title, [scriptblock]$body) {
    Write-Host "  ... $title"
    Add-Line "## $title"
    Add-Line '```'
    try {
        $text = & $body 2>&1 | Out-String -Width 200
        Add-Line $text.TrimEnd()
    } catch {
        Add-Line "ОШИБКА: $_"
    }
    Add-Line '```'
    Add-Line ''
}

Write-Host "Сбор диагностики, ~30 секунд..."

$admin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()
    ).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)

Add-Line "# xray-runner: диагностика Windows"
Add-Line ''
Add-Line "- Дата: $(Get-Date -Format 'yyyy-MM-dd HH:mm:ss zzz')"
Add-Line "- Права администратора: $admin"
Add-Line "- Папка: $root"
Add-Line ''

Sec 'Система' {
    Get-CimInstance Win32_OperatingSystem |
        Select-Object Caption, Version, BuildNumber, OSArchitecture, LastBootUpTime,
            @{n = 'FreeRAM_MB'; e = { [int]($_.FreePhysicalMemory / 1KB) } } | Format-List
    Get-CimInstance Win32_Processor | Select-Object Name, NumberOfCores, NumberOfLogicalProcessors | Format-List
}

Sec 'Загрузка CPU: топ-15 процессов (замер 5 сек)' {
    $n = [Environment]::ProcessorCount
    $s1 = Get-Process | Select-Object Id, ProcessName, @{n = 'cpu'; e = { $_.CPU } }
    Start-Sleep -Seconds 5
    $s2 = Get-Process | Select-Object Id, ProcessName, @{n = 'cpu'; e = { $_.CPU } }
    $map = @{}
    foreach ($p in $s1) { $map[$p.Id] = $p.cpu }
    $s2 | ForEach-Object {
        $prev = $map[$_.Id]
        if ($null -ne $prev -and $null -ne $_.cpu) {
            [pscustomobject]@{
                Процесс = $_.ProcessName
                PID     = $_.Id
                'CPU_%' = [math]::Round(($_.cpu - $prev) / 5 / $n * 100, 1)
            }
        }
    } | Sort-Object 'CPU_%' -Descending | Select-Object -First 15 | Format-Table -AutoSize
}

Sec 'Службы в svchost: кто именно грузит (Dnscache и соседи)' {
    # 98% CPU у svchost ничего не говорит: в одном процессе живут десятки служб.
    $hot = Get-Process svchost -ErrorAction SilentlyContinue | Sort-Object CPU -Descending | Select-Object -First 5
    foreach ($p in $hot) {
        "PID $($p.Id): CPU(сумм) = $([math]::Round($p.CPU,1))s, потоков = $($p.Threads.Count)"
        Get-CimInstance Win32_Service -Filter "ProcessId=$($p.Id)" |
            Select-Object Name, DisplayName, State | Format-Table -AutoSize | Out-String
    }
    Get-Service Dnscache, Dhcp, NlaSvc, iphlpsvc -ErrorAction SilentlyContinue |
        Select-Object Name, Status, StartType | Format-Table -AutoSize
}

Sec 'Процессы xray / xray-runner' {
    Get-CimInstance Win32_Process -Filter "Name like '%xray%'" |
        Select-Object ProcessId, Name, CreationDate, CommandLine | Format-List
    Get-Process xray, xray-runner -ErrorAction SilentlyContinue |
        Select-Object Id, ProcessName, @{n = 'CPU_сек'; e = { [math]::Round($_.CPU, 1) } },
            @{n = 'RAM_MB'; e = { [int]($_.WorkingSet64 / 1MB) } }, Handles,
            @{n = 'Потоков'; e = { $_.Threads.Count } } | Format-Table -AutoSize
}

Sec 'Сетевые адаптеры' {
    Get-NetAdapter | Sort-Object ifIndex |
        Select-Object ifIndex, Name, InterfaceDescription, Status, LinkSpeed, MacAddress | Format-Table -AutoSize
    'ADRESSES:'
    Get-NetIPAddress -AddressFamily IPv4 |
        Select-Object ifIndex, InterfaceAlias, IPAddress, PrefixLength, PrefixOrigin, SuffixOrigin, AddressState |
        Format-Table -AutoSize
    'IP INTERFACE (метрики, MTU):'
    Get-NetIPInterface -AddressFamily IPv4 |
        Select-Object ifIndex, InterfaceAlias, NlMtu, InterfaceMetric, AutomaticMetric, Dhcp, ConnectionState |
        Format-Table -AutoSize
}

Sec 'TUN-адаптер (xray-tun / Wintun)' {
    # Ключевой пункт: у xray-tun должен быть заданный адрес (10.0.0.1), а не
    # 169.254.x.x — APIPA означает, что адрес интерфейсу никто не назначил.
    $tun = Get-NetAdapter -Name 'xray-tun' -ErrorAction SilentlyContinue
    if (-not $tun) { $tun = Get-NetAdapter -ErrorAction SilentlyContinue | Where-Object InterfaceDescription -match 'Wintun' }
    if ($tun) {
        $tun | Select-Object ifIndex, Name, InterfaceDescription, Status, DriverVersion, DriverDate | Format-List
        Get-NetIPAddress -InterfaceIndex $tun.ifIndex -ErrorAction SilentlyContinue |
            Select-Object IPAddress, PrefixLength, AddressFamily, PrefixOrigin, SuffixOrigin | Format-Table -AutoSize
        Get-NetIPInterface -InterfaceIndex $tun.ifIndex -ErrorAction SilentlyContinue |
            Select-Object AddressFamily, NlMtu, InterfaceMetric, ConnectionState, Forwarding | Format-Table -AutoSize
        Get-NetRoute -InterfaceIndex $tun.ifIndex -ErrorAction SilentlyContinue |
            Select-Object DestinationPrefix, NextHop, RouteMetric | Format-Table -AutoSize
    } else { 'TUN-адаптер не найден (приложение не запущено в режиме TUN?)' }
    'wintun.dll рядом с exe:'
    Get-Item (Join-Path $root 'wintun.dll') -ErrorAction SilentlyContinue |
        Select-Object FullName, Length, LastWriteTime, @{n = 'Version'; e = { $_.VersionInfo.FileVersion } } | Format-List
}

Sec 'Таблица маршрутизации' {
    Get-NetRoute -AddressFamily IPv4 | Sort-Object DestinationPrefix |
        Select-Object DestinationPrefix, NextHop, RouteMetric, InterfaceMetric, ifIndex, InterfaceAlias, Store |
        Format-Table -AutoSize
    'route print (ядро сравнивает по нему):'
    route print -4
}

Sec 'Куда реально уходит трафик (Find-NetRoute)' {
    # 1.1.1.1 — этим приложение выбирает физический адаптер для прямых сокетов.
    # 77.88.55.242 (Яндекс) — типичный «российский» адрес, который должен идти direct.
    foreach ($ip in '1.1.1.1', '8.8.8.8', '77.88.55.242', '90.156.232.4') {
        "--- $ip"
        Find-NetRoute -RemoteIPAddress $ip -ErrorAction SilentlyContinue |
            Select-Object IPAddress, InterfaceIndex, InterfaceAlias, NextHop, DestinationPrefix |
            Format-Table -AutoSize | Out-String
    }
}

Sec 'DNS' {
    Get-DnsClientServerAddress -AddressFamily IPv4 |
        Select-Object InterfaceIndex, InterfaceAlias, ServerAddresses | Format-Table -AutoSize
    "Записей в кэше DNS: $((Get-DnsClientCache -ErrorAction SilentlyContinue | Measure-Object).Count)"
    'Resolve-DnsName 2ip.ru:'
    Resolve-DnsName 2ip.ru -Type A -DnsOnly -ErrorAction SilentlyContinue |
        Select-Object Name, IPAddress, TTL | Format-Table -AutoSize | Out-String
    'nslookup 2ip.ru:'
    nslookup 2ip.ru
}

Sec 'TCP-соединения и эфемерные порты' {
    # Исчерпание диапазона 49152-65535 — прямой признак петли: сокеты плодятся
    # быстрее, чем освобождаются.
    netsh int ipv4 show dynamicport tcp
    $c = Get-NetTCPConnection -ErrorAction SilentlyContinue
    "Всего соединений: $($c.Count)"
    $c | Group-Object State | Sort-Object Count -Descending |
        Select-Object Count, Name | Format-Table -AutoSize | Out-String
    'Топ процессов по числу соединений:'
    $c | Group-Object OwningProcess | Sort-Object Count -Descending | Select-Object -First 10 |
        ForEach-Object {
            $p = Get-Process -Id $_.Name -ErrorAction SilentlyContinue
            [pscustomobject]@{ PID = $_.Name; Процесс = $p.ProcessName; Соединений = $_.Count }
        } | Format-Table -AutoSize | Out-String
    'Локальных портов занято (уникальных):'
    ($c | Select-Object -ExpandProperty LocalPort -Unique).Count
}

Sec 'Проверка связности' {
    foreach ($t in '77.88.55.242', '2ip.ru', '1.1.1.1') {
        "--- $t"
        Test-NetConnection -ComputerName $t -Port 443 -InformationLevel Detailed -WarningAction SilentlyContinue |
            Select-Object ComputerName, RemoteAddress, TcpTestSucceeded, SourceAddress, InterfaceAlias |
            Format-List | Out-String
    }
    'tracert до 77.88.55.242 (5 хопов):'
    tracert -4 -h 5 -w 1000 77.88.55.242
}

Sec 'Параметры сети / фильтрация' {
    netsh int ipv4 show global
    netsh int ipv4 show interfaces
    'Правила брандмауэра, созданные приложением (kill switch):'
    Get-NetFirewallRule -ErrorAction SilentlyContinue |
        Where-Object DisplayName -match 'xray' |
        Select-Object DisplayName, Direction, Action, Enabled | Format-Table -AutoSize | Out-String
    'Системный прокси:'
    Get-ItemProperty 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings' -ErrorAction SilentlyContinue |
        Select-Object ProxyEnable, ProxyServer, ProxyOverride | Format-List
}

Sec 'Конфиг xray (без секретов)' {
    $cfgPath = Join-Path $root 'xray_config.json'
    if (Test-Path $cfgPath) {
        $cfg = Get-Content $cfgPath -Raw | ConvertFrom-Json
        'inbounds:'
        $cfg.inbounds | Select-Object tag, protocol, port, listen | Format-Table -AutoSize | Out-String
        'settings TUN-инбаунда (что реально уехало в xray):'
        ($cfg.inbounds | Where-Object protocol -eq 'tun').settings | ConvertTo-Json -Depth 5
        'outbounds (только теги/протоколы + sockopt):'
        $cfg.outbounds | ForEach-Object {
            [pscustomobject]@{
                tag      = $_.tag
                protocol = $_.protocol
                sockopt  = if ($_.streamSettings.sockopt) { ($_.streamSettings.sockopt | ConvertTo-Json -Compress) } else { '' }
            }
        } | Format-Table -AutoSize | Out-String
        "routing.domainStrategy: $($cfg.routing.domainStrategy); правил: $($cfg.routing.rules.Count)"
        "dns.servers: $(($cfg.dns.servers | ForEach-Object { if ($_ -is [string]) { $_ } else { $_.address } }) -join ', ')"
        "log.loglevel: $($cfg.log.loglevel)"
    } else { "xray_config.json не найден в $root" }
}

Sec 'Лог приложения: сводка' {
    $logPath = Join-Path $root 'xray-runner.log'
    if (Test-Path $logPath) {
        $log = Get-Content $logPath -ErrorAction SilentlyContinue
        "Файл: $logPath, строк: $($log.Count), размер: $([int]((Get-Item $logPath).Length/1KB)) КБ"
        $acc = $log | Select-String -Pattern 'accepted' -SimpleMatch
        "Строк 'accepted': $($acc.Count)"
        'Распределение по маршрутам ([tun -> outbound]):'
        $acc | ForEach-Object { if ($_ -match '\[([^\]]+)\]') { $matches[1] } } |
            Group-Object | Sort-Object Count -Descending | Select-Object Count, Name |
            Format-Table -AutoSize | Out-String
        'Источники пакетов из TUN (должен быть адрес TUN-интерфейса):'
        $acc | ForEach-Object { if ($_ -match 'from tcp:([\d.]+):') { $matches[1] } } |
            Group-Object | Sort-Object Count -Descending | Select-Object -First 5 Count, Name |
            Format-Table -AutoSize | Out-String
        'Топ-10 адресов назначения:'
        $acc | ForEach-Object { if ($_ -match 'accepted \w+:([\d.:]+)') { $matches[1] } } |
            Group-Object | Sort-Object Count -Descending | Select-Object -First 10 Count, Name |
            Format-Table -AutoSize | Out-String
        'Строки с ошибками (последние 40):'
        $log | Select-String -Pattern 'error|failed|refused|unreachable|interface' |
            Select-Object -Last 40 | ForEach-Object { $_.Line } | Out-String
    } else { "xray-runner.log не найден в $root" }
}

Sec 'Лог приложения: последние 200 строк' {
    $logPath = Join-Path $root 'xray-runner.log'
    if (Test-Path $logPath) { Get-Content $logPath -Tail 200 } else { 'нет файла' }
}

Set-Content -Path $out -Value $md.ToString() -Encoding UTF8
Write-Host ""
Write-Host "Готово: $out"
Write-Host "Пришлите этот файл разработчику."

# Ручные проверки F07 (приватность рабочего каталога) на настоящей Windows.
#
# Запуск (от администратора, из папки со скриптом):
#   powershell -ExecutionPolicy Bypass -File .\test-windows-f07.ps1
#
# Что делает: заводит временную обычную учётную запись со случайным паролем,
# прогоняет ей проверки доступа, затем удаляет её. Рядом со скриптом должен
# лежать app_tests.exe — собранный набор тестов, Go на машине не нужен.
#
# Результат: xray-runner-f07-<дата>.md рядом со скриптом. Секретов машины и
# паролей в отчёт не попадает.

$ErrorActionPreference = 'Continue'
$ProgressPreference = 'SilentlyContinue'

$root = Split-Path -Parent $MyInvocation.MyCommand.Path
$exe = Join-Path $root 'app_tests.exe'
$out = Join-Path $root ("xray-runner-f07-{0}.md" -f (Get-Date -Format 'yyyy-MM-dd_HH-mm-ss'))
$md = New-Object System.Text.StringBuilder

function Add-Line($text) { [void]$md.AppendLine($text) }

$admin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()
    ).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)

if (-not $admin) {
    Write-Host "Нужны права администратора: скрипт заводит временную учётную запись." -ForegroundColor Red
    Write-Host "Запустите PowerShell от имени администратора и повторите."
    exit 1
}
if (-not (Test-Path $exe)) {
    Write-Host "Рядом со скриптом нет app_tests.exe — распакуйте архив целиком." -ForegroundColor Red
    exit 1
}

# Run-Tests прогоняет набор тестов и возвращает итог одной проверки.
# Отдельный процесс на каждую группу: у них разные переменные окружения.
function Run-Tests($title, $pattern, $envVars) {
    Write-Host "  ... $title"
    foreach ($k in $envVars.Keys) { Set-Item -Path "env:$k" -Value $envVars[$k] }
    try {
        $text = & $exe "-test.run" $pattern "-test.v" 2>&1 | Out-String -Width 200
    } catch {
        $text = "ОШИБКА запуска: $_"
    }
    foreach ($k in $envVars.Keys) { Remove-Item -Path "env:$k" -ErrorAction SilentlyContinue }

    $pass = ([regex]::Matches($text, '(?m)^\s*--- PASS')).Count
    $fail = ([regex]::Matches($text, '(?m)^\s*--- FAIL')).Count
    $skip = ([regex]::Matches($text, '(?m)^\s*--- SKIP')).Count
    $verdict = if ($fail -gt 0) { 'FAIL' } elseif ($pass -gt 0) { 'PASS' } else { 'SKIP' }

    [pscustomobject]@{
        Title = $title; Verdict = $verdict
        Pass = $pass; Fail = $fail; Skip = $skip; Text = $text
    }
}

Write-Host "Проверки F07, ~1 минута..."

# --- временная учётная запись -------------------------------------------------
# Имя короткое: у локальных учётных записей предел 20 символов.
$user = "xrt$(Get-Random -Minimum 100000 -Maximum 999999)"

# Пароль собирается из безопасного набора: без кавычек, &, % и ^ — их по-разному
# понимают cmd и PowerShell, а пароль уходит в том числе в net user. По одному
# символу каждого класса берётся явно: политика сложности иначе может отвергнуть
# случайную строку, и учётная запись не заведётся.
$sets = @('ABCDEFGHJKLMNPQRSTUVWXYZ', 'abcdefghijkmnopqrstuvwxyz', '23456789', '#$*+-=?@')
$all = -join $sets
$chars = @()
foreach ($s in $sets) { $chars += $s[(Get-Random -Maximum $s.Length)] }
while ($chars.Count -lt 24) { $chars += $all[(Get-Random -Maximum $all.Length)] }
$plain = ($chars | Get-Random -Count $chars.Count) -join ''
$secure = ConvertTo-SecureString $plain -AsPlainText -Force
$created = $false

try {
    try {
        New-LocalUser -Name $user -Password $secure -AccountNeverExpires `
            -UserMayNotChangePassword -Description 'xray-runner F07 test, временная' | Out-Null
        # Группа «Пользователи» названа по-разному в разных локализациях,
        # поэтому берётся по SID, а не по имени. Без членства в ней Windows может
        # не дать учётной записи войти, и главная проверка провалится не по делу —
        # поэтому неудача здесь не замалчивается.
        $users = Get-LocalGroup -SID 'S-1-5-32-545'
        try {
            Add-LocalGroupMember -Group $users -Member $user -ErrorAction Stop
        } catch {
            Write-Host "  Не удалось добавить $user в группу $($users.Name): $_" -ForegroundColor Yellow
        }
        $created = $true
    } catch {
        Write-Host "  New-LocalUser не сработал ($_), пробую net user" -ForegroundColor Yellow
        net user $user $plain /add /comment:"xray-runner F07 test" | Out-Null
        if ($LASTEXITCODE -eq 0) { $created = $true }
    }
    if (-not $created) { throw "не удалось завести временную учётную запись" }
    Write-Host "  Временная учётная запись $user заведена"

    $results = @()
    $results += Run-Tests 'Доступ вторым пользователем (главный пункт)' `
        '^TestRuntimeDir_SecondUserIsLockedOut$' `
        @{ XRAY_RUNNER_SECOND_USER = $user; XRAY_RUNNER_SECOND_PASS = $plain }

    $results += Run-Tests 'Temp этой машины пригоден для запуска' `
        '^TestCheckTrustedDirs_Machine$' `
        @{ XRAY_RUNNER_MACHINE_CHECKS = '1' }

    $results += Run-Tests 'Права каталога и конфига, в том числе elevated' `
        '^(TestCreateProtectedDir|TestCheckProtectedSD|TestCheckTrustedSD|TestClaim_)' `
        @{}

    # --- отчёт ----------------------------------------------------------------
    $os = Get-CimInstance Win32_OperatingSystem
    Add-Line '# xray-runner: ручные проверки F07 на Windows'
    Add-Line ''
    Add-Line "- Дата: $(Get-Date -Format 'yyyy-MM-dd HH:mm:ss zzz')"
    Add-Line "- Windows: $($os.Caption), версия $($os.Version), сборка $($os.BuildNumber), $($os.OSArchitecture)"
    Add-Line "- Запущено от администратора: $admin"
    Add-Line "- Имя машины: $env:COMPUTERNAME"
    Add-Line "- Учётная запись, под которой шли тесты: $env:USERNAME"
    Add-Line "- Временная вторая учётная запись: $user (удалена после прогона)"
    Add-Line "- app_tests.exe: $([int]((Get-Item $exe).Length/1MB)) МБ, SHA-256 $((Get-FileHash $exe -Algorithm SHA256).Hash)"
    Add-Line ''

    Add-Line '## Итог'
    Add-Line ''
    Add-Line '| Проверка | Итог | PASS | FAIL | SKIP |'
    Add-Line '|---|---|---|---|---|'
    foreach ($r in $results) {
        Add-Line "| $($r.Title) | **$($r.Verdict)** | $($r.Pass) | $($r.Fail) | $($r.Skip) |"
    }
    Add-Line ''
    $anyFail = ($results | Where-Object Verdict -eq 'FAIL').Count -gt 0
    if ($anyFail) {
        Add-Line 'Есть **FAIL** — подробности в разделах ниже, их нужно передать разработчику целиком.'
    } else {
        Add-Line 'Падений нет.'
    }
    Add-Line ''

    Add-Line '## Что означает каждая проверка'
    Add-Line ''
    Add-Line '- **Доступ вторым пользователем** — заводится вторая обычная учётная запись, и от её имени'
    Add-Line '  делается попытка открыть рабочий конфиг с паролем от сервера. Ожидается отказ. Внутри есть'
    Add-Line '  контрольный опыт: рядом лежит файл, который вторая учётная запись обязана прочитать. Если'
    Add-Line '  не прочитала — тест сообщает, что проверка ничего не доказала, вместо ложного успеха.'
    Add-Line '- **Temp этой машины** — проверяет, что временный каталог именно этого компьютера годится:'
    Add-Line '  посторонний не может подменить его или сменить права. Если тут FAIL — приложение на этой'
    Add-Line '  машине откажется стартовать, и это ожидаемое поведение, а не поломка.'
    Add-Line '- **Права каталога и конфига** — весь набор проверок прав, включая те, что требуют'
    Add-Line '  администратора и потому не идут в обычном прогоне.'
    Add-Line ''

    foreach ($r in $results) {
        Add-Line "## $($r.Title) — $($r.Verdict)"
        Add-Line ''
        Add-Line '```'
        Add-Line $r.Text.TrimEnd()
        Add-Line '```'
        Add-Line ''
    }

    Add-Line '## Права на временные каталоги (для разбора при FAIL)'
    Add-Line ''
    Add-Line '```'
    Add-Line "icacls $env:TEMP"
    Add-Line ((icacls $env:TEMP 2>&1 | Out-String).TrimEnd())
    Add-Line ''
    Add-Line "icacls $env:WINDIR\Temp"
    Add-Line ((icacls "$env:WINDIR\Temp" 2>&1 | Out-String).TrimEnd())
    Add-Line '```'
    Add-Line ''

    Set-Content -Path $out -Value $md.ToString() -Encoding UTF8
    Write-Host ''
    Write-Host "Готово: $out"
    Write-Host 'Пришлите этот файл разработчику.'
} finally {
    # Учётная запись удаляется в любом случае — даже если прогон упал.
    if ($created) {
        try {
            Remove-LocalUser -Name $user -ErrorAction Stop
        } catch {
            net user $user /delete | Out-Null
        }
        if (Get-LocalUser -Name $user -ErrorAction SilentlyContinue) {
            Write-Host "ВНИМАНИЕ: не удалось удалить учётную запись $user, удалите вручную." -ForegroundColor Red
        } else {
            Write-Host "  Временная учётная запись $user удалена"
        }
    }
}

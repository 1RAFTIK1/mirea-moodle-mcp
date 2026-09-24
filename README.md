# mirea-moodle-mcp

MCP-сервер, который подключает **Claude Desktop** к СДО РТУ МИРЭА ([online-edu.mirea.ru](https://online-edu.mirea.ru)).
После установки можно просто спросить Claude:

- «какие у меня дедлайны на этой неделе?»
- «что задано по большим данным и какие файлы приложены к заданию?»
- «скачай лекции по ИСУДД в Загрузки»
- «загрузи ~/Documents/отчёт.pdf в практическую работу 2 черновиком»

Плюс локальный **дашборд** с дедлайнами и курсами.

> Неофициальный проект студентов, к МИРЭА отношения не имеет. Работает только с твоим аккаунтом и только с тем, что ты и так видишь в браузере.

---

## Установка (≈ 3 минуты)

Нужен установленный [Claude Desktop](https://claude.ai/download).

### macOS / Linux

Открой Терминал и выполни:

```sh
curl -fsSL https://raw.githubusercontent.com/1RAFTIK1/mirea-moodle-mcp/main/install.sh | sh
```

### Windows

Открой PowerShell и выполни:

```powershell
irm https://raw.githubusercontent.com/1RAFTIK1/mirea-moodle-mcp/main/install.ps1 | iex
```

Установщик скачает программу и запустит настройку `setup`. Она проведёт по трём шагам:

1. **Сессия Moodle** — нужно один раз скопировать cookie из браузера (подробно — ниже).
2. **Подключение к Claude Desktop** — программа сама допишет себя в `claude_desktop_config.json` (старый конфиг сохранится рядом как `.bak-…`).
3. **Дашборд** — откроется страница с твоими дедлайнами.

После этого **полностью перезапусти Claude Desktop** (Quit, не просто закрыть окно) — в списке инструментов появится `mirea-moodle`.

---

## Как достать MoodleSession

Сервер не знает твой пароль и не проходит SSO сам — он работает через сессию, которую ты открыл(а) в браузере.

1. Зайди на <https://online-edu.mirea.ru/my/> и войди как обычно (логин, пароль, код с почты).
2. Открой инструменты разработчика на этой вкладке:

   | Браузер | Где найти |
   |---|---|
   | Chrome / Яндекс / Edge / Arc | `F12` (на Mac `⌥⌘I`) → **Application** → **Cookies** → `https://online-edu.mirea.ru` |
   | Firefox | `F12` → **Хранилище** → **Куки** → `https://online-edu.mirea.ru` |
   | Safari | Настройки → Дополнения → «Показывать меню Разработка», затем `⌥⌘I` → **Хранилище** → **Cookies** |

3. Найди строку **`MoodleSession`**, скопируй её **Value** (строка из ~26 букв и цифр).
4. Вставь в терминал, когда `setup` попросит.

⚠️ Это значение — временный ключ к твоему аккаунту. Не отправляй его никому (и в чаты тоже).

### Когда сессия протухнет

Moodle завершает сессию через несколько часов бездействия или при выходе из аккаунта. Claude скажет «сессия истекла». Тогда:

```sh
mirea-moodle-mcp cookie <новое значение MoodleSession>
```

Claude Desktop перезапускать не нужно.

> Если выйти из аккаунта в браузере кнопкой «Выход», сессия умрёт и у сервера. Просто закрой вкладку.

---

## Команды

| Команда | Что делает |
|---|---|
| `mirea-moodle-mcp setup` | первая настройка целиком |
| `mirea-moodle-mcp cookie <значение>` | обновить сессию |
| `mirea-moodle-mcp status` | проверить, жива ли сессия |
| `mirea-moodle-mcp dashboard` | собрать и открыть дашборд |
| `mirea-moodle-mcp install-claude` | заново прописать сервер в Claude Desktop |

## Инструменты для Claude

| tool | что делает |
|---|---|
| `list_courses` | курсы: текущие / все / прошедшие / будущие |
| `course_contents` | разделы и элементы курса (задания, файлы, папки, тесты…) |
| `deadlines` | невыполненные дедлайны: просроченные и ближайшие |
| `get_activity` | открыть элемент: текст задания, сроки, приложенные файлы, статус сдачи и оценка; файлы папки; текст страницы |
| `download_file` | скачать файл (по умолчанию в `~/Downloads/mirea`) |
| `read_text` | прочитать html/txt/csv прямо в чат |
| `submit_assignment` | загрузить файлы/текст в задание **черновиком**; `dry_run=true` — только проверить загрузку, ничего не сохраняя; `finalize=true` — отправить на проверку (Claude обязан спросить) |
| `dashboard` | открыть дашборд |
| `session_status` | проверить сессию |

`submit_assignment` **заменяет** ранее загруженные в черновик файлы. Первый раз попроси Claude сделать `dry_run` (файл уйдёт только во временную область, ответ не изменится), потом — настоящую загрузку, и проверь результат в браузере.

---

## Ручное подключение к Claude Desktop

Если `install-claude` не справился, открой конфиг:

- macOS: `~/Library/Application Support/Claude/claude_desktop_config.json`
- Windows: `%APPDATA%\Claude\claude_desktop_config.json`
- Linux: `~/.config/Claude/claude_desktop_config.json`

и добавь (путь — к скачанной программе):

```json
{
  "mcpServers": {
    "mirea-moodle": { "command": "/Users/ИМЯ/.local/bin/mirea-moodle-mcp" }
  }
}
```

## Где что лежит

- программа: `~/.local/bin/mirea-moodle-mcp` (Windows: `%LOCALAPPDATA%\mirea-moodle-mcp\`)
- сессия: `~/Library/Application Support/mirea-moodle-mcp/session.json` (macOS), `%APPDATA%\mirea-moodle-mcp\` (Windows), `~/.config/mirea-moodle-mcp/` (Linux) — права `600`
- дашборд: там же, `dashboard.html`

Удалить всё: удали программу, эту папку и блок `mirea-moodle` из конфига Claude.

## Проблемы

| Симптом | Что делать |
|---|---|
| `Moodle не принял эту сессию` | скопировано не то значение или не с того сайта; проверь, что открыт именно online-edu.mirea.ru и ты залогинен(а) |
| `сессия истекла` | `mirea-moodle-mcp cookie <новое значение>` |
| таймаут при подключении | сайт МИРЭА режет часть зарубежных IP — если включён VPN, добавь `*.mirea.ru` в исключения |
| в Claude нет инструментов | полностью перезапусти Claude Desktop; проверь конфиг (раздел выше) |
| macOS: «не удаётся проверить разработчика» | `xattr -d com.apple.quarantine ~/.local/bin/mirea-moodle-mcp` (бывает, если скачал бинарник браузером) |

## Сборка из исходников

```sh
go install github.com/1RAFTIK1/mirea-moodle-mcp@latest   # Go ≥ 1.22, без внешних зависимостей
```

Релиз с бинарниками под macOS/Linux/Windows: `./release.sh v0.x.y` (нужны Go и авторизованный `gh`).

## Документация

- [Как устроен сервер](docs/ARCHITECTURE.md) — протокол MCP, сессия Moodle, разбор сдачи задания по HTTP-запросам, безопасность
- [Как написать свой MCP-сервер](docs/WRITE-YOUR-OWN-MCP.md) — минимальный сервер на 80 строк ([пример](examples/minimal-mcp/main.go)), проектирование инструментов, отладка, распространение

## Как это устроено (коротко)

Мобильный API (веб-сервисы Moodle) на online-edu выключен, поэтому сервер работает как браузер: вызывает те AJAX-функции, которые Moodle открывает для веб-интерфейса (`core_course_get_enrolled_courses_by_timeline_classification`, `core_calendar_get_action_events_by_timesort`, `core_courseformat_get_state`), а задания, файлы и сдачу берёт с обычных страниц. Протокол MCP (JSON-RPC по stdio) реализован вручную, внешних зависимостей нет.

Лицензия: MIT.

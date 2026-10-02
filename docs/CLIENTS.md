# Подключение к агентам и IDE

`mirea-moodle-mcp` — обычный MCP-сервер, поэтому работает с любым клиентом, который поддерживает MCP. Разница между клиентами только в том, **где лежит конфиг** и **как он устроен**. Программа умеет прописывать себя сама:

```sh
mirea-moodle-mcp clients            # какие клиенты найдены на компьютере
mirea-moodle-mcp install detected   # подключить ко всем найденным
mirea-moodle-mcp install cursor     # к конкретному
mirea-moodle-mcp config zed         # показать фрагмент для ручной вставки
```

Перед изменением файла делается бэкап `<файл>.bak-<время>`. Остальные серверы и настройки не трогаются. Если конфиг не чистый JSON (например, с комментариями), программа его **не меняет** и выводит фрагмент для ручной вставки.

После подключения **перезапусти клиент**.

## Локальные клиенты (stdio)

| id | клиент | как подключается | файл |
|---|---|---|---|
| `claude-desktop` | Claude Desktop | файл | macOS `~/Library/Application Support/Claude/claude_desktop_config.json`, Windows `%APPDATA%\Claude\claude_desktop_config.json` |
| `claude-code` | Claude Code | `claude mcp add --scope user mirea-moodle -- <путь>` | — |
| `cursor` | Cursor | файл | `~/.cursor/mcp.json` |
| `vscode` | VS Code (Copilot, режим агента) | `code --add-mcp …`, иначе файл | `<профиль VS Code>/User/mcp.json` |
| `cline` | Cline (расширение VS Code) | файл | `<профиль VS Code>/User/globalStorage/saoudrizwan.claude-dev/settings/cline_mcp_settings.json` |
| `windsurf` | Windsurf | файл | `~/.codeium/windsurf/mcp_config.json` |
| `gemini` | Gemini CLI | файл | `~/.gemini/settings.json` |
| `codex` | OpenAI Codex CLI | `codex mcp add mirea-moodle -- <путь>`, иначе файл | `~/.codex/config.toml` |
| `lmstudio` | LM Studio | файл | `~/.lmstudio/mcp.json` |
| `zed` | Zed | файл (если без комментариев) | `~/.config/zed/settings.json` |

Проверено вживую: Claude Desktop, Claude Code (`claude mcp list` → Connected), Codex CLI (`codex mcp list` → enabled), Gemini CLI (`gemini mcp list` видит сервер), MCP Inspector (stdio и HTTP).

### Форматы — если делаешь руками

Большинство клиентов (Claude Desktop, Cursor, Windsurf, Gemini CLI, LM Studio, Cline):

```json
{ "mcpServers": { "mirea-moodle": { "command": "/полный/путь/mirea-moodle-mcp" } } }
```

VS Code:

```json
{ "servers": { "mirea-moodle": { "type": "stdio", "command": "/полный/путь/mirea-moodle-mcp" } } }
```

Zed:

```json
{ "context_servers": { "mirea-moodle": { "command": "/полный/путь/mirea-moodle-mcp", "args": [], "env": {} } } }
```

Codex CLI (`~/.codex/config.toml`):

```toml
[mcp_servers.mirea-moodle]
command = '/полный/путь/mirea-moodle-mcp'
```

Другие клиенты (Continue, Goose, JetBrains AI Assistant, Msty…) — ищи в их настройках раздел «MCP» и укажи как команду **полный путь** к программе, без аргументов.

### Особенности клиентов

- **VS Code**: инструменты MCP доступны только в режиме **Agent** чата Copilot.
- **Gemini CLI**: в «недоверенной» папке все MCP-серверы отключены (`gemini mcp list` покажет `Disabled`). В нужной папке выполни `/permissions trust`.
- **Cursor / Windsurf**: после правки конфига серверу иногда нужно нажать «refresh» в настройках MCP.
- **Локальные модели (LM Studio и т. п.)**: нужна модель с поддержкой вызова инструментов (tool calling). Небольшие модели путают аргументы — помогает явная просьба «используй инструмент deadlines».

## Удалённые клиенты (HTTP): ChatGPT, claude.ai и др.

Облачные ассистенты не могут запустить программу на твоём компьютере — им нужен **URL**. Для них есть HTTP-транспорт (MCP Streamable HTTP):

```sh
mirea-moodle-mcp http
# MCP (Streamable HTTP) слушает 127.0.0.1:8787
#   URL для клиентов:   http://127.0.0.1:8787/mcp/<токен>
```

- Токен генерируется один раз и хранится в `<конфиг>/mirea-moodle-mcp/http_token` (или задаётся через `MCP_HTTP_TOKEN`). Работает двумя способами: секретный путь `/mcp/<токен>` (для клиентов, которые принимают только URL) или заголовок `Authorization: Bearer <токен>`.
- По умолчанию сервер слушает только `127.0.0.1`. Чтобы до него достучался облачный клиент, нужен HTTPS-туннель:

  ```sh
  cloudflared tunnel --url http://127.0.0.1:8787
  # → https://<случайное-имя>.trycloudflare.com
  ```

  В клиент вводишь `https://<случайное-имя>.trycloudflare.com/mcp/<токен>`.
- **ChatGPT**: добавь свой коннектор или приложение по URL (режим разработчика в настройках; доступность зависит от тарифа). Авторизация — «без авторизации», токен уже в URL.
- **claude.ai**: Настройки → Коннекторы → добавить свой коннектор → URL.
- **Важно**:
  - МИРЭА пускает на online-edu не все зарубежные IP, поэтому запускай `http` на компьютере в России (например, на своём) и пробрасывай туннелем. Сервер в зарубежном дата-центре до Moodle, скорее всего, не достучится.
  - Файлы (`download_file`, дашборд) сохраняются на той машине, где запущен `http`. Пути в `submit_assignment` — тоже пути той машины.
  - URL с токеном = доступ к твоему Moodle. Никому не отправляй. Если ссылка утекла, удали файл `http_token` и перезапусти: токен сменится.

Флаги:

| флаг | что делает |
|---|---|
| `--addr 0.0.0.0:8787` | слушать не только localhost (например, в Docker за реверс-прокси с HTTPS) |
| `--allow-origin https://example.com` | разрешить запросы из браузера с этого Origin (защита от DNS-rebinding: «чужие» Origin получают 403) |
| `--no-auth` | без токена — **только** для `127.0.0.1` (MCP Inspector, локальные эксперименты) |
| `--allow-submit` | разрешить `submit_assignment` удалённому клиенту (по умолчанию выключено: облачный агент читает чужой текст со страниц Moodle, и это главный канал для инъекции промпта) |

### Как устроен HTTP-транспорт

Минимальная stateless-реализация спецификации MCP 2025-06-18:

| запрос | ответ |
|---|---|
| `POST /mcp[/<токен>]` с JSON-RPC запросом | `200 application/json` с ответом |
| `POST` с notification | `202 Accepted` без тела |
| `GET /mcp` | `405` — серверный SSE-поток не нужен: сервер ничего не присылает по своей инициативе |
| без токена / неверный токен | `401` |
| браузерный запрос с чужим `Origin` | `403` |
| `GET /healthz` | `ok` (для мониторинга / Docker healthcheck) |

Транспорт — тонкая обёртка над тем же диспетчером `dispatch()`, что и stdio. Инструменты ничего не знают о транспорте.

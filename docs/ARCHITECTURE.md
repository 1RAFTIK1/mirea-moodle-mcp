# Как устроен mirea-moodle-mcp

Этот документ — подробный разбор реализации: что такое MCP на уровне байтов, как сервер разговаривает с Claude, как он добывает данные из Moodle, где у Moodle подводные камни и почему решения именно такие.

- [1. Общая картина](#1-общая-картина)
- [2. MCP на уровне протокола](#2-mcp-на-уровне-протокола)
- [3. Файлы и слои](#3-файлы-и-слои)
- [4. Транспорт: `mcp.go`](#4-транспорт-mcpgo)
- [5. Сессия Moodle: `session.go`](#5-сессия-moodle-sessiongo)
- [6. Инструменты: `tools.go`](#6-инструменты-toolsgo)
- [7. Сдача задания: разбор по запросам](#7-сдача-задания-разбор-по-запросам)
- [8. Дашборд, setup, установка](#8-дашборд-setup-установка)
- [9. Как мы исследовали сайт](#9-как-мы-исследовали-сайт)
- [10. Безопасность](#10-безопасность)
- [11. Ограничения и что можно улучшить](#11-ограничения-и-что-можно-улучшить)

---

## 1. Общая картина

```
┌──────────────────┐  stdin/stdout (JSON-RPC)  ┌─────────────────────┐   HTTPS + cookie   ┌──────────────────────┐
│  Claude Desktop  │ ────────────────────────▶ │  mirea-moodle-mcp   │ ─────────────────▶ │ online-edu.mirea.ru  │
│  (MCP-клиент)    │ ◀──────────────────────── │  (MCP-сервер, Go)   │ ◀───────────────── │ (Moodle 4.x)         │
└──────────────────┘                           └─────────────────────┘                    └──────────────────────┘
        ▲                                                │
        │ модель решает, какой tool вызвать               ├─ lib/ajax/service.php  — JSON-функции веб-интерфейса
        │ и с какими аргументами                          ├─ /mod/*/view.php       — HTML-страницы (парсинг)
                                                          └─ /repository/*.php      — загрузка файлов
```

1. Claude Desktop при старте **запускает наш бинарник как дочерний процесс** (команда из `claude_desktop_config.json`).
2. Они общаются через **stdin/stdout**: одна строка = одно JSON-RPC сообщение.
3. Клиент спрашивает список инструментов (`tools/list`) — получает имена, описания и JSON Schema аргументов. Эти описания видит модель.
4. Когда модель решает вызвать инструмент, клиент шлёт `tools/call`, сервер делает HTTP-запросы в Moodle и возвращает текст (обычно JSON), который модель читает.

Модель никогда не ходит в Moodle сама. Всё, что она «умеет» на сайте, — ровно то, что описано нашими инструментами.

---

## 2. MCP на уровне протокола

**MCP (Model Context Protocol)** — открытый протокол, по которому LLM-приложение (клиент/хост) подключает внешние «серверы» с инструментами (tools), ресурсами (resources) и шаблонами промптов (prompts). Мы используем только tools.

Поверх JSON-RPC 2.0. Транспорт у нас — **stdio**: сообщения разделены `\n`, внутри сообщения переводов строки быть не должно (JSON это гарантирует, если не форматировать с отступами).

Три типа сообщений:

| тип | признак | ответ |
|---|---|---|
| request | есть `id` и `method` | обязателен, с тем же `id` |
| notification | есть `method`, нет `id` | не отвечаем |
| response | есть `id` и `result` **или** `error` | — |

### Жизненный цикл сессии

```jsonc
// 1. клиент → сервер
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"claude-ai","version":"…"}}}
// 2. сервер → клиент: версия протокола, что умеем, кто мы, общие инструкции для модели
{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"mirea-moodle","version":"v0.1.1"},"instructions":"Tools for …"}}
// 3. клиент → сервер (notification, без ответа)
{"jsonrpc":"2.0","method":"notifications/initialized"}
// 4. список инструментов
{"jsonrpc":"2.0","id":2,"method":"tools/list"}
{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"deadlines","description":"…","inputSchema":{"type":"object","properties":{"days":{"type":"integer","description":"…"}}},"annotations":{"readOnlyHint":true}}, …]}}
// 5. вызов
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"deadlines","arguments":{"days":7}}}
{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"[{\"due\":\"2026-09-28 23:59 Mon\", …}]"}],"isError":false}}
```

Важные детали, на которых ломаются самописные серверы:

- **Ошибка инструмента ≠ ошибка протокола.** Если Moodle ответил «сессия истекла», это `result` с `isError: true` и понятным текстом — модель прочитает и объяснит пользователю. JSON-RPC `error` (`-32601`, `-32602`) — только для «метод не существует», «параметры не парсятся».
- **`protocolVersion`**: сервер отвечает версией, которую поддерживает. Мы просто эхоим версию клиента — для набора «только tools» формат не менялся.
- **stdout — только протокол.** Любой `fmt.Println` в режиме сервера сломает клиенту парсер. Логи — в stderr (Claude Desktop пишет их в `~/Library/Logs/Claude/mcp-server-mirea-moodle.log`).
- **`annotations`** (`readOnlyHint`, `destructiveHint`) — подсказки клиенту: Claude Desktop может по-разному спрашивать подтверждение для читающих и изменяющих инструментов.
- **`instructions`** в `initialize` — текст, который клиент добавляет модели в контекст. Туда кладём «как пользоваться набором целиком» и правила вроде «перед finalize спроси пользователя».

---

## 3. Файлы и слои

| файл | слой | что внутри |
|---|---|---|
| `mcp.go` | транспорт | цикл чтения stdin, диспетчер `initialize` / `tools/list` / `tools/call`, конкурентность |
| `session.go` | доступ к Moodle | cookie-сессия, `sesskey`, вызов `lib/ajax/service.php`, сохранение на диск |
| `tools.go` | предметная логика | 10 инструментов, парсинг HTML, загрузка файлов, сдача |
| `dashboard.go` | UI | HTML-дашборд через `html/template` |
| `claude.go` | интеграция | запись в `claude_desktop_config.json` |
| `main.go` | CLI | `setup`, `cookie`, `status`, `dashboard`, `install-claude`, режим сервера |
| `util.go` | утилиты | HTML→текст, время МСК, пути |

Внешних зависимостей **нет** — только стандартная библиотека Go. Это осознанно: бинарник собирается под 6 платформ одной командой, нет supply-chain-рисков, нечего обновлять.

---

## 4. Транспорт: `mcp.go`

Суть в ~150 строках:

```go
type tool struct {
	Name   string         `json:"name"`
	Desc   string         `json:"description"`
	Schema map[string]any `json:"inputSchema"`
	Ann    map[string]any `json:"annotations,omitempty"`
	fn     func(a json.RawMessage) (any, error) // не экспортируется в JSON
}
```

Инструмент = метаданные для модели + Go-функция. `tools/list` просто сериализует срез `[]*tool` — неэкспортируемое поле `fn` в JSON не попадает.

Цикл:

```go
sc := bufio.NewScanner(os.Stdin)
sc.Buffer(make([]byte, 1<<20), 64<<20) // по умолчанию лимит строки 64 КБ — мало
for sc.Scan() {
	var q rpcReq
	json.Unmarshal(sc.Bytes(), &q)
	if len(q.ID) == 0 { continue }       // notification
	s.wg.Add(1)
	go func() { defer s.wg.Done(); s.handle(q) }()
}
s.wg.Wait() // иначе на EOF процесс выйдет раньше, чем горутины допишут ответы
```

Решения и почему:

- **`json.RawMessage` для `id`** — id может быть числом или строкой; мы возвращаем его байт-в-байт.
- **Горутина на запрос** — скачивание файла на 20 МБ не должно блокировать `ping`. Запись в stdout под мьютексом, чтобы строки не перемешались.
- **`recover()` вокруг инструмента** — паника в парсере HTML превращается в `isError`, сервер не падает.
- **Результат `any`** — строку отдаём как есть, остальное `json.MarshalIndent`. Модели удобнее читать JSON с отступами, чем прозу.

---

## 5. Сессия Moodle: `session.go`

### Почему не официальный API

У Moodle есть REST API (`/webservice/rest/server.php` + токен). Его использует мобильное приложение. **На online-edu он выключен**: `launch.php?service=moodle_mobile_app` отвечает «Веб-служба недоступна». Значит, работаем как браузер.

### Что нужно браузеру

1. Cookie **`MoodleSession`** — идентификатор сессии на сервере.
2. **`sesskey`** — CSRF-токен. Moodle кладёт его в каждую страницу в `M.cfg = {"sesskey":"…"}` и требует в каждом изменяющем запросе.

`setCookie()` ставит cookie, открывает `/my/`, регэкспом вытаскивает `sesskey` и `userId`. Если нас редиректнуло на `/login/` — сессия не та.

### AJAX-функции веб-интерфейса

Страницы Moodle сами тянут данные через `POST /lib/ajax/service.php?sesskey=…&info=<fn>`:

```http
POST /lib/ajax/service.php?sesskey=AbCd123&info=core_calendar_get_action_events_by_timesort
Content-Type: application/json
Cookie: MoodleSession=…

[{"index":0,"methodname":"core_calendar_get_action_events_by_timesort","args":{"timesortfrom":1790000000,"limitnum":50}}]
```

Ответ — массив: `[{"error":false,"data":{…}}]` или `[{"error":true,"exception":{"errorcode":"…","message":"…"}}]`. Если сломана сама сессия, приходит не массив, а объект `{"error":…,"errorcode":"invalidsesskey"}` — это отдельно ловим и превращаем в `errExpired`.

Через этот вход доступна **не любая** функция — только помеченные в коде Moodle `'ajax' => true`. Мы проверили перебором (команда `probe` в ранней версии):

| функция | доступна | используем для |
|---|---|---|
| `core_course_get_enrolled_courses_by_timeline_classification` | ✅ | список курсов |
| `core_calendar_get_action_events_by_timesort` | ✅ | дедлайны |
| `core_courseformat_get_state` | ✅ | структура курса (разделы, элементы) |
| `core_calendar_get_calendar_upcoming_view` | ✅ | — |
| `core_course_get_contents` | ❌ | (было бы удобнее: сразу файлы) |
| `mod_assign_get_assignments`, `mod_assign_get_submission_status` | ❌ | → парсим HTML |
| `core_webservice_get_site_info`, `gradereport_*` | ❌ | — |

Всё, что ❌, добываем со страниц.

### Хранение

`session.json` в `os.UserConfigDir()/mirea-moodle-mcp/` с правами `0600`: `{"moodle_session":"…","sesskey":"…","uid":75682}`. `sesskey` обновляется при каждом открытии страницы (он может меняться при ротации сессии).

---

## 6. Инструменты: `tools.go`

| tool | источник | как |
|---|---|---|
| `list_courses` | AJAX | timeline classification |
| `course_contents` | AJAX | `core_courseformat_get_state` возвращает **JSON-строку внутри JSON** — второй `Unmarshal`. Собираем разделы → `cmlist` → элементы |
| `deadlines` | AJAX | action events; фильтр по курсу — на нашей стороне |
| `get_activity` | HTML | по пути `/mod/<тип>/view.php` выбираем стратегию, см. ниже |
| `download_file` | HTTP | GET pluginfile-ссылки с cookie, стрим в файл |
| `read_text` | HTTP | то же, но в ответ; HTML → текст |
| `submit_assignment` | HTML-формы + repository | см. раздел 7 |
| `dashboard` | AJAX | `dashboard.go` |
| `session_status` | AJAX | дешёвый пинг |

### `get_activity`: стратегии по типу элемента

- **resource** → `view.php?id=…&redirect=1`. Moodle отвечает 303 на `pluginfile.php/…/файл.pdf` — это и есть ссылка на файл.
- **url** → тот же `redirect=1`, но редирект не следуем (`CheckRedirect: ErrUseLastResponse`), берём `Location` — внешняя ссылка.
- **folder** → все `href` на `pluginfile.php` со страницы.
- **assign** → из HTML:
  - `data-region="activity-dates"` → «Открыто с / Срок сдачи»;
  - `div.activity-description` → текст задания + приложенные файлы (`introattachment`);
  - таблица статуса: пары `<th>…</th><td>…</td>` → словарь «Состояние ответа», «Оставшееся время», файлы ответа…
- остальное → текст `div[role=main]`.

Парсим регэкспами, без HTML-парсера. Это хрупко, но: разметка Moodle (тема Boost) стабильна между версиями, зависимостей ноль, и каждый регэксп привязан к семантическому маркеру (`data-region`, `role`), а не к позиции.

### HTML → текст

`strip()`: выкидываем `<script>/<style>`, блочные теги → `\n`, `<li>` → `• `, остальные теги удаляем, `html.UnescapeString`, схлопываем пробелы. Модели не нужна разметка — нужен текст с абзацами.

---

## 7. Сдача задания: разбор по запросам

Это то, что делает браузер, когда ты нажимаешь «Добавить ответ → Загрузить файл → Сохранить». Сервер повторяет ровно ту же последовательность.

**Шаг 1. Открыть форму.** `GET /mod/assign/view.php?id=<cmid>&action=editsubmission`

Из HTML берём:
- скрытые поля формы `mform`: `id`, `userid`, `action=savesubmission`, `sesskey`, `lastmodified`, `_qf__mod_assign_submission_form`, **`files_filemanager`** — номер *черновой области* (draft itemid);
- JSON из `M.form_filemanager.init(Y, {…})`:

```jsonc
{
  "itemid": 494228198,            // черновая область файлов этой формы
  "client_id": "6ab4d9e01d94e",
  "maxbytes": "20971520",         // 20 МБ на файл
  "maxfiles": "10",
  "author": "Талапов Дмитрий Викторович",
  "defaultlicense": "allrightsreserved",
  "context": {"id": 1143299},
  "list": [ … уже загруженные файлы … ],
  "filepicker": {
    "repositories": {             // ← репозитории лежат ЗДЕСЬ, не в корне
      "3": {"type": "recent"},
      "4": {"type": "upload"},    // ← «Загрузить файл»
      "6": {"type": "user"},
      "7": {"type": "wikimedia"}
    }
  }
}
```

> Первая версия искала `repositories` в корне и падала с «нет репозитория загрузки». А ещё слала `author: ""` и `license: "unknown"` — Moodle может такое отвергнуть. Правильно — брать `author` и `defaultlicense` из этого JSON, как делает окно «Выбор файла».

**Шаг 2. Очистить черновую область** (там лежат ранее сданные файлы — форма редактирования начинается с копии текущего ответа):
`POST /repository/draftfiles_ajax.php?action=delete` с `sesskey, client_id, itemid, filepath, filename` — на каждый файл.

**Шаг 3. Загрузить файлы** — то самое окно «Выбор файла → Загрузить файл»:

```http
POST /repository/repository_ajax.php?action=upload
Content-Type: multipart/form-data

repo_upload_file=<байты>  title=отчёт.pdf  author=…  license=allrightsreserved
itemid=494228198  repo_id=4  env=filemanager  sesskey=…  client_id=…
ctx_id=1143299  savepath=/  maxbytes=20971520  areamaxbytes=-1
```

Успех: `{"url":"…draftfile.php/…","id":494228198,"file":"отчёт.pdf"}`. Ошибка: `{"error":"…"}`. Конфликт имён: `{"event":"fileexists", …}`.

Файлы пока лежат в *черновой* области пользователя — к заданию они ещё не привязаны. На этом останавливается `dry_run`.

**Шаг 4. Сохранить форму.** `POST /mod/assign/view.php` со всеми скрытыми полями + `submitbutton`. Moodle переносит файлы из черновой области в ответ. Теперь статус «Черновик» или «Отправлено», в зависимости от настроек задания.

**Шаг 5 (только `finalize=true`).** Если в задании включено «Требовать нажатия кнопки отправки»: `GET …&action=submit` → форма подтверждения (`action=confirmsubmit`, возможно чекбокс `submissionstatement`) → `POST`. После этого студент обычно не может править ответ, поэтому в описании инструмента и в `instructions` прописано «спроси пользователя».

**Проверка.** После сохранения сервер снова открывает задание и возвращает таблицу статуса — модель видит, что файл действительно прикреплён.

---

## 8. Дашборд, setup, установка

- **Дашборд** (`dashboard.go`): две AJAX-функции → группировка по срочности (просрочено / 48 ч / неделя / позже) → `html/template` (автоэкранирование, XSS из названий заданий не пройдёт) → один самодостаточный HTML-файл без внешних ресурсов, светлая/тёмная тема через `prefers-color-scheme`, фильтр по курсам на 10 строках JS.
- **`setup`**: инструкция → ввод cookie с проверкой (3 попытки) → `registerClaude()` → дашборд.
- **`registerClaude()`**: читает `claude_desktop_config.json`, делает бэкап `.bak-<время>`, добавляет/обновляет только ключ `mcpServers.mirea-moodle`, остальные серверы не трогает. Путь к бинарнику — `os.Executable()` + `EvalSymlinks`, чтобы работало после `go install` и через симлинки.
- **Установка**: `install.sh` определяет ОС/архитектуру, качает бинарник из GitHub Releases (`releases/latest/download/<имя>` — стабильный URL на последний релиз), fallback на `go install`, запускает `setup < /dev/tty` — потому что при `curl | sh` stdin занят скриптом.
- **Релиз**: `release.sh` — кросс-компиляция `CGO_ENABLED=0` под 6 платформ, `-ldflags "-X main.version=…"` для версии, `gh release create`.

---

## 9. Как мы исследовали сайт

Воспроизводимый метод для любого сайта без API:

1. **Проверить официальный API.** Для Moodle — `/login/token.php`, `/admin/tool/mobile/launch.php`. Здесь выключен.
2. **Понять авторизацию.** `curl -v` на страницу логина: редирект на `sso.mirea.ru/realms/mirea/…` → Keycloak, OIDC. Для публичного инструмента выбрали cookie из браузера: не храним пароли и не проходим второй фактор за пользователя.
3. **Открыть DevTools → Network** на нужных страницах и посмотреть, какие XHR делает сама страница. Так находятся `lib/ajax/service.php` и имена функций.
4. **Перебрать кандидатов** маленьким скриптом и записать, что доступно (таблица в разделе 5).
5. **Для недоступного — HTML.** Сохранить страницу, найти устойчивые маркеры (`data-region`, `role`, классы темы), проверить регэкспы на 2–3 разных страницах.
6. **Для форм** — повторить действие руками с открытым Network (как на скриншотах окна «Выбор файла»), выписать все поля каждого запроса. Сначала реализовать **безопасный режим** (`dry_run`) и проверить на живом сайте, и только потом «боевой» шаг.

---

## 10. Безопасность

- `MoodleSession` = доступ к аккаунту. Хранится с правами `0600`, в логи/ответы не попадает, в чат вставлять нельзя (README это повторяет).
- Все запросы — только на хост из `MOODLE_URL`: `sameHost()` не даст модели отправить cookie на чужой адрес через `download_file`.
- Имена скачиваемых файлов чистятся от `/` и `\` — нельзя записать за пределы папки.
- Изменяющий инструмент один (`submit_assignment`), помечен `destructiveHint`, по умолчанию сохраняет черновик; необратимое действие (`finalize`) — отдельный явный флаг.
- Сервер не хранит пароль и не проходит SSO сам.

---

## 11. Ограничения и что можно улучшить

- **Сессия живёт часы** → периодически `cookie`. Улучшение: браузерное расширение, которое само передаёт cookie на `localhost`.
- **Регэкспы по HTML** хрупки при смене темы Moodle. Улучшение: `golang.org/x/net/html` + селекторы; контрактные тесты на сохранённых страницах (`testdata/*.html`).
- **Тесты** (`quiz`) не проходятся — только ссылка и срок. Это правильно.
- **Оценки**: `gradereport_*` недоступны по AJAX — можно парсить `/grade/report/user/index.php`.
- **Уведомления**: `message_popup_get_popup_notifications` (кандидат на AJAX) → tool «что нового».
- **Ресурсы MCP**: файлы курса можно отдавать как `resources` (`resources/list`, `resources/read`), чтобы клиент подтягивал их без вызова инструментов.

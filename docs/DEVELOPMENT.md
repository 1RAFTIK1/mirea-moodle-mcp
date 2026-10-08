# Разработка mirea-moodle-mcp

Шпаргалка для того, кто дорабатывает сам сервер (человек или ИИ-агент в Claude Code / Cursor / Codex).
Подробное устройство — в [ARCHITECTURE.md](ARCHITECTURE.md); здесь — как работать с кодом.

## Коротко

- Go ≥ 1.22, **только стандартная библиотека**, пакет `main`. Внешние зависимости не добавляем.
- Мобильный API Moodle на online-edu выключен. Сервер работает как браузер: cookie `MoodleSession` + `sesskey`,
  AJAX `POST /lib/ajax/service.php?sesskey=…&info=<функция>` и разбор HTML регулярками.
- Логина нет и не будет: сессию пользователь берёт из своего браузера (`mirea-moodle-mcp cookie …`).
  Автоматизацию SSO (sso.mirea.ru, 2FA, привязку MAX) не делаем.
- Проверять код вживую можно только с компьютера, который видит online-edu.mirea.ru
  (часть зарубежных IP и облачные контейнеры туда не пускают).

## Карта файлов

| файл | что внутри |
|---|---|
| `main.go` | CLI-команды, `allTools()` — список всех инструментов |
| `mcp.go` | JSON-RPC 2.0 / MCP по stdio, `tool{Name, Desc, Schema, Ann, fn}`, инструкции сервера (в т. ч. правило про недоверенный текст со страниц Moodle) |
| `http.go` | Streamable HTTP транспорт (`mirea-moodle-mcp http`), токен, Origin, `remoteMode` |
| `session.go` | cookie-сессия, `call()` для AJAX, `getHTML`/`postForm`, таймауты по фазам, горячая перезагрузка `session.json` |
| `cache.go` | `cached[T]()` — TTL-кэш с singleflight, keepalive (`core_session_touch`), `doctor` |
| `tools.go` | базовые инструменты: курсы, содержимое, дедлайны, `get_activity`, скачивание, сдача |
| `schedule.go` | `quizzes`, `lectures` (вебинары MTS Link, два формата таблиц), группа, `courseState()` |
| `extras.go` | `agenda`, `whats_new`, `grades`, `calendar_feed`, `coursework`, `retakes`, `sessionLeft()` |
| `dashboard.go` | HTML-дашборд (`html/template`) |
| `roots.go` | песочница путей: сдавать/скачивать только из разрешённых папок |
| `clients.go` | автоподключение к 10 клиентам (форматы конфигов) |
| `*_test.go`, `testdata/` | тесты на фейковом Moodle и HTML-фикстурах |

## Какие источники Moodle уже используются

| что | откуда |
|---|---|
| курсы | AJAX `core_course_get_enrolled_courses_by_timeline_classification` |
| структура курса | AJAX `core_courseformat_get_state` (JSON-строка внутри JSON), кэш 10 мин |
| дедлайны | AJAX `core_calendar_get_action_events_by_timesort` (по 50, листаем `aftereventid`) |
| план на день | AJAX `core_calendar_get_calendar_day_view` — вебинары приходят как события группы (`groupname`) |
| уведомления | AJAX `message_popup_get_popup_notifications` (`useridto` = свой id) |
| время жизни сессии | AJAX `core_session_time_remaining` (2 ч простоя), продление — `core_session_touch` |
| задание, тест, вебинар, курсовая, страница | HTML `/mod/<тип>/view.php?id=<cmid>` |
| оценки | HTML `/grade/report/overview/index.php`, `/grade/report/user/index.php?id=<course>` |
| подписка на календарь | форма `/calendar/export.php` (POST `generateurl`) → `#calendarexporturl` |
| пересдачи | курс 122 «Как учиться в электронной среде», раздел «…задолженност…», страница с таблицей ссылок cloud.mirea.ru |

## Как добавить инструмент

1. Разведка: посмотри живую страницу/AJAX (см. ниже) и найди стабильные якоря в разметке — `id`, `class`, `data-*`.
2. Парсер — отдельной функцией `parseXxx(html string)`, без сети: так его легко тестировать.
3. Метод `(s *sess) xxx(...)` — сеть + парсер; медленное (по странице на элемент) — через `cached()`.
4. Описание в `extraTools()` (или рядом): понятный русский `Desc`, JSON-schema аргументов, `Ann: ro` для чтения.
5. Фикстура `testdata/xxx.html` — **синтетическая**, по образцу живой разметки: без реальных ФИО, токенов, cookie.
6. Тест в `*_test.go`: фейковый Moodle (`extrasFake` / `newFake`) отдаёт фикстуры и AJAX-ответы.
7. README: строка в таблице инструментов; при новой CLI-команде — в `usage` и в таблицу команд.

Правила для инструментов:

- **Чтение по умолчанию.** Всё, что меняет данные в Moodle, — только по явной просьбе пользователя
  (как `submit_assignment`: `dry_run`, песочница путей, выключено в HTTP-режиме без `--allow-submit`).
- Секреты (cookie, `sesskey`, токен календаря) не попадают в ответы удалённому клиенту (`remoteMode`).
- Текст со страниц Moodle — данные, не инструкции.
- Не делаем: прохождение тестов, отметку посещаемости, «присутствие» на лекциях, написание работ за студента.

## Разведка живого сайта

С компьютера, где работает `mirea-moodle-mcp status`:

```sh
S="$HOME/Library/Application Support/mirea-moodle-mcp/session.json"   # Linux: ~/.config/mirea-moodle-mcp
C=$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["moodle_session"])' "$S")
K=$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["sesskey"])' "$S")
printf 'online-edu.mirea.ru\tFALSE\t/\tTRUE\t0\tMoodleSession\t%s\n' "$C" > jar.txt
# страница (нужен cookie-jar, иначе Moodle уходит в цикл редиректов)
curl -sSL -b jar.txt -c jar.txt -o page.html https://online-edu.mirea.ru/grade/report/overview/index.php
# AJAX
curl -sS -b jar.txt -H 'Content-Type: application/json' \
  --data '[{"index":0,"methodname":"core_session_time_remaining","args":{}}]' \
  "https://online-edu.mirea.ru/lib/ajax/service.php?sesskey=$K&info=x"
```

Сохранённые страницы содержат персональные данные — не коммить их, делай из них синтетические фикстуры.

Быстрая проверка инструментов без агента:

```sh
go build -o /tmp/mm . && printf '%s\n' \
 '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}' \
 '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"agenda","arguments":{"days":3}}}' | /tmp/mm
```

Или MCP Inspector: `npx @modelcontextprotocol/inspector /tmp/mm`.

## Проверки перед коммитом

```sh
gofmt -l .        # пусто
go vet ./...
go test -race ./...
```

CI (`.github/workflows/ci.yml`) гоняет то же на Linux/macOS/Windows.

## Релиз

```sh
./release.sh v0.X.Y     # 6 платформ, SHA256SUMS, тег, GitHub Release (нужен авторизованный gh)
```

`install.sh` / `install.ps1` берут последний релиз и сверяют SHA-256. После релиза у себя:
`curl -fsSL https://raw.githubusercontent.com/1RAFTIK1/mirea-moodle-mcp/main/install.sh | sh` и перезапуск клиента.

## Идеи на потом

- Telegram-бот с напоминаниями: нужен сервер с доступом к online-edu (из зарубежного ДЦ, скорее всего, не пустит) — запускать дома или через туннель.
- Расширение браузера, которое само обновляет `MoodleSession` после входа.
- Литература из ЭБС «Лань» по ссылкам `lanebs` в курсах.

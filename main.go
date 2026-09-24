package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

var version = "dev" // set by -ldflags at release build

const usage = `mirea-moodle-mcp — MCP-сервер для online-edu.mirea.ru (Moodle РТУ МИРЭА)

  mirea-moodle-mcp setup              первая настройка: сессия + подключение к Claude Desktop
  mirea-moodle-mcp cookie <значение>  обновить сессию (значение cookie MoodleSession)
  mirea-moodle-mcp status             проверить, жива ли сессия
  mirea-moodle-mcp dashboard          открыть дашборд с дедлайнами и курсами
  mirea-moodle-mcp group ИКБО-50-23   задать группу (фильтр лекций)
  mirea-moodle-mcp install-claude     (пере)подключить сервер к Claude Desktop
  mirea-moodle-mcp version
  mirea-moodle-mcp                    без аргументов — режим MCP (так его запускает Claude)

Переменные окружения: MOODLE_URL, MOODLE_DOWNLOAD_DIR
`

func main() {
	s := newSess(baseURL())
	if len(os.Args) < 2 {
		if err := newServer(append(tools(s), scheduleTools(s)...)).serve(os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	switch os.Args[1] {
	case "setup":
		os.Exit(setup(s))
	case "cookie":
		v := ""
		if len(os.Args) > 2 {
			v = os.Args[2]
		} else {
			v = ask("Вставь значение MoodleSession: ")
		}
		name, err := s.setCookie(v)
		if err != nil {
			die(err)
		}
		fmt.Printf("✓ сессия сохранена%s\n", who(name))
	case "status":
		if err := ping(s); err != nil {
			die(err)
		}
		fmt.Println("✓ сессия жива, userid", s.uid)
	case "dashboard":
		p, err := buildDashboard(s)
		if err != nil {
			die(err)
		}
		fmt.Println("✓", p)
		openURL(p)
	case "group":
		c := loadCfg()
		if len(os.Args) > 2 {
			c.Group = normGroup(os.Args[2])
			if err := saveCfg(c); err != nil {
				die(err)
			}
		}
		fmt.Println("группа:", c.Group)
	case "install-claude":
		p, err := registerClaude()
		if err != nil {
			die(err)
		}
		fmt.Println("✓ добавлено в", p, "— перезапусти Claude Desktop")
	case "version", "-v", "--version":
		fmt.Println(version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Print(usage)
		os.Exit(2)
	}
}

func ping(s *sess) error {
	return s.call("core_course_get_enrolled_courses_by_timeline_classification", obj{"classification": "inprogress", "limit": 1, "offset": 0, "sort": "fullname"}, nil)
}

func who(name string) string {
	if name == "" {
		return ""
	}
	return " — " + name
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "✗", err)
	os.Exit(1)
}

var stdin = bufio.NewReader(os.Stdin)

func ask(q string) string {
	fmt.Print(q)
	l, _ := stdin.ReadString('\n')
	return strings.TrimSpace(l)
}

func setup(s *sess) int {
	fmt.Print(`
=== Настройка mirea-moodle-mcp ===

Шаг 1. Сессия Moodle.
Сервер работает от твоего имени через сессию браузера — пароль ему не нужен.

  1) Открой https://online-edu.mirea.ru/my/ и войди как обычно (SSO МИРЭА).
  2) Открой инструменты разработчика:
       Chrome / Яндекс / Edge:  F12 (на Mac ⌥⌘I) → Application → Cookies → https://online-edu.mirea.ru
       Firefox:                 F12 → Хранилище → Куки → https://online-edu.mirea.ru
       Safari:                  ⌥⌘I → Хранилище → Cookies (сначала включи меню «Разработка» в настройках)
  3) Скопируй значение (Value) строки MoodleSession.

Никому это значение не отправляй — это доступ к твоему аккаунту, пока сессия жива.

`)
	openURL(s.base + "/my/")
	for i := 0; ; i++ {
		v := ask("MoodleSession: ")
		name, err := s.setCookie(v)
		if err == nil {
			fmt.Printf("✓ вошли%s\n\n", who(name))
			break
		}
		fmt.Println("✗", err)
		if i >= 2 {
			return 1
		}
	}

	if g := normGroup(ask("Твоя группа (например ИКБО-50-23, Enter — пропустить): ")); g != "" {
		c := loadCfg()
		c.Group = g
		saveCfg(c)
		fmt.Println("✓ группа", g)
	}

	fmt.Println("\nШаг 2. Подключение к Claude Desktop.")
	if a := strings.ToLower(ask("Добавить сервер в Claude Desktop? [Y/n] ")); a == "" || a == "y" || a == "д" || a == "да" || a == "yes" {
		p, err := registerClaude()
		if err != nil {
			fmt.Println("✗", err)
			fmt.Println("  Можно добавить вручную — см. README, раздел «Ручное подключение».")
		} else {
			fmt.Println("✓ добавлено в", p)
		}
	}

	fmt.Println("\nШаг 3. Дашборд.")
	if p, err := buildDashboard(s); err == nil {
		fmt.Println("✓", p)
		openURL(p)
	} else {
		fmt.Println("✗", err)
	}

	fmt.Print(`
Готово. Перезапусти Claude Desktop (полностью: Quit и открыть заново) и спроси, например:
  «какие у меня дедлайны на этой неделе?»

Когда сессия истечёт (Claude скажет об этом), снова войди в браузере и выполни:
  mirea-moodle-mcp cookie <новое значение MoodleSession>
`)
	return 0
}

func openURL(u string) {
	var c *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		c = exec.Command("open", u)
	case "windows":
		c = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		c = exec.Command("xdg-open", u)
	}
	c.Start()
}

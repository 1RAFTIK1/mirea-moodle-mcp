const $ = (id) => document.getElementById(id);
const send = (msg) => chrome.runtime.sendMessage(msg);
const LOGIN = "https://online-edu.mirea.ru/login/index.php";

function el(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text) e.textContent = text;
  return e;
}

function show(s) {
  const box = $("st");
  box.replaceChildren();
  if (!s) {
    box.append(el("span", "mut", "Ещё не подключено. Нажми «Подключить»."));
    return;
  }
  if (s.ok) {
    box.append(el("div", "ok", "✓ Подключено" + (s.name ? " — " + s.name : "")));
    if (s.uid) box.append(el("div", "mut", "userid " + s.uid));
  } else if (s.hostError) {
    box.append(el("div", "bad", "Программа mirea-moodle-mcp не найдена браузером."));
    box.append(el("div", "mut", "Выполни в терминале и перезапусти браузер:"));
    box.append(el("code", "", "mirea-moodle-mcp extension"));
    box.append(el("div", "mut", s.error || ""));
  } else if (s.needLogin) {
    box.append(el("div", "bad", "Нужно войти в Moodle."));
    const a = el("a", "", "Открыть online-edu.mirea.ru →");
    a.href = "#";
    a.onclick = () => chrome.tabs.create({ url: LOGIN });
    box.append(a);
    box.append(el("div", "mut", "После входа cookie обновится сама (если включено автообновление)."));
  } else {
    box.append(el("div", "bad", "✗ " + (s.error || "ошибка")));
  }
  if (s.at) box.append(el("div", "mut", new Date(s.at).toLocaleString("ru-RU")));
}

const MOODLE = "https://online-edu.mirea.ru/";
const open = (u) => chrome.tabs.create({ url: u && u.startsWith(MOODLE) ? u : MOODLE + "my/" });

function when(ts) {
  const ms = ts * 1000 - Date.now();
  const d = new Date(ts * 1000).toLocaleString("ru-RU", {
    timeZone: "Europe/Moscow", weekday: "short", day: "numeric", month: "short", hour: "2-digit", minute: "2-digit",
  });
  const h = ms / 36e5;
  const rel = h < 1 ? "меньше часа" : h < 24 ? "через " + Math.floor(h) + " ч" : "через " + Math.floor(h / 24) + " дн";
  return { text: d + " · " + rel, cls: h < 24 ? "soon" : h < 72 ? "near" : "" };
}

function showDeadlines(r) {
  const sec = $("dlsec"), ul = $("dl"), more = $("dlmore");
  if (!r || !r.ok) {
    sec.hidden = true;
    return;
  }
  sec.hidden = false;
  ul.replaceChildren();
  more.replaceChildren();
  if (!r.items.length) ul.append(el("li", "mut", "Ближайших дедлайнов нет 🎉"));
  for (const it of r.items) {
    const li = el("li");
    const w = when(it.ts);
    li.append(el("div", "nm", it.name), el("div", "cr", it.course || ""), el("div", "due " + w.cls, w.text));
    li.title = it.name + (it.course ? "\n" + it.course : "");
    li.onclick = () => open(it.url);
    ul.append(li);
  }
  if (r.overdue) more.append(el("span", "bad", "и просрочено: " + r.overdue + " "));
  const all = el("a", "", "все в Moodle →");
  all.href = "#";
  all.onclick = () => open(MOODLE + "my/");
  more.append(all);
}

async function loadDeadlines() {
  showDeadlines(await send({ type: "cachedDeadlines" })); // instant, from this browser session
  const r = await send({ type: "deadlines" });
  if (r && r.ok) showDeadlines(r);
  else if (r && r.needLogin) {
    $("dlsec").hidden = true;
    show({ ok: false, needLogin: true });
  }
}

$("go").onclick = async () => {
  $("go").disabled = true;
  $("go").textContent = "Проверяю…";
  const st = await send({ type: "connect" });
  show(st);
  if (st && st.ok) loadDeadlines();
  $("go").disabled = false;
  $("go").textContent = "Подключить";
};

$("auto").onchange = async (e) => {
  const st = await send({ type: "setAuto", auto: e.target.checked });
  show(st && "ok" in st ? st : st.status);
};

send({ type: "get" }).then((st) => {
  $("auto").checked = st.auto !== false;
  show(st.status);
  if (st.status && st.status.ok) loadDeadlines();
});

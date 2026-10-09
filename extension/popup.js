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

$("go").onclick = async () => {
  $("go").disabled = true;
  $("go").textContent = "Проверяю…";
  show(await send({ type: "connect" }));
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
});

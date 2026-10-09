// МИРЭА Moodle → MCP: передаёт cookie MoodleSession локальной программе
// mirea-moodle-mcp через Native Messaging (без сети, без DevTools).
//
// Значение cookie нигде не хранится: в storage лежит только его SHA-256,
// чтобы не отправлять одно и то же повторно.

const HOST = "ru.mirea.moodle_mcp";
const SITE = "https://online-edu.mirea.ru";
const COOKIE = "MoodleSession";

const sha256 = async (s) => {
  const d = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(s));
  return [...new Uint8Array(d)].map((b) => b.toString(16).padStart(2, "0")).join("");
};

const getState = async () =>
  (await chrome.storage.local.get({ auto: true, lastHash: "", status: null })) || {};

function native(msg) {
  return new Promise((resolve) => {
    chrome.runtime.sendNativeMessage(HOST, msg, (resp) => {
      const err = chrome.runtime.lastError;
      if (err) resolve({ ok: false, hostError: true, error: err.message || String(err) });
      else resolve(resp || { ok: false, error: "пустой ответ программы" });
    });
  });
}

async function badge(ok) {
  await chrome.action.setBadgeText({ text: ok === null ? "" : ok ? "✓" : "!" });
  await chrome.action.setBadgeBackgroundColor({ color: ok ? "#1a7f37" : "#cf222e" });
}

async function currentCookie() {
  const c = await chrome.cookies.get({ url: SITE + "/", name: COOKIE });
  return c && c.value ? c.value : "";
}

// push sends the browser's cookie to mirea-moodle-mcp.
// force=true (кнопка «Подключить»): проверить в Moodle даже если cookie не менялась.
async function push(force) {
  const value = await currentCookie();
  if (!value) {
    const status = { ok: false, needLogin: true, error: "Нет cookie MoodleSession: войди на online-edu.mirea.ru", at: Date.now() };
    await chrome.storage.local.set({ status });
    await badge(false);
    return status;
  }
  const h = await sha256(value);
  const st = await getState();
  if (!force && h === st.lastHash && st.status && st.status.ok) return st.status;

  const r = await native({ type: "set_cookie", cookie: value, force: !!force });
  const status = { ...r, at: Date.now() };
  delete status.cookie; // на всякий случай: значение не храним
  // До входа Moodle выдаёт анонимную сессию — это не ошибка, а «ещё не вошли».
  if (!r.ok && !r.hostError) status.needLogin = true;
  await chrome.storage.local.set({ status, lastHash: r.ok ? h : st.lastHash });
  await badge(!!r.ok);
  return status;
}

// Автообновление: Moodle меняет MoodleSession при входе — сразу отдаём новую.
let timer = null;
chrome.cookies.onChanged.addListener(async ({ cookie, removed }) => {
  if (removed || cookie.name !== COOKIE || !cookie.domain.endsWith("online-edu.mirea.ru")) return;
  const { auto } = await getState();
  if (!auto) return;
  clearTimeout(timer);
  timer = setTimeout(() => push(false), 1500); // вход даёт несколько Set-Cookie подряд
});

// Подстраховка: при старте браузера и раз в 30 минут сверяем cookie.
chrome.runtime.onStartup.addListener(() => getState().then((s) => s.auto && push(false)));
chrome.runtime.onInstalled.addListener(() => chrome.alarms.create("sync", { periodInMinutes: 30 }));
chrome.alarms.onAlarm.addListener((a) => a.name === "sync" && getState().then((s) => s.auto && push(false)));

chrome.runtime.onMessage.addListener((msg, sender, reply) => {
  if (sender.id !== chrome.runtime.id) return false;
  (async () => {
    if (msg.type === "connect") return push(true);
    if (msg.type === "check") return native({ type: "status" });
    if (msg.type === "deadlines") {
      const r = await native({ type: "deadlines", limit: 4 });
      if (r.ok) await chrome.storage.session.set({ dl: { ...r, at: Date.now() } });
      return r;
    }
    if (msg.type === "cachedDeadlines") return (await chrome.storage.session.get("dl")).dl || null;
    if (msg.type === "setAuto") {
      await chrome.storage.local.set({ auto: !!msg.auto });
      return msg.auto ? push(false) : getState();
    }
    return getState();
  })().then(reply);
  return true; // async reply
});

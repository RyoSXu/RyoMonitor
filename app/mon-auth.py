#!/usr/bin/env python3
from http import HTTPStatus
from http.server import ThreadingHTTPServer, BaseHTTPRequestHandler
from pathlib import Path
from urllib.parse import parse_qs, quote, unquote, urlparse
import base64
import hashlib
import hmac
import html
import mimetypes
import os
import secrets
import time


WEB_ROOT = Path(os.environ.get("MON_AUTH_WEB_ROOT", "/opt/ryo-monitor/app")).resolve()
HOST = os.environ.get("MON_AUTH_HOST", "127.0.0.1")
PORT = int(os.environ.get("MON_AUTH_PORT", "8090"))
COOKIE_NAME = os.environ.get("MON_AUTH_COOKIE", "ryo_mon_session")
SESSION_TTL = int(os.environ.get("MON_AUTH_SESSION_TTL", str(7 * 24 * 60 * 60)))
PASSWORD_HASH = os.environ["MON_AUTH_PASSWORD_HASH"]
SECRET = os.environ["MON_AUTH_SECRET"].encode("utf-8")


def b64url_encode(raw: bytes) -> str:
    return base64.urlsafe_b64encode(raw).decode("ascii").rstrip("=")


def b64url_decode(value: str) -> bytes:
    return base64.urlsafe_b64decode(value + "=" * (-len(value) % 4))


def verify_password(password: str) -> bool:
    try:
        scheme, iterations, salt, expected = PASSWORD_HASH.split("$", 3)
        if scheme != "pbkdf2_sha256":
            return False
        derived = hashlib.pbkdf2_hmac(
            "sha256",
            password.encode("utf-8"),
            b64url_decode(salt),
            int(iterations),
        )
        return hmac.compare_digest(b64url_encode(derived), expected)
    except Exception:
        return False


def sign_payload(payload: str) -> str:
    digest = hmac.new(SECRET, payload.encode("utf-8"), hashlib.sha256).digest()
    return f"{b64url_encode(payload.encode('utf-8'))}.{b64url_encode(digest)}"


def valid_session(cookie_header: str | None) -> bool:
    if not cookie_header:
        return False
    cookies = {}
    for part in cookie_header.split(";"):
        if "=" in part:
            key, value = part.strip().split("=", 1)
            cookies[key] = value
    token = cookies.get(COOKIE_NAME)
    if not token or "." not in token:
        return False
    payload_b64, signature = token.rsplit(".", 1)
    try:
        payload = b64url_decode(payload_b64).decode("utf-8")
    except Exception:
        return False
    expected = sign_payload(payload).rsplit(".", 1)[1]
    if not hmac.compare_digest(signature, expected):
        return False
    try:
        expires_at, _nonce = payload.split(":", 1)
        return int(expires_at) >= int(time.time())
    except Exception:
        return False


def make_session() -> str:
    payload = f"{int(time.time()) + SESSION_TTL}:{secrets.token_urlsafe(18)}"
    return sign_payload(payload)


def resolve_static_path(request_path: str) -> Path | None:
    path = unquote(urlparse(request_path).path)
    if path == "/":
        path = "/index.html"
    candidate = (WEB_ROOT / path.lstrip("/")).resolve()
    if WEB_ROOT not in candidate.parents and candidate != WEB_ROOT:
        return None
    if candidate.is_dir():
        candidate = (candidate / "index.html").resolve()
    return candidate


LOGIN_PAGE = """<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>RyoMonitor</title>
<link rel="icon" href="/assets/logo.svg" type="image/svg+xml">
<style>
:root {
  color-scheme: dark;
  --bg: #111;
  --panel: #1b1b1b;
  --panel-soft: #1a1a1a;
  --text: #eee;
  --muted: #aaa;
  --line: #333;
  --accent: #77ff8a;
  --accent-strong: #5fe873;
  --danger: #ff8a8a;
}
* { box-sizing: border-box; }
body {
  margin: 0;
  min-height: 100vh;
  background: var(--bg);
  color: var(--text);
  font-family: system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;
}
.shell {
  max-width: 900px;
  margin: 0 auto;
  padding: 24px;
}
.header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  margin: 0 0 20px;
}
h1 {
  margin: 0 0 6px;
  font-size: 24px;
  line-height: 1.2;
}
.sub {
  margin: 0;
  color: var(--muted);
  font-size: 14px;
}
.lang-switch{
  display:inline-flex;
  gap:4px;
  padding:3px;
  border:1px solid var(--line);
  border-radius:999px;
  background:var(--panel-soft);
  flex:0 0 auto;
}
.lang-switch button{
  height:auto;
  border:0;
  border-radius:999px;
  background:transparent;
  color:var(--muted);
  cursor:pointer;
  font:inherit;
  font-size:12px;
  font-weight:400;
  padding:5px 9px;
}
.lang-switch button.active{
  background:var(--line);
  color:var(--text);
}
.card {
  width: min(100%, 420px);
  border: 1px solid var(--line);
  border-radius: 12px;
  background: var(--panel);
  padding: 16px;
  min-height: 118px;
}
.card-title {
  margin: 0 0 16px;
  font-size: 18px;
  font-weight: 700;
}
label {
  display: block;
  color: var(--muted);
  font-size: 13px;
  margin-bottom: 8px;
}
.field {
  width: 100%;
  height: 46px;
  border: 1px solid var(--line);
  border-radius: 8px;
  background: var(--panel-soft);
  color: var(--text);
  font: inherit;
  padding: 0 13px;
  outline: none;
}
.field:focus {
  border-color: var(--accent);
  box-shadow: 0 0 0 3px rgba(119,255,138,.12);
}
.row {
  display: flex;
  align-items: center;
  justify-content: flex-start;
  margin-top: 16px;
}
button {
  height: 42px;
  border: 1px solid var(--line);
  border-radius: 8px;
  background: var(--panel-soft);
  color: var(--text);
  font: inherit;
  font-weight: 700;
  padding: 0 20px;
  cursor: pointer;
}
button:hover {
  border-color: var(--accent);
}
.error {
  min-height: 20px;
  margin: 13px 0 0;
  color: var(--danger);
  font-size: 13px;
  line-height: 1.5;
}
@media(max-width:760px){
  .shell{
    padding: 20px;
  }

  .card{
    width: 100%;
  }
}
</style>
</head>
<body>
<main class="shell">
  <div class="header">
    <div>
      <h1>RyoMonitor</h1>
      <p class="sub" data-i18n="subtitle">请输入访问密码</p>
    </div>
    <div class="lang-switch" aria-label="Language">
      <button type="button" data-lang="zh">中文</button>
      <button type="button" data-lang="en">EN</button>
    </div>
  </div>
  <form class="card" method="post" action="/login">
    <div class="card-title" data-i18n="login">登录</div>
    <input type="hidden" name="next" value="__NEXT__">
    <label for="password" data-i18n="password">访问密码</label>
    <input class="field" id="password" name="password" type="password" autocomplete="current-password" autofocus required>
    <div class="row"><button type="submit" data-i18n="signIn">登录</button></div>
    <p class="error" data-error-key="__ERROR_KEY__"></p>
  </form>
</main>
<script>
const translations = {
  zh: {
    subtitle: "请输入访问密码",
    login: "登录",
    password: "访问密码",
    signIn: "登录",
    invalidPassword: "密码不正确"
  },
  en: {
    subtitle: "Enter the access password",
    login: "Sign in",
    password: "Access password",
    signIn: "Sign in",
    invalidPassword: "Incorrect password"
  }
};

function initialLanguage() {
  const saved = localStorage.getItem("ryo-monitor-lang");
  if (saved === "zh" || saved === "en") return saved;
  return navigator.language && navigator.language.toLowerCase().startsWith("zh") ? "zh" : "en";
}

let currentLang = initialLanguage();

function t(key) {
  return translations[currentLang][key] || translations.en[key] || key;
}

function applyLanguage() {
  document.documentElement.lang = currentLang === "zh" ? "zh-CN" : "en";
  document.querySelectorAll("[data-i18n]").forEach(el => {
    el.textContent = t(el.dataset.i18n);
  });
  document.querySelectorAll("[data-lang]").forEach(button => {
    button.classList.toggle("active", button.dataset.lang === currentLang);
  });
  const error = document.querySelector("[data-error-key]");
  if (error && error.dataset.errorKey) {
    error.textContent = t(error.dataset.errorKey);
  }
}

document.querySelectorAll("[data-lang]").forEach(button => {
  button.addEventListener("click", () => {
    currentLang = button.dataset.lang;
    localStorage.setItem("ryo-monitor-lang", currentLang);
    applyLanguage();
  });
});

applyLanguage();
</script>
</body>
</html>
"""


class Handler(BaseHTTPRequestHandler):
    server_version = "RyoMonAuth/1.0"

    def log_message(self, fmt: str, *args) -> None:
        print(f"{self.address_string()} - {fmt % args}", flush=True)

    def send_login(self, status=HTTPStatus.OK, error="", next_path="/"):
        body = (
            LOGIN_PAGE
            .replace("__ERROR_KEY__", html.escape(error))
            .replace("__NEXT__", html.escape(next_path, quote=True))
        ).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        if self.command != "HEAD":
            self.wfile.write(body)

    def redirect(self, location: str):
        self.send_response(HTTPStatus.SEE_OTHER)
        self.send_header("Location", location)
        self.send_header("Content-Length", "0")
        self.end_headers()

    def do_GET(self):
        parsed = urlparse(self.path)
        path = parsed.path
        if path == "/healthz":
            body = b"ok\n"
            self.send_response(HTTPStatus.OK)
            self.send_header("Content-Type", "text/plain; charset=utf-8")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        if path == "/logout":
            self.send_response(HTTPStatus.SEE_OTHER)
            self.send_header("Location", "/login")
            self.send_header("Set-Cookie", f"{COOKIE_NAME}=; Path=/; Max-Age=0; HttpOnly; Secure; SameSite=Lax")
            self.send_header("Content-Length", "0")
            self.end_headers()
            return
        if path == "/login":
            if valid_session(self.headers.get("Cookie")):
                self.redirect("/")
                return
            next_path = parse_qs(parsed.query).get("next", ["/"])[0]
            self.send_login(next_path=next_path if next_path.startswith("/") else "/")
            return
        if path.startswith("/assets/"):
            self.serve_static()
            return
        if not valid_session(self.headers.get("Cookie")):
            self.redirect(f"/login?next={quote(self.path, safe='/?=&')}")
            return
        self.serve_static()

    def do_HEAD(self):
        self.do_GET()

    def do_POST(self):
        if urlparse(self.path).path != "/login":
            self.send_error(HTTPStatus.NOT_FOUND)
            return
        length = int(self.headers.get("Content-Length", "0"))
        raw = self.rfile.read(min(length, 4096)).decode("utf-8", errors="replace")
        form = parse_qs(raw)
        password = form.get("password", [""])[0]
        next_path = form.get("next", ["/"])[0]
        if not next_path.startswith("/"):
            next_path = "/"
        if not verify_password(password):
            time.sleep(0.8)
            self.send_login(HTTPStatus.UNAUTHORIZED, "invalidPassword", next_path)
            return
        self.send_response(HTTPStatus.SEE_OTHER)
        self.send_header("Location", next_path)
        self.send_header("Set-Cookie", f"{COOKIE_NAME}={make_session()}; Path=/; Max-Age={SESSION_TTL}; HttpOnly; Secure; SameSite=Lax")
        self.send_header("Content-Length", "0")
        self.end_headers()

    def serve_static(self):
        target = resolve_static_path(self.path)
        if target is None or not target.exists() or not target.is_file():
            self.send_error(HTTPStatus.NOT_FOUND)
            return
        content_type = mimetypes.guess_type(target.name)[0] or "application/octet-stream"
        self.send_response(HTTPStatus.OK)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(target.stat().st_size))
        if target.name == "status.json":
            self.send_header("Cache-Control", "no-store")
        else:
            self.send_header("Cache-Control", "private, max-age=30")
        self.end_headers()
        if self.command != "HEAD":
            with target.open("rb") as file:
                while chunk := file.read(1024 * 128):
                    self.wfile.write(chunk)


def main():
    WEB_ROOT.mkdir(parents=True, exist_ok=True)
    server = ThreadingHTTPServer((HOST, PORT), Handler)
    print(f"listening on {HOST}:{PORT}, serving {WEB_ROOT}", flush=True)
    server.serve_forever()


if __name__ == "__main__":
    main()

package hub

import _ "embed"

//go:embed icon.png
var iconPNG []byte

// landingPage — что видно, если открыть адрес сервера в браузере.
const landingPage = `<!doctype html>
<html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>FruityHub — сервер</title><link rel="icon" type="image/png" href="/icon.png">
<style>
*{box-sizing:border-box}
html,body{height:100%;margin:0}
body{font:16px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",Inter,Roboto,sans-serif;color:#f1f2f4;
  background:radial-gradient(80% 60% at 50% 0%,#2a1f10 0%,#121317 55%,#0d0e11 100%);display:flex;align-items:center;justify-content:center;padding:24px}
.card{width:min(440px,100%);text-align:center;padding:40px 32px 32px;border:1px solid rgba(255,255,255,.10);border-radius:20px;
  background:rgba(22,23,28,.72);box-shadow:0 24px 70px rgba(0,0,0,.45);backdrop-filter:blur(18px)}
img{width:128px;height:128px;filter:drop-shadow(0 10px 26px rgba(0,0,0,.55));animation:float 5s ease-in-out infinite}
h1{margin:14px 0 4px;font-size:30px;letter-spacing:.2px}
.status{display:inline-flex;align-items:center;gap:9px;margin:14px 0 6px;padding:8px 16px;border-radius:999px;
  background:rgba(76,195,138,.12);border:1px solid rgba(76,195,138,.35);color:#7fe0b0;font-weight:600}
.dot{width:9px;height:9px;border-radius:50%;background:#4cc38a;box-shadow:0 0 0 0 rgba(76,195,138,.6);animation:pulse 2s infinite}
.hint{color:#a3a7b0;font-size:14px;margin:8px 0 22px}
.by{color:#a3a7b0;font-size:14px;margin-top:22px;padding-top:18px;border-top:1px solid rgba(255,255,255,.08)}
.by b{color:#f1f2f4}
a.gh{display:inline-flex;align-items:center;gap:8px;margin-top:12px;padding:9px 16px;border-radius:10px;color:#1a1205;
  background:linear-gradient(135deg,#f7a531,#e0662c);font-weight:700;text-decoration:none}
a.gh:hover{filter:brightness(1.08)}
svg{width:18px;height:18px}
@keyframes pulse{0%{box-shadow:0 0 0 0 rgba(76,195,138,.55)}70%{box-shadow:0 0 0 10px rgba(76,195,138,0)}100%{box-shadow:0 0 0 0 rgba(76,195,138,0)}}
@keyframes float{0%,100%{transform:translateY(0)}50%{transform:translateY(-6px)}}
@media (prefers-reduced-motion:reduce){img,.dot{animation:none}}
</style></head>
<body><main class="card">
<img src="/icon.png" alt="FruityHub">
<h1>FruityHub</h1>
<div class="status"><span class="dot"></span>Сервер работает</div>
<div class="hint">Совместная работа над проектами FL Studio. Этот адрес вписывается в приложение FruityHub.</div>
<div class="by">2026 by <b>@tetraminomusic</b><br>
<a class="gh" href="https://github.com/tetraminomusic/FruityHub" target="_blank" rel="noopener">
<svg viewBox="0 0 16 16" fill="currentColor"><path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.013 8.013 0 0016 8c0-4.42-3.58-8-8-8z"/></svg>
FruityHub на GitHub</a></div>
</main></body></html>`

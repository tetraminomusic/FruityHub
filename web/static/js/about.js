// «О программе» (нажатие на логотип) и заставка при запуске.

function openAbout() {
  const v = S.state?.version || '';
  const m =
    modal(`<div class="about"><img src="/assets/icon?v=${Date.now()}" alt=""><h2>FruityHub</h2>${v ? `<div class="ver">версия ${esc(v)}</div>` : ''}
    <div class="by">2026 by <b>@tetraminomusic</b></div>
    <div class="row" style="justify-content:center"><button class="primary" id="gh">GitHub ↗</button><button id="ok">Закрыть</button></div>
    <div class="muted small mono" style="margin-top:10px">${REPO_URL.replace('https://', '')}</div></div>`);
  m.querySelector('.modal').style.width = 'min(380px,92vw)';
  m.querySelector('#gh').onclick = () => api('/api/openurl', { url: REPO_URL }).catch(() => window.open(REPO_URL));
  m.querySelector('#ok').onclick = () => m.close();
}
function hideSplash() {
  const sp = $('#splash');
  if (!sp) return;
  sp.classList.add('out');
  setTimeout(() => sp.remove(), 300);
}

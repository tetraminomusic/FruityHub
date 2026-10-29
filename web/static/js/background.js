// Фон окна и его затемнение.

function setDim(v) {
  document.documentElement.style.setProperty('--dim', (Math.max(0, Math.min(90, v)) / 100).toFixed(2));
}
function reloadBg(stamp) {
  const u = '/assets/bg?v=' + (stamp || Date.now());
  const st =
    document.getElementById('bgstyle') ||
    document.head.appendChild(Object.assign(document.createElement('style'), { id: 'bgstyle' }));
  st.textContent = `body::before,body.lite::before{background-image:url('${u}')}`;
}

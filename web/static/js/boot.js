// Запуск интерфейса. Этот файл подключается последним — к этому моменту все функции уже объявлены.
initCore();
initDrafts();
initDragDrop();
initKeyboard();

setInterval(pollNotes, 6000);
document.addEventListener('visibilitychange', () => {
  if (!document.hidden) pollNotes();
});
// вернулся в окно — перечитать открытый проект (мог поменяться в FL)
window.addEventListener('focus', () => {
  if (S.sel && !document.querySelector('.modal-bg')) loadProject(S.sel);
});

// Windows: запущен не из папки программы — сначала окно установки (до знакомства и регистрации)
api('/api/state')
  .then(st => {
    if (st.install && (st.install.show || st.install.uninstall)) {
      S.state = st;
      S.installer = true;
    }
  })
  .catch(() => {})
  .then(() => refreshAll())
  .then(() => {
    if (S.installer) openInstaller();
    hideSplash();
    applyLang();
    pollNotes();
  })
  .catch(e => {
    hideSplash();
    $('#main').innerHTML = '<div class="badbox">' + esc(e.message) + '</div>';
  });

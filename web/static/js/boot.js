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
let focusRefresh;
window.addEventListener('focus', () => {
  clearTimeout(focusRefresh);
  focusRefresh = setTimeout(() => {
    if (S.sel && !document.hidden && !document.querySelector('.modal-bg')) loadProject(S.sel);
  }, 250);
});

// Состояние загружается один раз; окно показываем до фоновых проверок.
refreshAll()
  .then(() => {
    if (S.installer) openInstaller();
    hideSplash();
    applyLang();
    pollNotes();
    setTimeout(() => refreshAll().catch(() => {}), 150);
  })
  .catch(e => {
    hideSplash();
    $('#main').innerHTML = '<div class="badbox">' + esc(e.message) + '</div>';
  });

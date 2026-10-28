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

refreshAll()
  .then(() => {
    hideSplash();
    applyLang();
    pollNotes();
  })
  .catch(e => {
    hideSplash();
    $('#main').innerHTML = '<div class="badbox">' + esc(e.message) + '</div>';
  });

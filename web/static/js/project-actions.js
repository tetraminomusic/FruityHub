// Состояние определяет акцент; привычное расположение действий сохраняется.
function projectActionState(p, missing, pluginProblems) {
  if (p.behind > 0) return { primary: 'receive', label: 'Получить изменения', status: 'На сервере есть новая версия проекта' };
  if (missing || pluginProblems) return { primary: 'prepare', label: 'Подготовить проект к работе', status: 'Для работы нужно подготовить файлы или плагины' };
  if (p.ahead > 0 || (p.changes || []).some(c => !c.hidden)) return { primary: 'send', label: 'Отправить изменения', status: 'Твои изменения ещё не отправлены' };
  if (p.lock && p.lock.user !== p.me) return { primary: null, status: 'Сейчас работает ' + p.lock.user };
  return { primary: 'launch', label: 'Открыть в FL Studio', status: p.lock ? 'Проект в работе у тебя' : 'Всё актуально, проект свободен' };
}
function mainProjectAction(action) {
  if (action === 'receive') return receive();
  if (action === 'send') return sendDialog();
  if (action === 'launch') return launchProject();
  if (action === 'prepare') {
    const missing = (S.analysis?.analysis?.samples || []).some(s => s.status === 'missing');
    setTab(missing ? 'samples' : 'plugins');
  }
}
function projectMore(event) {
  popMenu(event, [
    ['Переименовать…', () => renameProject()],
    ['Настройки проекта', () => setTab('settings')],
    ['Открыть папку', () => api('/api/project/open', { path: S.sel })],
    [(S.state.projects || []).find(p => p.path === S.sel)?.pinned ? 'Открепить проект' : 'Закрепить проект', () => togglePin(S.sel)],
    '-',
    ['Убрать из списка', () => removeProject()],
    ['<span class="danger-text">Удалить файлы проекта…</span>', () => trashProject(S.sel)],
    ['<span class="danger-text">Удалить репозиторий…</span>', () => deleteRepoDialog()],
  ]);
}

// Клавиши: ↑/↓ — проекты, Tab — между папками, Esc — закрыть окно, ⌘/Ctrl+N, ⌘/Ctrl+O.

function visibleProjects() {
  return [...document.querySelectorAll('#plist .pitem')].map(el => el.dataset.path);
}
function scrollSelIntoView() {
  const el = document.querySelector('#plist .pitem.sel');
  if (el) el.scrollIntoView({ block: 'nearest' });
}
function topModal() {
  const ms = document.querySelectorAll('#modals .modal-bg');
  return ms[ms.length - 1];
}
function groupStarts() {
  // первый проект каждой папки (и общего списка) — для Tab
  const out = [];
  document.querySelectorAll('#plist .gbody, #plist .topzone').forEach(z => {
    const f = z.querySelector('.pitem');
    if (f) out.push(f.dataset.path);
  });
  return out;
}

function initKeyboard() {
  document.addEventListener('keydown', e => {
    const mod = e.metaKey || e.ctrlKey,
      k = e.key.toLowerCase();
    const typing =
      /^(input|textarea|select)$/i.test(document.activeElement?.tagName || '') ||
      document.activeElement?.isContentEditable;
    if (e.key === 'Escape') {
      const menu = document.querySelector('.menu');
      if (menu) {
        menu.remove();
        e.preventDefault();
        return;
      }
      const m = topModal();
      if (m && !m.busy) {
        e.preventDefault();
        m.close();
        return;
      }
      return;
    }
    if (mod && e.shiftKey && (k === 'n' || e.code === 'KeyN')) {
      e.preventDefault();
      if (!topModal()) newGroup();
      return;
    }
    if (mod && !e.shiftKey && (k === 'n' || e.code === 'KeyN')) {
      e.preventDefault();
      if (!topModal()) addProject();
      return;
    }
    if (mod && !e.shiftKey && (k === 'o' || e.code === 'KeyO')) {
      e.preventDefault();
      if (S.sel && !topModal()) api('/api/project/open', { path: S.sel }).catch(fail);
      return;
    }
    if (typing || topModal() || mod || e.altKey) return;
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      if (S.side === 'av') return;
      const list = visibleProjects();
      if (!list.length) return;
      e.preventDefault();
      let i = list.indexOf(S.sel);
      i = i < 0 ? 0 : Math.max(0, Math.min(list.length - 1, i + (e.key === 'ArrowDown' ? 1 : -1)));
      if (list[i] !== S.sel) selectProject(enc(list[i]));
    } else if (e.key === 'Tab') {
      if (S.side === 'av') return;
      const starts = groupStarts();
      if (!starts.length) return;
      e.preventDefault();
      const list = visibleProjects(),
        cur = list.indexOf(S.sel);
      let idx;
      if (!e.shiftKey) {
        idx = starts.findIndex(p => list.indexOf(p) > cur);
        if (idx < 0) idx = 0;
      } else {
        const before = starts.filter(p => list.indexOf(p) < cur);
        idx = before.length ? starts.indexOf(before[before.length - 1]) : starts.length - 1;
        // если стоим не на первом проекте папки — сначала к её началу
        const own = starts.filter(p => list.indexOf(p) <= cur).pop();
        if (own && own !== S.sel && e.shiftKey) idx = starts.indexOf(own);
      }
      selectProject(enc(starts[idx]));
    }
  });
}

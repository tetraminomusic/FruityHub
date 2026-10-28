// Статистика «Время в FL Studio» во вкладке «Профиль»: считается по счётчику времени в самих .flp
// (FL пишет его в файл при сохранении), разница между версиями в истории — автору версии.

function fmtDur(sec) {
  sec = Math.max(0, Math.round(sec || 0));
  const h = Math.floor(sec / 3600),
    m = Math.floor((sec % 3600) / 60);
  if (h && m) return `${h} ч ${m} мин`;
  if (h) return `${h} ч`;
  return `${m} мин`;
}

async function loadFlStats(el, fresh) {
  if (!el) return;
  el.innerHTML = '<div class="muted small">Считаю время по истории проектов…</div>';
  let st;
  try {
    st = await api('/api/stats' + (fresh ? '?fresh=1' : ''));
  } catch (e) {
    el.innerHTML = `<div class="muted small">Не получилось посчитать: ${esc(e.message || e)}</div>`;
    return;
  }
  const tile = (v, label) => `<div class="stt"><b>${fmtDur(v)}</b><span class="muted small">${label}</span></div>`;
  const weeks = st.weeks || [];
  const max = Math.max(1, ...weeks.map(w => w.me + w.others));
  const dm = d => {
    const x = new Date(d * 1000);
    return `${String(x.getDate()).padStart(2, '0')}.${String(x.getMonth() + 1).padStart(2, '0')}`;
  };
  const bars = weeks
    .map(w => {
      const hMe = (w.me / max) * 100,
        hO = (w.others / max) * 100;
      return `<div class="stw" title="Неделя с ${dm(w.start)}: ты — ${fmtDur(w.me)}, друзья — ${fmtDur(w.others)}"><div class="stb"><i class="o" style="height:${hO}%"></i><i class="m" style="height:${hMe}%"></i></div><span>${dm(w.start)}</span></div>`;
    })
    .join('');
  const projs = (st.projects || [])
    .map(p => {
      const others = (p.people || []).filter(x => !x.me);
      return `<div class="stp"><div class="grow"><b>${esc(p.name)}</b><div class="muted small">${others.length ? 'Друзья: ' + others.map(x => `${esc(x.nick)} — ${fmtDur(x.sec)}`).join(', ') : 'Работал только ты'}${p.fl ? ` · всего в FL по файлу проекта: ${fmtDur(p.fl)}` : ''}</div></div><b class="stme">${fmtDur(p.me)}</b></div>`;
    })
    .join('');
  if (!st.all && !(st.projects || []).some(p => p.total)) {
    el.innerHTML = `<div class="muted small">Пока нечего считать: время появится, когда в проектах будут сохранённые в FL версии .flp.</div>`;
    return;
  }
  el.innerHTML = `<div class="sttiles">${tile(st.today, 'сегодня')}${tile(st.week, 'эта неделя')}${tile(st.month, 'этот месяц')}${tile(st.all, `всего · дней: ${st.days}`)}</div>
    <div class="stchart">${bars}</div>
    <div class="row small muted" style="gap:14px;margin:2px 0 8px"><span><i class="stdot m"></i>ты</span><span><i class="stdot o"></i>друзья в твоих проектах</span><span class="spacer"></span><a id="strf">пересчитать</a></div>
    <div class="stprojs">${projs}</div>
    <div class="muted small" style="margin-top:6px">Время берётся у самой FL Studio: счётчик «сколько проект был открыт» (Project info) сохраняется в .flp, и прибавка между версиями в истории записывается тому, кто эту версию отправил. Твоё ещё не сохранённое в историю тоже учтено. Время до FruityHub записано тому, кто добавил проект.</div>`;
  el.querySelector('#strf').onclick = () => loadFlStats(el, true);
}

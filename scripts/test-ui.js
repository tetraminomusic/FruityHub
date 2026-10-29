// Regression checks without a browser, a live account or project files.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const read = name => fs.readFileSync(`web/static/js/${name}.js`, 'utf8');
const screen = { innerHTML: '' };
const context = {
  S: { state: { platform: 'windows-amd64', projects: [] }, sel: '/test', notes: {}, comments: { comments: [], status: {} } },
  $: () => screen, enc: encodeURIComponent, esc: value => String(value ?? ''), fmtTime: String,
  layoutBanners: () => '', dbox: () => '', renderTab: () => '', ICON_REFRESH: '',
};
vm.createContext(context);
vm.runInContext(read('project-actions') + '\n' + read('project') + '\n' + read('i18n'), context);
const base = { path: '/test', name: 'Test', exists: true, isRepo: true, adopted: true, me: 'me', ahead: 0, behind: 0, changes: [], fl: {} };
for (const scenario of [
  { primary: 'launch' },
  { behind: 1, primary: 'receive' },
  { changes: [{ path: 'song.flp', kind: 'modified' }], primary: 'send' },
  { lock: { user: 'friend' }, primary: null },
  { lock: { user: 'me' }, primary: 'launch' },
  { missing: 1, primary: 'prepare' },
]) {
  context.S.proj = { ...base, ...scenario };
  context.S.analysis = { analysis: { samples: scenario.missing ? [{ status: 'missing' }] : [], audio: [] }, plugins: [] };
  assert.equal(context.projectActionState(context.S.proj, scenario.missing || 0, 0).primary, scenario.primary);
  context.renderMainNow();
  const html = screen.innerHTML;
  const receive = html.indexOf('onclick="receive()"');
  const take = html.indexOf(scenario.lock?.user === 'me' ? 'onclick="unlock()"' : 'onclick="takeLock(');
  const send = html.indexOf('onclick="sendDialog()"');
  const launch = html.indexOf('onclick="launchProject()"');
  assert.ok(receive >= 0 && receive < take && take < send && send < launch, 'action positions changed');
  assert.ok(!html.includes(' disabled'), 'state alone disabled a project action');
  assert.ok(html.includes('card launch'), 'separate launch panel missing');
}
assert.equal(context.trText('Ctrl/⌘+S; ⌘/Ctrl + Enter; Cmd+N'), 'Ctrl+S; Ctrl + Enter; Ctrl+N');
context.S.state.platform = 'darwin-arm64';
assert.equal(context.trText('Ctrl/⌘+S; Ctrl+C; ⌘/Ctrl + Enter'), '⌘+S; ⌘+C; ⌘ + Enter');
let calls = 0;
context.api = async path => { calls++; assert.equal(path, '/api/pickfolder'); return { path: '' }; };
context.uiPrompt = () => { throw new Error('cancelled folder picker opened a path dialog'); };
context.addProject().then(() => {
  assert.equal(calls, 1);
  console.log('UI checks passed: stable actions, state priorities, platform shortcuts, cancelled folder picker.');
}).catch(error => { console.error(error); process.exitCode = 1; });

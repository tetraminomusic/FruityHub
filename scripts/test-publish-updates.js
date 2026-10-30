// Проверяем публикацию только на временных файлах, без подключения к серверу.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const crypto = require('node:crypto');
const { spawnSync } = require('node:child_process');
const root = fs.mkdtempSync(path.join(os.tmpdir(), 'fruityhub-publish-'));
const source = path.join(root, 'source');
const dest = path.join(root, 'updates');
const files = {
  'windows-amd64': ['FruityHub.exe', 'exe'],
  'darwin-arm64': ['FruityHub-mac-AppleSilicon.zip', 'zip'],
  'darwin-amd64': ['FruityHub-mac-Intel.zip', 'zip'],
};
const script = path.resolve(__dirname, '../deploy/publish-updates.sh');
const run = () => spawnSync('bash', [script, source, dest], { encoding: 'utf8' });
try {
  fs.mkdirSync(source);
  fs.mkdirSync(dest);
  const old = '{"version":"0.9.23","files":{}}\n';
  fs.writeFileSync(path.join(dest, 'manifest.json'), old);
  fs.writeFileSync(path.join(dest, 'old.exe'), 'previous download');
  fs.writeFileSync(path.join(source, 'VERSION'), '0.10.0\n');
  fs.writeFileSync(path.join(source, 'NOTES'), 'Быстрее "FLP" \\ Git\nНовая строка\tтекст');
  for (const [input] of Object.values(files)) fs.writeFileSync(path.join(source, input), input);
  fs.unlinkSync(path.join(source, 'FruityHub.exe'));
  assert.notEqual(run().status, 0, 'incomplete package must fail');
  assert.equal(fs.readFileSync(path.join(dest, 'manifest.json'), 'utf8'), old);
  fs.writeFileSync(path.join(source, 'FruityHub.exe'), 'Windows fixture');
  fs.mkdirSync(path.join(dest, '.publish-lock'));
  assert.notEqual(run().status, 0, 'concurrent publication must fail');
  assert.equal(fs.readFileSync(path.join(dest, 'manifest.json'), 'utf8'), old);
  fs.rmdirSync(path.join(dest, '.publish-lock'));
  let result = run();
  assert.equal(result.status, 0, result.stderr + result.stdout);
  const manifestText = fs.readFileSync(path.join(dest, 'manifest.json'), 'utf8');
  const manifest = JSON.parse(manifestText);
  assert.equal(manifest.version, '0.10.0');
  assert.equal(manifest.notes, 'Быстрее "FLP" \\ Git Новая строка текст');
  assert.equal(fs.readFileSync(path.join(dest, 'manifest.previous.json'), 'utf8'), old);
  assert.equal(fs.readFileSync(path.join(dest, 'old.exe'), 'utf8'), 'previous download');
  for (const [platform, [input, ext]] of Object.entries(files)) {
    const entry = manifest.files[platform];
    assert.equal(entry.file, `${platform}-0.10.0.${ext}`);
    const content = fs.readFileSync(path.join(dest, entry.file));
    assert.deepEqual(content, fs.readFileSync(path.join(source, input)));
    assert.equal(entry.size, content.length);
    assert.equal(entry.sha256, crypto.createHash('sha256').update(content).digest('hex'));
  }
  fs.writeFileSync(path.join(source, 'FruityHub.exe'), 'different build');
  assert.notEqual(run().status, 0, 'different build of published version must fail');
  assert.equal(fs.readFileSync(path.join(dest, 'manifest.json'), 'utf8'), manifestText);
  fs.writeFileSync(path.join(source, 'VERSION'), '../../bad');
  assert.notEqual(run().status, 0, 'invalid version must fail');
  assert.equal(fs.readFileSync(path.join(dest, 'manifest.json'), 'utf8'), manifestText);
  assert.equal(fs.readdirSync(dest).filter(name => name.startsWith('.publish.')).length, 0);
  console.log('Update publishing checks passed');
} finally {
  fs.rmSync(root, { recursive: true, force: true });
}

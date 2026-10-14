import assert from 'node:assert/strict';

export async function testModels(page, openSettings) {
  await openSettings('AI 配置');
  await page.getByRole('button', { name: '添加 AI 配置', exact: true }).first().click();
  const form = page.getByRole('dialog');
  await form.getByLabel('配置名称', { exact: true }).fill('多个模型测试');
  await form.getByLabel('AI 后端', { exact: true }).selectOption('model');
  await form.getByLabel(/^模型服务地址/).fill('http://models.internal/v1');
  await form.getByLabel('默认模型名称', { exact: true }).fill('model-a');
  await form.getByLabel('可选模型', { exact: true }).fill('model-b\nmodel-c, model-b');
  await form.getByRole('button', { name: '保存', exact: true }).click();
  await form.waitFor({ state: 'hidden' });
  const profile = await page.evaluate(() => window.__test.data.aiProfiles.find(p => p.name === '多个模型测试'));
  assert.deepEqual(profile.models, ['model-b', 'model-c']);
  await page.getByRole('button', { name: '返回工作区', exact: false }).click();
  await page.evaluate(() => { window.__test.trusted = true; });
  await page.getByRole('button', { name: '新建会话', exact: true }).first().click();
  const dialog = page.getByRole('dialog');
  await dialog.getByLabel('服务器', { exact: true }).selectOption('host-one');
  await dialog.getByLabel('AI 配置', { exact: true }).selectOption(profile.id);
  await dialog.getByRole('button', { name: '创建对话', exact: true }).click();
  await dialog.waitFor({ state: 'hidden' });
  await page.getByRole('button', { name: '连接并开始', exact: true }).click();
  const model = page.locator('.native-workbench:visible').getByLabel('当前 AI 模型', { exact: true });
  await model.waitFor();
  assert.equal(await model.inputValue(), 'model-a');
  assert.deepEqual(await model.locator('option').allTextContents(), ['model-a', 'model-b', 'model-c']);
  await model.selectOption('model-b');
  await page.waitForFunction(() => Object.values(window.__test.workbenches).some(s => s.model === 'model-b'));
  assert.equal(await model.inputValue(), 'model-b');
  const id = await page.evaluate(() => window.__test.sessions.at(-1).id);
  await page.evaluate((id) => {
    window.__test.workbenches[id].busy = true;
    window.__test.publishWorkbench(id);
  }, id);
  await page.waitForFunction(() => document.querySelector('.native-workbench:not([style*="display: none"]) select[aria-label="当前 AI 模型"]')?.disabled);
  assert.equal(await model.isDisabled(), true);
  await page.evaluate((id) => {
    window.__test.workbenches[id].busy = false;
    window.__test.publishWorkbench(id);
    window.__test.updateSession(id, 'ended');
  }, id);
  await page.locator('.native-workbench:visible').getByRole('button', { name: '重新连接', exact: true }).click();
  await page.waitForFunction(() => window.__test.sessions.at(-1).status === 'running');
  const restored = page.locator('.native-workbench:visible').getByLabel('当前 AI 模型', { exact: true });
  await restored.waitFor();
  assert.equal(await restored.inputValue(), 'model-b');
  assert.equal(await page.evaluate(() => window.__test.data.aiProfiles.find(p => p.name === '多个模型测试').model), 'model-a');
  assert.equal(await page.evaluate(() => window.__test.conversations[0].aiModel), 'model-b');
  console.log('PASS: multiple model form, live switching, busy state, persisted reconnect and unchanged profile default');
}

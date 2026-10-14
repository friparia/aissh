import assert from 'node:assert/strict';

export async function testTerminalTakeover(page) {
  await page.locator('.session-select').first().waitFor();
  await page.evaluate(() => { window.__test.trusted = true; });
  await page.getByRole('button', { name: '连接并继续', exact: true }).click();
  const workbench = page.locator('.native-workbench:visible');
  await workbench.getByText('服务器负载平稳', { exact: false }).waitFor();
  await page.evaluate(() => {
    const id = window.__test.sessions[0].id;
    window.__test.workbenches[id].busy = true;
    window.__test.workbenches[id].executing = true;
    window.__test.publishWorkbench(id);
  });
  const takeover = workbench.getByRole('button', { name: '取消 AI，接管终端', exact: true });
  assert.equal(await takeover.isDisabled(), false);
  await takeover.click();
  await workbench.getByText('终端已接管，请在右侧终端完成或退出审批', { exact: true }).waitFor();
  await page.waitForFunction(() => document.activeElement?.classList.contains('xterm-helper-textarea'));
  await page.keyboard.type('Q');
  await page.keyboard.press('Enter');
  await page.waitForFunction(() => window.__test.writes.join('').includes('Q\r'));
  const writes = await page.evaluate(() => window.__test.writes.join(''));
  assert.equal(writes, 'Q\r');
  assert.equal(await workbench.getByRole('button', { name: '发送', exact: true }).isDisabled(), true);
  assert.equal(await page.evaluate(() => window.__test.actions.at(-1).kind), 'terminal-takeover');
  console.log('PASS: terminal takeover remains reachable during AI/SSH execution, focuses terminal and forwards Q without injecting Ctrl+C');
}

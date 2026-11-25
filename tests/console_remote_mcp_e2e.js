// Run with playwright-cli in an authenticated desktop console session.
async page => {
  await page.route('**/v1/system/info', async route => {
    const response = await route.fetch();
    const data = await response.json();
    data.mode = 'remote';
    data.origin = 'https://mobile.example.test';
    data.mcp_executable = '/app/mobile-use';
    data.mcp_commands = {
      ...data.mcp_commands,
      codex: 'codex mcp add mobile-use -- /app/mobile-use mcp',
      claude: 'claude mcp add --transport stdio mobile-use -- /app/mobile-use mcp',
      http: 'https://mobile.example.test/mcp',
    };
    await route.fulfill({ response, json: data });
  });

  await page.reload();
  await page.getByRole('button', { name: 'MCP & Agents' }).click();
  await page.getByRole('heading', { name: 'Connect a local workstation' }).waitFor();

  const setup = await page.locator('.mcp-card pre code').first().textContent();
  const commands = await page.locator('.mcp-cards pre code').allTextContents();
  if (!setup.includes('https://mobile.example.test') || !setup.includes('<AGENT_TOKEN>')) {
    throw new Error('Remote profile setup must use the server origin and an agent-token placeholder.');
  }
  if (commands[0] !== 'codex mcp add mobile-use -- mobile-use mcp') {
    throw new Error(`Unexpected remote Codex command: ${commands[0]}`);
  }
  if (commands[1] !== 'claude mcp add --transport stdio mobile-use -- mobile-use mcp') {
    throw new Error(`Unexpected remote Claude command: ${commands[1]}`);
  }
  if (!commands[2].includes('"command": "mobile-use"')) {
    throw new Error('Desktop client JSON must use the workstation CLI.');
  }
  if (commands[2].includes('/app/mobile-use') || commands[3] !== 'https://mobile.example.test/mcp') {
    throw new Error('Remote setup exposed a server executable or used the wrong HTTP endpoint.');
  }

  return { setup, commands, status: 'passed' };
}

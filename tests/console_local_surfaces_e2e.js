// Run with playwright-cli in an authenticated workstation console session.
async page => {
  await page.unrouteAll();
  await page.reload();
  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByRole('button', { name: 'Devices' }).click();
  await page.getByRole('button', { name: '+ Add device' }).first().click();

  await page.getByRole('img', { name: 'QR code for pairing Android companion' }).waitFor();
  const code = await page.locator('.big-code').textContent();
  if (!/^[A-Z0-9]{4}-[A-Z0-9]{4}$/.test(code || '')) {
    throw new Error(`Unexpected companion pairing code: ${code}`);
  }

  await page.getByRole('tab', { name: 'Android Wi-Fi (ADB)' }).click();
  await page.getByRole('heading', { name: '1. Pair new phone (first time)' }).waitFor();
  await page.getByRole('heading', { name: '2. Connect paired phone' }).waitFor();

  await page.getByRole('tab', { name: 'Remote & iPhone' }).click();
  await page.getByText('Connect a remote Android/iPhone host (mTLS Connector)', { exact: true }).waitFor();
  const remoteInstructions = await page.locator('.remote-guide').innerText();
  if (!remoteInstructions.includes('mobile-use remote set --url https://mobile-use.example.com')) {
    throw new Error('Remote CLI setup instructions are missing.');
  }
  if (!remoteInstructions.includes('--appium http://127.0.0.1:4723')) {
    throw new Error('iPhone connector instructions are missing.');
  }

  const widths = await page.evaluate(() => ({
    viewport: document.documentElement.clientWidth,
    document: document.documentElement.scrollWidth,
  }));
  if (widths.viewport !== widths.document) throw new Error(`Mobile layout overflows horizontally: ${JSON.stringify(widths)}`);

  await page.getByRole('button', { name: 'Close' }).click();
  await page.getByRole('button', { name: 'Sessions' }).click();
  await page.getByText('No active sessions or requests.').waitFor();
  await page.getByRole('button', { name: 'Audit' }).click();
  await page.getByRole('table', { name: 'Workspace audit events' }).waitFor();

  return { pairingCodeVisible: true, wifiAdb: 'passed', remoteAndIphone: 'passed', widths, sessions: 'passed', audit: 'passed' };
}

// Run with playwright-cli in an authenticated workstation console session.
async page => {
  await page.unrouteAll();
  await page.reload();
  await page.setViewportSize({ width: 390, height: 844 });

  // The mobile navigation is a sheet too; select Devices before opening the add-device sheet.
  await page.getByRole('button', { name: 'Toggle navigation' }).click();
  const navigationLayout = await page.locator('[data-slot="sheet-content"]').evaluate(sheet => ({
    viewport: document.documentElement.clientWidth,
    document: document.documentElement.scrollWidth,
    sheet: sheet.clientWidth,
    sheetContent: sheet.scrollWidth,
  }));
  if (navigationLayout.document > navigationLayout.viewport + 1 || navigationLayout.sheetContent > navigationLayout.sheet + 1) {
    throw new Error('The mobile navigation sheet overflows: ' + JSON.stringify(navigationLayout));
  }
  await page.getByRole('button', { name: 'Devices' }).click();
  await page.keyboard.press('Escape');
  await page.getByRole('button', { name: 'Add device' }).first().click();

  await page.getByRole('img', { name: 'QR code for pairing Android companion' }).waitFor();
  const code = (await page.locator('code').first().textContent())?.trim() || '';
  if (!/^[23456789A-HJ-NP-Z]{4}-[23456789A-HJ-NP-Z]{4}$/.test(code)) {
    throw new Error('Unexpected companion pairing code: ' + code);
  }

  const layouts = [];
  for (const tab of ['Android companion', 'Wi-Fi ADB', 'Remote / iPhone']) {
    await page.getByRole('tab', { name: tab }).click();
    for (const width of [320, 390, 768, 1024, 1920]) {
      await page.setViewportSize({ width, height: 844 });
      const layout = await page.locator('[data-slot="sheet-content"]').evaluate(sheet => ({
        viewport: document.documentElement.clientWidth,
        document: document.documentElement.scrollWidth,
        sheet: sheet.clientWidth,
        sheetContent: sheet.scrollWidth,
      }));
      if (layout.document > layout.viewport + 1 || layout.sheetContent > layout.sheet + 1) {
        throw new Error(tab + ' overflows horizontally at ' + width + 'px: ' + JSON.stringify(layout));
      }
      layouts.push({ tab, width, ...layout });
    }
  }

  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByRole('tab', { name: 'Wi-Fi ADB' }).click();
  await page.getByRole('heading', { name: '1. Pair this computer' }).waitFor();
  await page.getByRole('heading', { name: '2. Connect paired phone' }).waitFor();

  await page.getByRole('tab', { name: 'Remote / iPhone' }).click();
  await page.getByRole('heading', { name: 'Remote Android host' }).waitFor();
  await page.getByRole('heading', { name: 'Remote server from this computer' }).waitFor();
  const remoteInstructions = await page.getByRole('tabpanel', { name: 'Remote / iPhone' }).innerText();
  if (!remoteInstructions.includes('mobile-use connector --endpoint')) {
    throw new Error('Remote connector setup instructions are missing.');
  }
  if (!remoteInstructions.includes('mobile-use remote set --url')) {
    throw new Error('Remote CLI setup instructions are missing.');
  }
  if (!remoteInstructions.includes('Appium and WebDriverAgent')) {
    throw new Error('iPhone connector guidance is missing.');
  }

  const cancellation = page.waitForRequest(request =>
    request.method() === 'DELETE' && /\/v1\/companion\/pairing\//.test(new URL(request.url()).pathname),
    { timeout: 5000 },
  );
  await page.getByRole('button', { name: 'Close' }).click();
  await cancellation;

  await page.getByRole('button', { name: 'Toggle navigation' }).click();
  await page.getByRole('button', { name: 'Sessions' }).click();
  await page.keyboard.press('Escape');
  await page.getByRole('heading', { name: 'No active sessions' }).waitFor();
  await page.getByRole('button', { name: 'Toggle navigation' }).click();
  await page.getByRole('button', { name: 'Audit log' }).click();
  await page.keyboard.press('Escape');
  await page.getByRole('heading', { name: 'No audit events yet' }).waitFor();

  return { pairingCodeVisible: true, closeRevokesPendingCode: true, wifiAdb: 'passed', remoteAndIphone: 'passed', layouts, sessions: 'passed', audit: 'passed' };
}

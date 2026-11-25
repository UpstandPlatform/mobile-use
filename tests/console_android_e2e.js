// Run with playwright-cli run-code in an authenticated console session.
async page => {
  const purpose = 'Verify Android console and maintenance';
  const capture = page.getByRole('button', { name: 'Capture screen' });
  const screenshot = page.getByRole('img', { name: 'Latest authorized screenshot of the phone' });
  const video = page.locator('video');
  const maintenance = page.getByText('App maintenance', { exact: true });
  const stop = page.getByRole('button', { name: 'Stop', exact: true });
  const selected = page.getByRole('region', { name: 'Device inventory' }).getByRole('button', { name: /Android · Virtual device Online/, pressed: true });
  await page.setViewportSize({ width: 1280, height: 820 });
  await selected.waitFor();

  await page.getByText('Request a session', { exact: true }).click();
  await page.getByRole('textbox', { name: 'Purpose' }).fill(purpose);
  await page.getByRole('checkbox', { name: 'Installed apps' }).check();
  await page.getByRole('button', { name: 'Request access' }).click();
  await stop.waitFor();

  try {
    await capture.click();
    await screenshot.waitFor();
    await page.getByRole('button', { name: 'Live video', exact: true }).click();
    await page.waitForFunction(() => {
      const screen = document.querySelector('video');
      return screen && screen.videoWidth > 0 && screen.currentTime > 0;
    }, undefined, { timeout: 25000 });
    const decoded = await video.evaluate(screen => ({ width: screen.videoWidth, height: screen.videoHeight }));

    await page.getByRole('button', { name: 'Pause', exact: true }).click();
    await video.waitFor({ state: 'detached' });
    await page.getByRole('button', { name: 'Resume', exact: true }).click();
    await capture.click();
    await screenshot.waitFor();

    await page.setViewportSize({ width: 390, height: 820 });
    await maintenance.click();
    await page.getByRole('combobox', { name: 'Approved app' }).selectOption('fixture-app');
    await page.getByRole('combobox', { name: 'Operation' }).selectOption('reset');
    const reset = page.getByRole('button', { name: 'Reset app data', exact: true });
    if (await reset.isEnabled()) throw new Error('Reset must require confirmation.');
    await page.getByLabel(/Type reset fixture-app to confirm/).fill('reset fixture-app');
    await reset.click();
    await page.getByRole('status').getByText('The app data was cleared.').waitFor();
    await screenshot.waitFor({ state: 'detached' });

    const widths = await page.evaluate(() => ({ viewport: document.documentElement.clientWidth, document: document.documentElement.scrollWidth }));
    if (widths.viewport !== widths.document) throw new Error('The narrow console has horizontal overflow.');
    return { decoded, widths, reset: 'passed', pauseResumeStop: 'passed' };
  } finally {
    if (await stop.isVisible()) await stop.click();
    await maintenance.waitFor({ state: 'detached' });
  }
}

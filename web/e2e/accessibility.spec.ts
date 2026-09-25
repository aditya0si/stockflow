import AxeBuilder from '@axe-core/playwright'
import { expect, login, test, uniqueCode } from './fixtures'

test('has no detectable accessibility violations on each tab', async ({ page }) => {
  await login(page)
  for (const name of ['Stock', 'Orders', 'Reconciliation']) {
    await page.getByRole('tab', { name }).click()
    const results = await new AxeBuilder({ page }).analyze()
    expect(
      results.violations,
      `${name} tab violations:\n${JSON.stringify(results.violations, null, 2)}`,
    ).toEqual([])
  }
})

test('tablist supports Arrow, Home, and End keys with focus management', async ({ page }) => {
  await login(page)
  const stockTab = page.getByRole('tab', { name: 'Stock' })
  const ordersTab = page.getByRole('tab', { name: 'Orders' })
  const reconciliationTab = page.getByRole('tab', { name: 'Reconciliation' })

  await stockTab.focus()
  await page.keyboard.press('ArrowRight')
  await expect(ordersTab).toHaveAttribute('aria-selected', 'true')
  await expect(ordersTab).toBeFocused()

  await page.keyboard.press('End')
  await expect(reconciliationTab).toHaveAttribute('aria-selected', 'true')
  await expect(reconciliationTab).toBeFocused()

  await page.keyboard.press('Home')
  await expect(stockTab).toHaveAttribute('aria-selected', 'true')
  await expect(stockTab).toBeFocused()
})

test('data tables expose captions', async ({ page }) => {
  await login(page)

  const code = uniqueCode('CAP')
  await page.getByLabel('Code', { exact: true }).fill(code)
  await page.getByLabel('Name', { exact: true }).fill('Caption Item')
  await page.getByLabel('Opening quantity', { exact: true }).fill('5')
  await page.getByRole('button', { name: 'Create SKU' }).click()
  await expect(page.getByRole('rowheader', { name: code })).toBeVisible()
  await expect(page.getByRole('table', { name: /SKU balances/i })).toBeVisible()

  await page.getByRole('tab', { name: 'Orders' }).click()
  await page.getByLabel('SKU 1').fill(code)
  await page.getByRole('button', { name: 'Submit order' }).click()
  await expect(page.getByText('Order submitted')).toBeVisible()
  await page.locator('.order-list button').first().click()
  await expect(page.getByRole('table', { name: /Order items/i })).toBeAttached()
  await expect(page.getByRole('table', { name: /inventory movements/i })).toBeAttached()
})

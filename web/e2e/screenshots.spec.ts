import { mkdirSync } from 'node:fs'
import path from 'node:path'
import { expect, login, test, uniqueCode } from './fixtures'

// These screenshots are generated from the running product, never mocked. They
// are gated so the normal suite does not rewrite committed images:
//   STOCKFLOW_CAPTURE_SCREENSHOTS=true npx playwright test screenshots
const capture = process.env.STOCKFLOW_CAPTURE_SCREENSHOTS === 'true'
const shotsDir = path.resolve(process.cwd(), '..', 'docs', 'screenshots')

test('capture real product screenshots', async ({ page }) => {
  test.skip(!capture, 'set STOCKFLOW_CAPTURE_SCREENSHOTS=true to regenerate screenshots')
  mkdirSync(shotsDir, { recursive: true })

  await login(page)

  const code = uniqueCode('SHOT')
  await page.getByLabel('Code', { exact: true }).fill(code)
  await page.getByLabel('Name', { exact: true }).fill('Screenshot Widget')
  await page.getByLabel('Opening quantity', { exact: true }).fill('12')
  await page.getByRole('button', { name: 'Create SKU' }).click()
  await expect(page.getByRole('rowheader', { name: code })).toBeVisible()

  await page.getByLabel('SKU code', { exact: true }).fill(code)
  await page.getByLabel('Quantity', { exact: true }).fill('4')
  await page.getByLabel('Reason', { exact: true }).fill('screenshot restock')
  await page.getByRole('button', { name: 'Receive' }).click()
  await expect(page.getByText(`Received 4 of ${code}`)).toBeVisible()
  await page.screenshot({ path: path.join(shotsDir, '01-stock.png'), fullPage: true })

  await page.getByRole('tab', { name: 'Orders' }).click()
  await page.getByLabel('SKU 1').fill(code)
  await page.getByLabel('Line quantity', { exact: true }).fill('2')
  await page.getByRole('button', { name: 'Submit order' }).click()
  await expect(page.getByText('Order submitted')).toBeVisible()
  await page.locator('.order-list button').first().click()
  await page.screenshot({ path: path.join(shotsDir, '02-orders.png'), fullPage: true })

  await page.getByRole('button', { name: 'pick', exact: true }).click()
  await page.getByRole('button', { name: 'pack', exact: true }).click()
  await page.getByRole('button', { name: 'ship', exact: true }).click()
  await expect(page.locator('#panel-orders .status.shipped').first()).toBeVisible()
  await page.screenshot({ path: path.join(shotsDir, '03-order-detail.png'), fullPage: true })

  await page.getByRole('tab', { name: 'Reconciliation' }).click()
  await page.getByRole('button', { name: 'Run report' }).click()
  await expect(page.getByText('Reconciliation run completed (report only)')).toBeVisible()
  await page.locator('.run-list button').first().click()
  await page.screenshot({ path: path.join(shotsDir, '04-reconciliation.png'), fullPage: true })
})

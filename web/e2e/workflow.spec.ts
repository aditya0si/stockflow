import { expect, login, test, uniqueCode } from './fixtures'

test('operator journey: stock, order, fulfilment, and reconciliation', async ({ page }) => {
  await login(page)

  // 1. Create a SKU with opening stock.
  const code = uniqueCode('E2E')
  await page.getByLabel('Code', { exact: true }).fill(code)
  await page.getByLabel('Name', { exact: true }).fill('E2E Widget')
  await page.getByLabel('Opening quantity', { exact: true }).fill('5')
  await page.getByRole('button', { name: 'Create SKU' }).click()
  await expect(page.getByText(`Created ${code}`)).toBeVisible()
  await expect(page.getByRole('rowheader', { name: code })).toBeVisible()

  // 2. Receive more stock.
  await page.getByLabel('SKU code', { exact: true }).fill(code)
  await page.getByLabel('Quantity', { exact: true }).fill('3')
  await page.getByLabel('Reason', { exact: true }).fill('e2e restock')
  await page.getByRole('button', { name: 'Receive' }).click()
  await expect(page.getByText(`Received 3 of ${code}`)).toBeVisible()

  // 3. Create an order.
  await page.getByRole('tab', { name: 'Orders' }).click()
  await page.getByLabel('SKU 1').fill(code)
  await page.getByLabel('Line quantity', { exact: true }).fill('2')
  await page.getByRole('button', { name: 'Submit order' }).click()
  await expect(page.getByText('Order submitted')).toBeVisible()

  // 4. Select the order and fulfil it.
  await page.locator('.order-list button').first().click()
  await expect(page.getByRole('button', { name: 'pick', exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'pick', exact: true }).click()
  await page.getByRole('button', { name: 'pack', exact: true }).click()
  await page.getByRole('button', { name: 'ship', exact: true }).click()
  await expect(page.locator('#panel-orders .status.shipped').first()).toBeVisible()

  // 5. Inspect fulfilment events and inventory movements.
  const ordersPanel = page.locator('#panel-orders')
  const events = ordersPanel.locator('.events')
  await expect(events.getByText('packed', { exact: true })).toBeVisible()
  await expect(events.getByText('shipped', { exact: true })).toBeVisible()
  const movements = ordersPanel.getByRole('table', { name: /inventory movements/i })
  await expect(movements.getByText('reservation', { exact: true })).toBeVisible()
  await expect(movements.getByText('release', { exact: true })).toBeVisible()
  await expect(movements.getByText('shipment', { exact: true })).toBeVisible()

  // 6. Run reconciliation and inspect the persisted run.
  await page.getByRole('tab', { name: 'Reconciliation' }).click()
  await page.getByRole('button', { name: 'Run report' }).click()
  await expect(page.getByText('Reconciliation run completed (report only)')).toBeVisible()
  await page.locator('.run-list button').first().click()
  await expect(page.getByText(/checks?,/)).toBeVisible()
  await expect(page.getByRole('table', { name: /findings/i })).toBeVisible()
})

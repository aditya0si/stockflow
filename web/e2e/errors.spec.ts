import { expect, login, test, uniqueCode } from './fixtures'

test('failed mutation preserves input and shows an actionable error', async ({ page }) => {
  await login(page)

  const missing = uniqueCode('MISSING')
  await page.getByLabel('SKU code', { exact: true }).fill(missing)
  await page.getByLabel('Quantity', { exact: true }).fill('2')
  await page.getByLabel('Reason', { exact: true }).fill('should fail')
  await page.getByRole('button', { name: 'Receive' }).click()

  const alert = page.getByRole('alert')
  await expect(alert).toBeVisible()
  await expect(alert).toContainText(missing)
  await expect(alert).toContainText('sku_not_found')

  // The form still holds what the operator typed, so it can be corrected.
  await expect(page.getByLabel('SKU code', { exact: true })).toHaveValue(missing)
  await expect(page.getByLabel('Quantity', { exact: true })).toHaveValue('2')
  await expect(page.getByLabel('Reason', { exact: true })).toHaveValue('should fail')

  // No success notice was shown as if the mutation completed.
  await expect(page.getByText(`Received 2 of ${missing}`)).toHaveCount(0)
})

test('sign-in with a wrong password shows an error without losing input', async ({ page }) => {
  await page.goto('/')
  await page.getByLabel('Username').fill('operator')
  await page.getByLabel('Password').fill('definitely-wrong')
  await page.getByRole('button', { name: 'Sign in' }).click()

  const alert = page.getByRole('alert')
  await expect(alert).toContainText('invalid_credentials')
  await expect(page.getByLabel('Username')).toHaveValue('operator')
})

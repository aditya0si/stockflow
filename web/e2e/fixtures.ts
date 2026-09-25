import { expect, test as base, type Page } from '@playwright/test'

export interface ErrorCollector {
  errors: string[]
}

// The fixture fails a test if the page logs a console error, throws an
// uncaught error, or fails a request (excluding deliberate aborts).
export const test = base.extend<{ pageErrors: ErrorCollector }>({
  pageErrors: async ({ page }, use) => {
    const errors: string[] = []
    page.on('console', (message) => {
      if (message.type() === 'error') {
        errors.push(`console error: ${message.text()}`)
      }
    })
    page.on('pageerror', (error) => {
      errors.push(`page error: ${error.message}`)
    })
    page.on('requestfailed', (request) => {
      const failure = request.failure()
      const text = failure?.errorText ?? 'unknown failure'
      if (text.includes('ERR_ABORTED')) {
        return
      }
      errors.push(`request failed: ${request.method()} ${request.url()} (${text})`)
    })
    await use({ errors })
    expect(errors, `browser errors:\n${errors.join('\n')}`).toEqual([])
  },
})

export { expect }

export async function login(
  page: Page,
  username = process.env.STOCKFLOW_E2E_USERNAME ?? 'operator',
  password = process.env.STOCKFLOW_E2E_PASSWORD ?? 'stockflow-demo',
) {
  await page.goto('/')
  await page.getByLabel('Username').fill(username)
  await page.getByLabel('Password').fill(password)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByRole('heading', { name: /StockFlow operator/i })).toBeVisible()
}

export function uniqueCode(prefix: string): string {
  return `${prefix}-${Date.now()}-${Math.floor(Math.random() * 1000)}`
}

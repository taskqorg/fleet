// Home card lookups for the checks (docs/design-system/pages/home.md). Every group shows one row of cards (OR-30), so a
// check that looks for a card by text can find it past the row, behind `+K more` in Waiting for you, when other checks of
// the same run left sessions or questions of their own. Look Home cards up through these helpers, never by text alone.
import type { Locator, Page } from 'playwright';

const settled = '[data-ui=home] :is([data-ui=session-card]:not(.skeleton), [data-ui=question-card], [data-ui=quiet-summary])';

/** Opens Waiting for you past its row when it has more cards than the row holds. */
export async function openWaiting(page: Page): Promise<void> {
  await page.locator(settled).first().waitFor();
  const more = page.locator('.home-group[aria-label="Waiting for you"]').getByRole('button', { name: /^\+\d+ more$/ });
  if (await more.count()) await more.click();
}

/**
 * The Home card (a session card or a question card) that contains `text`. When the card is not in the first row, it opens
 * Waiting for you past its row, then waits for the card. Only Waiting expands in place; the other groups link away.
 */
export async function homeCard(page: Page, text: string, timeout = 30_000): Promise<Locator> {
  const card = page.locator('[data-ui=home] :is([data-ui=session-card], [data-ui=question-card])', { hasText: text }).first();
  await page.locator(settled).first().waitFor({ timeout });
  const end = Date.now() + timeout;
  // Cards and questions arrive from separate reads: retry until the card shows, opening Waiting whenever it has more.
  while (!(await card.isVisible())) {
    if (Date.now() > end) throw new Error(`Home card "${text}" not found in the first row or past +K more`);
    await openWaiting(page);
    await page.waitForTimeout(200);
  }
  return card;
}

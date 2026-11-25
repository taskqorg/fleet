// The one way a walk or check script opens the deployed site in Playwright (AGENTS.md, "Opening the deployed web app"):
// a context that sends the Access service token to that origin only, and output that never carries a credential. A
// Playwright error (a TimeoutError's call log, a response dump) can quote request headers and cookies, among them
// CF_Authorization; such a script prints only an error's first line, with Cookie, Authorization, CF-Access-* and
// CF_Authorization values redacted (redact), and an error nothing caught ends the process the same way (guardOutput).
import type { Browser, BrowserContext, BrowserContextOptions } from 'playwright';

export const ORIGIN = new URL(process.env.FLEET_ORIGIN ?? 'https://fleet.ufobe.com').origin;

/** The first line of a message or error, with every credential value replaced by [redacted]. */
export function redact(e: unknown): string {
  return String((e as Error)?.message ?? e).split('\n')[0]
    // A header: its name, the separator, then the value up to the end of the line or the end of a quoted string.
    .replace(/((?:set-)?cookie|authorization|proxy-authorization|cf-access-[a-z-]+)(["']?\s*[:=]\s*["']?)[^\n"',}]+/gi, '$1$2[redacted]')
    // A Cloudflare Access cookie anywhere else.
    .replace(/(CF_Authorization|CF_AppSession|CF_Binding)=[^;\s"',}]+/g, '$1=[redacted]')
    .replace(/\b(bearer)\s+[A-Za-z0-9._~+/=-]+/gi, '$1 [redacted]')
    // The token itself, however it is quoted (a name/value header pair, a URL).
    .split(process.env.CF_ACCESS_CLIENT_SECRET || '\u0000never').join('[redacted]')
    .split(process.env.CF_ACCESS_CLIENT_ID || '\u0000never').join('[redacted]');
}

/** An error nothing caught ends the process with its redacted first line instead of Node's full dump. */
export function guardOutput() {
  const stop = (e: unknown) => { console.error(`stopped: ${redact(e)}`); process.exit(1); };
  process.on('uncaughtException', stop);
  process.on('unhandledRejection', stop);
}

/** The Access service token from the environment (sourced from ~/.config/ufobe-agents/site-access.env), or null. */
export function token(): { id: string; secret: string } | null {
  const id = process.env.CF_ACCESS_CLIENT_ID, secret = process.env.CF_ACCESS_CLIENT_SECRET;
  return id && secret ? { id, secret } : null;
}

/** A context that sends the token to ORIGIN only: requests to any other host are aborted. */
export async function prodContext(browser: Browser, cred: { id: string; secret: string }, opts: BrowserContextOptions = {}, origin = ORIGIN): Promise<BrowserContext> {
  const ctx = await browser.newContext({ ...opts, extraHTTPHeaders: { 'CF-Access-Client-Id': cred.id, 'CF-Access-Client-Secret': cred.secret } });
  const only = new URL(origin).origin;
  await ctx.route(u => new URL(u).origin !== only, r => r.abort());
  return ctx;
}

// Browser errors go to Bugsink, Fleet's error tracker (docs/designs/fleet-error-tracking.md), through the Sentry
// browser SDK. Events take Fleet's own tunnel POST /api/errors: same origin, so blockers keep them and no public ingest
// is needed; the coordinator checks the DSN and names the person (the SDK sends no personal data). Imported first by
// main.tsx, so the SDK is up before any other module runs. Without a configured DSN (config/bugsink.json) nothing starts.
import {
  init, captureException, breadcrumbsIntegration, browserApiErrorsIntegration, dedupeIntegration, functionToStringIntegration,
  globalHandlersIntegration, httpContextIntegration, linkedErrorsIntegration,
} from '@sentry/browser';
import bugsink from '../../config/bugsink.json';
import { OpError } from './api.ts';
import { AccessChanged } from './access.ts';
import { tabVersion } from './version.ts';

/** Not reported: an operation's own failure (OpError) is in the coordinator's spans, and an access change aborts reads on purpose. */
export const ignoreError = (e: unknown) => e instanceof OpError || e instanceof AccessChanged;

if (bugsink.web) {
  init({
    dsn: bugsink.web, tunnel: '/api/errors', environment: 'production',
    // No personal data from the browser (SDK 11's form of sendDefaultPii off): no user fields, cookies or bodies.
    dataCollection: { userInfo: false, cookies: false, httpBodies: [] },
    // Errors only: no sessions, tracing or replay.
    defaultIntegrations: [functionToStringIntegration(), browserApiErrorsIntegration(), breadcrumbsIntegration(), globalHandlersIntegration(),
      linkedErrorsIntegration(), dedupeIntegration(), httpContextIntegration()],
    beforeSend(event, hint) {
      if (ignoreError(hint.originalException)) return null;
      // The Fleet version this tab runs, as system_versions and the version chip show it; unknown until the first check.
      if (tabVersion.value) event.release = tabVersion.value;
      return event;
    },
  });
}

/** A render crash caught by the app's boundary (it is not an uncaught error, so the global handlers do not see it). */
export function captureRender(error: unknown) {
  if (bugsink.web && !ignoreError(error)) captureException(error, { tags: { source: 'render' } });
}

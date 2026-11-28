// Issuer entry point for a separate protected service. This module is never invoked by candidate CI.
import { createHash, sign } from 'node:crypto';
import { constants } from 'node:fs';
import { open } from 'node:fs/promises';
import { isAbsolute } from 'node:path';
import { verifyProtectedDesignCandidate } from './design-protected.mjs';

const repo = '/repos/taskqorg/fleet';
const name = 'design-approval';
const permissions = { checks: 'write', contents: 'read', pull_requests: 'read', metadata: 'read' };

export function designGitHubConfig(env = process.env) {
  const appId = Number(env.FLEET_DESIGN_GITHUB_APP_ID);
  const installationId = Number(env.FLEET_DESIGN_GITHUB_INSTALLATION_ID);
  const keyFile = env.FLEET_DESIGN_GITHUB_APP_KEY_FILE;
  if (![appId, installationId].every(id => Number.isSafeInteger(id) && id > 0) || !keyFile || !isAbsolute(keyFile))
    throw new Error('The protected design issuer needs an App ID, installation ID and absolute key file');
  return { appId, installationId, keyFile };
}

async function gitHubJWT({ appId, keyFile }) {
  let file;
  try {
    file = await open(keyFile, constants.O_RDONLY | constants.O_NOFOLLOW);
    const state = await file.stat();
    if (!state.isFile() || (state.mode & 0o077) !== 0) throw new Error('private key must be service-private');
    const now = Math.floor(Date.now() / 1000);
    const encode = value => Buffer.from(JSON.stringify(value)).toString('base64url');
    const jwt = encode({ alg: 'RS256', typ: 'JWT' }) + '.' + encode({ iss: String(appId), iat: now - 60, exp: now + 540 });
    return jwt + '.' + sign('RSA-SHA256', Buffer.from(jwt), await file.readFile()).toString('base64url');
  } catch {
    throw new Error('Could not load the private GitHub App signing key');
  } finally {
    await file?.close();
  }
}

async function github(path, token, body) {
  const response = await fetch('https://api.github.com' + path, {
    method: body ? 'POST' : 'GET',
    redirect: 'error',
    headers: { Authorization: 'Bearer ' + token, Accept: 'application/vnd.github+json',
      'Content-Type': 'application/json', 'X-GitHub-Api-Version': '2026-03-10' },
    body: body ? JSON.stringify(body) : undefined,
  });
  if (!response.ok) throw new Error('GitHub design checker returned HTTP ' + response.status);
  return response.json();
}

async function fleetInstallation(config) {
  const jwt = await gitHubJWT(config);
  const installed = await github(repo + '/installation', jwt);
  if (installed.id !== config.installationId || installed.app_id !== config.appId ||
      installed.repository_selection !== 'selected' || installed.suspended_at ||
      !Object.entries(permissions).every(([key, value]) => installed.permissions?.[key] === value) ||
      Object.keys(installed.permissions).some(key => !Object.hasOwn(permissions, key)))
    throw new Error('The GitHub App installation does not match the protected Fleet issuer');
  const issued = await github('/app/installations/' + config.installationId + '/access_tokens', jwt, {
    repositories: ['fleet'], permissions,
  });
  if (typeof issued.token !== 'string' || !issued.token ||
      !Object.entries(permissions).every(([key, value]) => issued.permissions?.[key] === value) ||
      Object.keys(issued.permissions).some(key => !Object.hasOwn(permissions, key)))
    throw new Error('GitHub did not grant the restricted issuer token');
  const scope = await github('/installation/repositories?per_page=2', issued.token);
  if (scope.total_count !== 1 || scope.repositories?.[0]?.full_name !== 'taskqorg/fleet')
    throw new Error('The issuer token is not scoped to the Fleet repository');
  return issued.token;
}

export function readDesignCheckReceipt(check, { appId, head, externalId }) {
  if (!Number.isSafeInteger(check?.id) || check.id < 1 || check.name !== name || check.app?.id !== appId ||
      check.head_sha !== head || check.status !== 'completed' || check.conclusion !== 'success' ||
      check.external_id !== externalId)
    throw new Error('GitHub design check readback did not match its App, head and retained receipt');
  return { checkRunId: check.id, appId, head, externalId, conclusion: check.conclusion };
}

// The protected adapter must durably retain evidence before publication. It never receives a credential.
export async function issueFleetDesignCheck(config, event, adapter) {
  const evidence = await verifyProtectedDesignCandidate(event, adapter);
  const externalId = createHash('sha256').update('fleet:design-check:v1\0').update(JSON.stringify(evidence)).digest('hex');
  await adapter.retainReceipt(externalId, evidence);
  const installationToken = await fleetInstallation(config);
  const found = await github(repo + '/commits/' + evidence.head + '/check-runs?check_name=' + name +
    '&filter=latest&app_id=' + config.appId, installationToken);
  const existing = found.check_runs?.find(check => check.external_id === externalId);
  const check = existing ?? await github(repo + '/check-runs', installationToken, {
    name, head_sha: evidence.head, status: 'completed', conclusion: 'success', external_id: externalId,
    output: { title: 'Fleet design verification', summary: evidence.classification },
  });
  if (!Number.isSafeInteger(check.id) || check.id < 1) throw new Error('Missing GitHub check-run ID');
  const stored = await github(repo + '/check-runs/' + check.id, installationToken);
  return readDesignCheckReceipt(stored, { appId: config.appId, head: evidence.head, externalId });
}

// Issuer entry point for a separate protected service. This module is never invoked by candidate CI.
import { createHash, createPrivateKey, createPublicKey, sign } from 'node:crypto';
import { constants } from 'node:fs';
import { open } from 'node:fs/promises';
import { dirname, isAbsolute } from 'node:path';
import { verifyProtectedDesignCandidate } from './design-protected.mjs';

const repo = '/repos/taskqorg/fleet';
const name = 'design-approval';
const permissions = { checks: 'write', contents: 'read', pull_requests: 'read', metadata: 'read' };
const repositoryId = 1450484391;
const unitCredentialDir = '/run/credentials/fleet-design-verifier.service';

export function designCredentialIsPrivate(path, stat) {
  if (!stat.isFile()) return false;
  if ((stat.mode & 0o077) === 0) return true;
  // DynamicUser's ACL masks a read-only systemd credential as group-readable.
  return dirname(path) === unitCredentialDir && stat.uid === 0 && stat.gid === 0 && (stat.mode & 0o7777) === 0o440;
}

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
    if (!designCredentialIsPrivate(keyFile, state)) throw new Error('private key must be service-private');
    const now = Math.floor(Date.now() / 1000);
    const encode = value => Buffer.from(JSON.stringify(value)).toString('base64url');
    const jwt = encode({ alg: 'RS256', typ: 'JWT' }) + '.' + encode({ iss: String(appId), iat: now - 60, exp: now + 540 });
    const key = createPrivateKey(await file.readFile());
    const keyFingerprint = 'SHA256:' + createHash('sha256')
      .update(createPublicKey(key).export({ type: 'spki', format: 'der' })).digest('base64');
    return { jwt: jwt + '.' + sign('RSA-SHA256', Buffer.from(jwt), key).toString('base64url'), keyFingerprint };
  } catch {
    throw new Error('Could not load the private GitHub App signing key');
  } finally {
    await file?.close();
  }
}

async function github(path, token, { method = 'GET', body } = {}) {
  const response = await fetch('https://api.github.com' + path, {
    method,
    redirect: 'error',
    signal: AbortSignal.timeout(30_000),
    headers: { Authorization: 'Bearer ' + token, Accept: 'application/vnd.github+json',
      'Content-Type': 'application/json', 'X-GitHub-Api-Version': '2026-03-10' },
    body: body ? JSON.stringify(body) : undefined,
  });
  if (!response.ok) throw Object.assign(new Error('GitHub design checker returned HTTP ' + response.status), { status: response.status });
  if (response.status === 204) return undefined;
  return response.json();
}

async function fleetInstallation(config) {
  const { jwt, keyFingerprint } = await gitHubJWT(config);
  const installed = await github(repo + '/installation', jwt);
  if (installed.id !== config.installationId || installed.app_id !== config.appId ||
      installed.repository_selection !== 'selected' || installed.suspended_at ||
      !Object.entries(permissions).every(([key, value]) => installed.permissions?.[key] === value) ||
      Object.keys(installed.permissions).some(key => !Object.hasOwn(permissions, key)))
    throw new Error('The GitHub App installation does not match the protected Fleet issuer');
  const issued = await github('/app/installations/' + config.installationId + '/access_tokens', jwt, {
    method: 'POST', body: { repository_ids: [repositoryId], permissions },
  });
  if (typeof issued.token !== 'string' || !issued.token ||
      !Object.entries(permissions).every(([key, value]) => issued.permissions?.[key] === value) ||
      Object.keys(issued.permissions).some(key => !Object.hasOwn(permissions, key)))
    throw new Error('GitHub did not grant the restricted issuer token');
  const scope = await github('/installation/repositories?per_page=2', issued.token);
  if (scope.total_count !== 1 || scope.repositories?.[0]?.full_name !== 'taskqorg/fleet' ||
      scope.repositories[0].id !== repositoryId)
    throw new Error('The issuer token is not scoped to the Fleet repository');
  const expires = Date.parse(issued.expires_at);
  if (!Number.isFinite(expires) || expires <= Date.now()) throw new Error('GitHub issued an invalid token expiry');
  return { token: issued.token, expires, keyFingerprint, authenticatedAt: Date.now() };
}

// Only the protected service receives this client. Git and any future isolated builder receive no token.
export function fleetDesignGitHub(config, { connect = fleetInstallation, send = github } = {}) {
  let access;
  const api = async (path, options) => {
    if (!access || access.expires - Date.now() < 60_000) access = await connect(config);
    try {
      return await send(path, access.token, options);
    } catch (err) {
      if (err.status === 401) access = undefined;
      throw err;
    }
  };
  api.authentication = () => access && ({ keyFingerprint: access.keyFingerprint,
    authenticatedAt: access.authenticatedAt, expiresAt: access.expires });
  api.close = async () => {
    if (!access) return;
    const { token } = access;
    access = undefined;
    await send('/installation/token', token, { method: 'DELETE' });
  };
  return api;
}

export function readDesignCheckReceipt(check, { appId, head, externalId, conclusion = 'success' }) {
  const status = conclusion === null ? 'in_progress' : 'completed';
  if (!Number.isSafeInteger(check?.id) || check.id < 1 || check.name !== name || check.app?.id !== appId ||
      check.head_sha !== head || check.status !== status || check.conclusion !== conclusion ||
      check.external_id !== externalId)
    throw new Error('GitHub design check readback did not match its App, head and retained receipt');
  return { checkRunId: check.id, appId, head, externalId, conclusion: check.conclusion };
}

export const designCheckDigest = evidence => createHash('sha256').update('fleet:design-check:v1\0')
  .update(JSON.stringify(evidence)).digest('hex');

export function fleetDesignChecks(api, appId) {
  const runs = repo + '/check-runs';
  const valid = (check, head) => Number.isSafeInteger(check?.id) && check.id > 0 &&
    check.name === name && check.app?.id === appId && check.head_sha === head;
  async function find(head) {
    const found = await api(repo + '/commits/' + head + '/check-runs?check_name=' + name +
      '&filter=latest&app_id=' + appId);
    if (!Array.isArray(found.check_runs) || found.total_count !== found.check_runs.length ||
        found.check_runs.length > 1 || found.check_runs.some(check => !valid(check, head)))
      throw new Error('GitHub returned ambiguous design check runs');
    return found.check_runs[0];
  }
  const read = id => api(runs + '/' + id);
  return {
    find, read,
    async publish(head, externalId, conclusion, summary, assertCurrent = () => {}) {
      let check = await find(head);
      await assertCurrent();
      const status = conclusion === null ? 'in_progress' : 'completed';
      if (!check || check.external_id !== externalId || check.status !== status || check.conclusion !== conclusion) {
        const body = { name, status, external_id: externalId,
          output: { title: 'Fleet design verification', summary } };
        if (conclusion !== null) {
          body.conclusion = conclusion;
          body.completed_at = new Date().toISOString();
        }
        // GitHub cannot reopen a completed check run. A new latest run supersedes it.
        check = check?.status === 'in_progress'
          ? await api(runs + '/' + check.id, { method: 'PATCH', body })
          : await api(runs, { method: 'POST', body: { ...body, head_sha: head } });
      }
      if (!valid(check, head)) throw new Error('GitHub did not return the expected check run');
      return readDesignCheckReceipt(await read(check.id), { appId, head, externalId, conclusion });
    },
  };
}

// The protected adapter durably retains evidence before publication.
export async function issueFleetDesignCheck(config, event, adapter) {
  const evidence = await verifyProtectedDesignCandidate(event, adapter);
  const externalId = designCheckDigest(evidence);
  await adapter.retainReceipt(externalId, evidence);
  return fleetDesignChecks(fleetDesignGitHub(config), config.appId)
    .publish(evidence.head, externalId, 'success', evidence.classification);
}

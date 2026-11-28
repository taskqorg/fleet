export const repository = 'github.com/taskqorg/fleet';
export const repo = '/repos/taskqorg/fleet';
export const repositoryId = 1450484391;
export const appId = 5962560;
export const installationId = 186718702;
const queuePrefix = 'refs/heads/gh-readonly-queue/main/';
const queueRef = /^refs\/heads\/gh-readonly-queue\/main\/pr-([1-9][0-9]*)-([a-f0-9]{40})$/;
const mainRef = 'refs/heads/main';
const target = 'main';
export const sha = value => typeof value === 'string' && /^[0-9a-f]{40}$/.test(value);
const positive = value => Number.isSafeInteger(value) && value > 0;

export class VerificationWait extends Error {}
export class DesignBlocked extends Error {}
const requireFact = (value, message) => { if (!value) throw new VerificationWait(message); };

function pull(row) {
  requireFact(positive(row?.number) && row.base?.repo?.id === repositoryId && row.base.ref === target &&
    sha(row.base.sha) && sha(row.head?.sha) && typeof row.head.ref === 'string' && row.head.ref,
  'GitHub returned incomplete Fleet PR facts');
  return { kind: 'pull_request', repository, target, number: row.number, head: row.head.sha, base: row.base.sha,
    branch: row.head.ref, sourceRepositoryId: row.head.repo?.id ?? null };
}

const samePR = (a, b) => a.head === b.head && a.base === b.base && a.branch === b.branch &&
  a.sourceRepositoryId === b.sourceRepositoryId;

function branch(row, ref) {
  requireFact(row?.ref === ref && row.object?.type === 'commit' && sha(row.object.sha), 'GitHub returned incomplete branch facts');
  return row.object.sha;
}

export function githubCandidates(api) {
  const read = (path, options) => api(repo + path, options);
  const readRef = async ref => branch(await read('/git/ref/' + ref.slice('refs/'.length)), ref);
  async function assertGroupRef(event) {
    requireFact(event.ref.startsWith(queuePrefix) && await readRef(mainRef) === event.base &&
      await readRef(event.ref) === event.head, 'The merge queue head or base changed');
  }
  async function list(path) {
    const all = [];
    for (let page = 1; ; page++) {
      const rows = await read(path + (path.includes('?') ? '&' : '?') + 'per_page=100&page=' + page);
      requireFact(Array.isArray(rows), 'GitHub returned an invalid list');
      all.push(...rows);
      if (rows.length < 100) return all;
    }
  }

  async function readPR(number) {
    const row = await read('/pulls/' + number);
    const event = pull(row);
    requireFact(event.number === number && row.state === 'open', 'The PR is no longer open on Fleet main');
    return event;
  }

  async function readQueue() {
    const entries = new Map();
    let cursor = null;
    do {
      const result = await api('/graphql', { method: 'POST', body: { variables: { cursor }, query: `
        query FleetDesignQueue($cursor: String) {
          repository(owner: "taskqorg", name: "fleet") {
            databaseId nameWithOwner
            mergeQueue(branch: "main") {
              configuration { mergeMethod }
              entries(first: 100, after: $cursor) {
                totalCount pageInfo { hasNextPage endCursor }
                nodes { id pullRequest { number headRefOid baseRefName state } }
              }
            }
          }
        }` } });
      const owner = result.data?.repository;
      const queue = owner?.mergeQueue;
      const page = queue?.entries;
      requireFact(!result.errors?.length && owner?.databaseId === repositoryId && owner.nameWithOwner === 'taskqorg/fleet' &&
        queue?.configuration?.mergeMethod === 'MERGE' && Array.isArray(page?.nodes) && typeof page.pageInfo?.hasNextPage === 'boolean',
      'GitHub did not confirm the live merge queue');
      for (const row of page.nodes) {
        const pr = row?.pullRequest;
        requireFact(row?.id && positive(pr?.number) && sha(pr.headRefOid) && pr.baseRefName === target && pr.state === 'OPEN' &&
          !entries.has(pr.number), 'GitHub returned incomplete queue member facts');
        entries.set(pr.number, { id: row.id, number: pr.number, head: pr.headRefOid });
      }
      const next = page.pageInfo.hasNextPage ? page.pageInfo.endCursor : null;
      requireFact(next ? next !== cursor && page.nodes.length > 0 : entries.size === page.totalCount && !page.pageInfo.hasNextPage,
        'GitHub returned an incomplete merge queue page');
      cursor = next;
    } while (cursor);
    return entries;
  }

  async function readGroup(event, git) {
    const match = queueRef.exec(event.ref);
    requireFact(match, 'GitHub returned an unsupported merge queue ref');
    await assertGroupRef(event);
    const chain = await git.groupChain(event);
    requireFact(chain.length && chain[0].parent === match[2], 'The queue ref does not match its first parent');
    const queue = [...(await readQueue()).values()];
    const members = [];
    for (const { commit, parent, original } of chain) {
      const row = await read('/commits/' + commit);
      // Provider-generated merge commits on the live queue ref, never a PR number from author text.
      requireFact(row.sha === commit && row.committer?.id === 19864447 && row.commit?.verification?.verified === true &&
        row.parents?.length === 2 && row.parents[0].sha === parent && row.parents[1].sha === original,
      'GitHub did not confirm the queue merge commit');
      const linked = queue.filter(pr => pr.head === original);
      requireFact(linked.length === 1, 'GitHub did not identify one queued PR for the original commit');
      const member = await readPR(linked[0].number);
      requireFact(member.head === original, 'A merge group member has moved to a new PR head');
      members.push({ ...member, mergeCommit: commit, queueEntryId: linked[0].id });
    }
    requireFact(members[0].number === Number(match[1]), 'The queue ref names a different final PR');
    return { ...event, complete: true, members: members.reverse() };
  }

  return {
    async snapshot() {
      const rows = await list('/pulls?state=open&base=main');
      const prs = rows.map(pull);
      const open = new Set(prs.map(pr => pr.number));
      const base = await readRef(mainRef);
      const refs = await list('/git/matching-refs/heads/gh-readonly-queue/main/');
      const groups = [];
      for (const row of refs) {
        const match = queueRef.exec(row?.ref);
        requireFact(match, 'GitHub returned an unsupported merge queue ref');
        // GitHub can leave refs around for closed PRs. The refs cannot authorize a check.
        if (open.has(Number(match[1]))) groups.push({ kind: 'merge_group', repository, target,
          ref: row.ref, head: branch(row, row.ref), base });
      }
      return [...prs, ...groups];
    },
    async readCandidate(event, git) {
      if (event.kind === 'merge_group') return readGroup(event, git);
      const actual = await readPR(event.number);
      requireFact(samePR(actual, event), 'The PR source, head or base changed');
      return actual;
    },
    async assertCurrent(event, candidate) {
      if (event.kind === 'pull_request') {
        const current = await readPR(event.number);
        requireFact(samePR(current, event), 'The PR source, head or base changed');
        return;
      }
      if (candidate) {
        const queue = await readQueue();
        for (const member of candidate.members) {
          const entry = queue.get(member.number);
          requireFact(entry?.id === member.queueEntryId && entry?.head === member.head &&
            samePR(await readPR(member.number), member), 'A merge group member has moved or left the queue');
        }
      }
      await assertGroupRef(event);
    },
  };
}

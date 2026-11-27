export const rich = {
  code: 'Q-FLT-7',
  question: 'Accept the updated review screen?',
  evidence: {
    acceptance: {
      taskCode: 'T-FLT-42',
      summary: 'Added review outcomes beside the files they verify.',
      verification: 'Go, app and production browser checks passed.',
      pullRequest: { repo: 'taskqorg/fleet', number: 643 },
      release: 'v1.261117.4',
    },
    artifactIds: ['00000000-0000-4000-8000-000000000055'],
  },
};

export const sparse = {
  code: 'Q-FLT-8',
  question: 'Accept the revised task brief?',
  evidence: { acceptance: { taskCode: 'T-FLT-43' } },
};

export const choices = [{ label: 'accepted', primary: true }, { label: 'rejected' }];

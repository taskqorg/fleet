export async function retain(tools, out, option, receipts = {}, checkpoint = () => {}) {
  const execution = option.execution;
  if (typeof execution !== 'string' || !/^[a-f0-9]{8}(?:-[a-f0-9]{4}){3}-[a-f0-9]{12}$/.test(execution))
    throw new Error('A verified designer execution is required');
  async function call(tool, args) {
    const result = await tool(args);
    const content = result.content?.find(item => item.type === 'text')?.text;
    if (result.isError || !content) throw new Error(content || 'Fleet did not return artifact evidence');
    return JSON.parse(content);
  }
  const expected = new Map();
  const resumed = { ...receipts };
  async function save(file, media, sha256) {
    let artifactId = resumed[file];
    if (!artifactId) {
      ({ artifactId } = await call(tools.mcp__fleet__artifact_save, {
        path: out + '/' + file, name: file, media_type: media, required: true,
      }));
      if (!artifactId) throw new Error('Missing artifact ID for ' + file);
      resumed[file] = artifactId;
      checkpoint({ file, artifactId });
    }
    if (expected.has(artifactId)) throw new Error('One artifact ID cannot represent multiple files');
    expected.set(artifactId, { sha256, mediaType: media });
    return artifactId;
  }
  const sourceArtifactId = await save('source.tar', 'application/x-tar', option.sourceSha256);
  const cases = [];
  for (const { scopeKey, recipe, recipeFile, recipeSha256, frames } of option.cases) {
    const recipeArtifactId = await save(recipeFile, 'application/json', recipeSha256);
    const originals = [];
    for (const { viewport, theme, file, sha256 } of frames)
      originals.push({ viewport, theme, artifactId: await save(file, 'image/png', sha256), sha256 });
    cases.push({ scopeKey, recipe, recipeArtifactId, frames: originals });
  }
  let cursor;
  do {
    const page = await call(tools.mcp__fleet__artifact_list, { execution_id: execution, ...(cursor && { cursor }) });
    if (page.state === 'pending' && !Array.isArray(page.artifacts))
      return { state: 'pending', receipts: resumed, pending: [...expected.keys()] };
    if (!Array.isArray(page.artifacts)) throw new Error('Fleet returned an invalid artifact list');
    for (const item of page.artifacts) {
      const entry = expected.get(item.id);
      if (!entry) continue;
      if (item.executionId !== execution) throw new Error('Stored artifact belongs to another execution');
      if (item.state === 'stored') {
        if (item.sha256 !== entry.sha256 || item.mediaType !== entry.mediaType)
          throw new Error('Stored artifact digest or media type differs');
        expected.delete(item.id);
      } else if (item.state === 'failed') throw new Error('Fleet artifact transfer failed');
    }
    cursor = page.nextCursor;
  } while (cursor && expected.size);
  if (expected.size) return { state: 'pending', receipts: resumed, pending: [...expected.keys()] };
  return { state: 'stored', receipts: resumed, sourceArtifactId, cases };
}

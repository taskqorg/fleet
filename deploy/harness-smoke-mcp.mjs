// A stdio MCP server with one tool (echo) for the managed CLI qualification (deploy/qualify-harness-pins.mjs).
import { createInterface } from 'node:readline';

const send = message => process.stdout.write(JSON.stringify({ jsonrpc: '2.0', ...message }) + '\n');
const tool = {
  name: 'echo',
  description: 'Returns its text argument.',
  inputSchema: { type: 'object', properties: { text: { type: 'string' } }, required: ['text'] },
};
for await (const line of createInterface({ input: process.stdin })) {
  let request;
  try {
    request = JSON.parse(line);
  } catch {
    continue;
  }
  if (request.id === undefined) continue; // notifications
  if (request.method === 'initialize') {
    send({ id: request.id, result: { protocolVersion: request.params?.protocolVersion ?? '2025-06-18', capabilities: { tools: {} }, serverInfo: { name: 'fleet-smoke', version: '1.0.0' } } });
  } else if (request.method === 'tools/list') {
    send({ id: request.id, result: { tools: [tool] } });
  } else if (request.method === 'tools/call') {
    send({ id: request.id, result: { content: [{ type: 'text', text: String(request.params?.arguments?.text ?? '') }] } });
  } else {
    send({ id: request.id, error: { code: -32601, message: `unknown method ${request.method}` } });
  }
}

import { gunzipSync, gzipSync } from 'node:zlib';

const maxWire = 96 * 1024;
const maxInventory = 256 * 1024;
const encoding = 'gzip-base64-v1';

// All recipes keep their exact input pins. Only the signed build transport
// collapses identical pins and compresses the full unique inventory.
export function dependencyWire(rows) {
  const unique = new Map();
  for (const { path, sha256 } of rows) {
    if (unique.has(path) && unique.get(path) !== sha256) throw new Error('Conflicting selected dependency pins');
    unique.set(path, sha256);
  }
  const inventory = JSON.stringify([...unique].map(([path, sha256]) => ({ path, sha256 })));
  if (Buffer.byteLength(inventory) <= maxWire) return inventory;
  if (Buffer.byteLength(inventory) > maxInventory) throw new Error('Selected dependency inventory is too large');
  const wire = JSON.stringify([{ encoding, data: gzipSync(inventory).toString('base64') }]);
  if (Buffer.byteLength(wire) > maxWire) throw new Error('Selected dependency input list is too large');
  return wire;
}

export function readDependencyWire(raw) {
  if (typeof raw !== 'string' || Buffer.byteLength(raw) > maxWire) throw new Error('Invalid dependency input size');
  const wire = JSON.parse(raw);
  if (!Array.isArray(wire)) throw new Error('Invalid dependency input list');
  const envelope = wire.length === 1 ? wire[0] : undefined;
  if (envelope?.encoding !== encoding) return wire;
  if (Object.keys(envelope).sort().join(',') !== 'data,encoding' || typeof envelope.data !== 'string')
    throw new Error('Invalid dependency input envelope');
  const compressed = Buffer.from(envelope.data, 'base64');
  if (compressed.toString('base64') !== envelope.data) throw new Error('Invalid dependency encoding');
  const rows = JSON.parse(gunzipSync(compressed, { maxOutputLength: maxInventory }));
  if (!Array.isArray(rows)) throw new Error('Invalid dependency inventory');
  return rows;
}

// esbuild inject for the ESM bundle (build.mjs): CommonJS dependencies get real require, __filename and __dirname.
import {createRequire} from 'node:module';import {fileURLToPath} from 'node:url';import {dirname} from 'node:path';
export const require=createRequire(import.meta.url);
export const __filename=fileURLToPath(import.meta.url);
export const __dirname=dirname(__filename);

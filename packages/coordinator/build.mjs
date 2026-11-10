// Coordinator build (docs/designs/fleet-stack.md D1): a clean dist, then one esbuild bundle per entry at the paths the
// compiled files had, so every import.meta.url asset path stays the same. server.js is a two-line entry that turns source
// maps on before the bundle loads (Node parses maps only for modules loaded after that), so every start path gets them.
import {rmSync,writeFileSync} from 'node:fs';
import {build} from 'esbuild';
rmSync('dist',{recursive:true,force:true});
const out='dist/packages/coordinator';
await build({entryPoints:{'server-main':'packages/coordinator/server.ts','migrate-sqlite':'packages/coordinator/migrate-sqlite.ts'},outdir:out,
 bundle:true,platform:'node',format:'esm',target:'node22',sourcemap:true,logLevel:'warning',
 // Every runtime dependency is in the bundle, so a release needs no node_modules (D4). PGlite is the dev/test store, loaded
 // only for a non-Postgres URL; pg-native is pg's optional native backend.
 external:['@electric-sql/pglite','pg-native'],
 // CommonJS dependencies need require, __filename and __dirname. A banner, not an inject file: esbuild's own __require
 // helper reads the free name require, which an inject does not reach.
 banner:{js:"import {createRequire as __fleetRequire} from 'node:module';import {fileURLToPath as __fleetPath} from 'node:url';import {dirname as __fleetDir} from 'node:path';const require=__fleetRequire(import.meta.url),__filename=__fleetPath(import.meta.url),__dirname=__fleetDir(__filename);"}});
writeFileSync(out+'/server.js',"process.setSourceMapsEnabled(true);\nawait import('./server-main.js');\n");

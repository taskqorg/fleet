// Coordinator build (docs/designs/fleet-toolchain.md D1): a clean dist, then one esbuild bundle per entry at the paths the
// compiled files had, so every import.meta.url asset path stays the same. server.js is a two-line entry that turns source
// maps on before the bundle loads (Node parses maps only for modules loaded after that), so every start path gets them.
import {rmSync,writeFileSync} from 'node:fs';
import {build} from 'esbuild';
rmSync('dist',{recursive:true,force:true});
const out='dist/packages/coordinator';
await build({entryPoints:{'server-main':'packages/coordinator/server.ts','migrate-sqlite':'packages/coordinator/migrate-sqlite.ts'},outdir:out,
 bundle:true,platform:'node',format:'esm',target:'node22',packages:'external',sourcemap:true,logLevel:'warning'});
writeFileSync(out+'/server.js',"process.setSourceMapsEnabled(true);\nawait import('./server-main.js');\n");

// Shared esbuild plugin that compiles Solid 2 JSX/TSX modules via @solidjs/compiler while leaving TS types for esbuild.
import { transform } from '@solidjs/compiler';
import { readFileSync } from 'node:fs';

export function solidEsbuildPlugin(opts = {}) {
  return {
    name: 'solid-2',
    setup(build) {
      build.onLoad({ filter: /\.[jt]sx$/ }, args => {
        const src = readFileSync(args.path, 'utf8');
        const res = transform(src, {
          filename: args.path,
          generate: 'dom',
          moduleName: '@solidjs/web',
          sourceMap: !!opts.sourceMap,
        });
        let code = res.code;
        if (opts.sourceMap && res.map) {
          code += `\n//# sourceMappingURL=data:application/json;base64,${Buffer.from(res.map).toString('base64')}`;
        }
        return { contents: code, loader: args.path.endsWith('.tsx') ? 'ts' : 'js' };
      });
    },
  };
}

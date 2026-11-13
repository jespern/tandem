import {build} from 'esbuild';
import {gzipSync} from 'node:zlib';
import {readFile} from 'node:fs/promises';

const result = await build({entryPoints: ['dist/browser.js'], outfile: 'dist/browser/tandem.js', bundle: true,
  format: 'esm', platform: 'browser', conditions: ['browser'], target: ['es2024'], minify: true, sourcemap: true,
  metafile: true, legalComments: 'linked'});
if (Object.keys(result.metafile.inputs).some(path => path === 'src/platform.ts' || path === 'dist/platform.js')) throw new Error('browser bundle contains the Node backend');
const bytes = await readFile('dist/browser/tandem.js');
console.log(`Browser SDK: ${bytes.length} bytes; ${gzipSync(bytes).length} bytes gzip`);
await build({entryPoints: ['dist/browser/argon-worker.js'], outfile: 'dist/browser/argon-worker.js', allowOverwrite: true, bundle: true,
  format: 'esm', platform: 'browser', target: ['es2024'], minify: true, sourcemap: true, legalComments: 'linked'});

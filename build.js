#!/usr/bin/env node

const esbuild = require('esbuild');
const fs = require('fs');
const path = require('path');
const crypto = require('crypto');

const isDev = process.argv.includes('--dev');
const staticDir = 'static';
const distDir = 'static/dist';

// Ensure dist directory exists
if (!fs.existsSync(distDir)) {
  fs.mkdirSync(distDir, { recursive: true });
}

// Asset specifications (matching the original Go tool)
const assets = [
  { source: 'css/wizard.css', kind: 'css' },
  { source: 'css/common.css', kind: 'css' },
  { source: 'css/admin.css', kind: 'css' },
  { source: 'css/queue.css', kind: 'css' },
  { source: 'js/common.js', kind: 'js' },
  { source: 'js/admin.js', kind: 'js' },
  { source: 'js/family-admin.js', kind: 'js' },
  { source: 'js/index.js', kind: 'js' },
  { source: 'js/queue.js', kind: 'js' }
];

async function buildAssets() {
  const manifest = {};
  
  for (const asset of assets) {
    const srcPath = path.join(staticDir, asset.source);
    
    if (!fs.existsSync(srcPath)) {
      console.error(`Source file not found: ${srcPath}`);
      process.exit(1);
    }
    
    const ext = path.extname(asset.source);
    const base = path.basename(asset.source, ext);
    const outDir = path.join(distDir, path.dirname(asset.source));
    
    // Ensure output directory exists
    if (!fs.existsSync(outDir)) {
      fs.mkdirSync(outDir, { recursive: true });
    }
    
    try {
      let result;
      
      if (asset.kind === 'css') {
        // Build CSS with esbuild
        result = await esbuild.build({
          entryPoints: [srcPath],
          outdir: outDir,
          minify: !isDev,
          bundle: false,
          sourcemap: isDev,
          target: 'es2015',
          format: 'iife',
          outExtension: { '.css': '.css' },
          write: false,
          // CSS-specific options
          minifyIdentifiers: false,
          minifySyntax: !isDev,
          minifyWhitespace: !isDev
        });
      } else if (asset.kind === 'js') {
        // Special handling for common.js - it's a collection of global functions
        if (asset.source === 'js/common.js') {
          let content = fs.readFileSync(srcPath, 'utf8');
          if (!isDev) {
            // Simple minification for common.js to preserve global functions
            content = content
              .replace(/\/\*[\s\S]*?\*\//g, '') // Remove block comments
              .replace(/\/\/.*$/gm, '') // Remove line comments
              .replace(/\s+/g, ' ') // Collapse whitespace
              .trim();
          }
          result = { outputFiles: [{ contents: Buffer.from(content, 'utf8') }] };
        } else {
          // For other JavaScript files, use esbuild for proper minification
          result = await esbuild.build({
            entryPoints: [srcPath],
            outdir: outDir,
            minify: !isDev,
            bundle: false,
            sourcemap: isDev,
            target: 'es2015',
            format: 'iife',
            outExtension: { '.js': '.js' },
            write: false,
            // Preserve function names and avoid aggressive optimizations
            keepNames: true,
            // Preserve string literals and identifiers
            minifyIdentifiers: false,
            minifySyntax: !isDev,
            minifyWhitespace: !isDev
          });
        }
      }
      
      if (result && result.outputFiles && result.outputFiles.length > 0) {
        const outputFile = result.outputFiles[0];
        const content = outputFile.contents;
        
        // Generate hash for fingerprinting
        const hash = crypto.createHash('sha256').update(content).digest('hex').substring(0, 12);
        const outName = `${base}-${hash}${ext}`;
        const outPath = path.join(outDir, outName);
        
        // Write the file
        fs.writeFileSync(outPath, content);
        
        // Add to manifest
        const key = asset.source.replace(/\\/g, '/');
        const rel = path.join('dist', path.dirname(asset.source), outName).replace(/\\/g, '/');
        manifest[key] = rel;
        
        console.log(`built ${asset.source} -> ${rel}`);
      }
    } catch (error) {
      console.error(`Error building ${asset.source}:`, error);
      process.exit(1);
    }
  }
  
  // Write manifest
  const manifestPath = path.join(distDir, 'manifest.json');
  fs.writeFileSync(manifestPath, JSON.stringify(manifest, null, 2));
  console.log(`wrote manifest to ${manifestPath}`);
}

// Run the build
buildAssets().catch(error => {
  console.error('Build failed:', error);
  process.exit(1);
});

import { defineConfig } from 'vite';
import { existsSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import laravel from 'laravel-vite-plugin';
import tailwindcss from '@tailwindcss/vite';

const root = fileURLToPath(new URL('.', import.meta.url));
const maybeAddon = (relPath) => (existsSync(`${root}${relPath}`) ? [relPath] : []);

export default defineConfig({
    server: {
        allowedHosts: true,
    },
    plugins: [
        laravel({
            input: [
                'resources/css/app.css',
                'resources/js/app.js',
                'resources/js/server-charts.js',
                'resources/css/filament/admin/theme.css',
                // Jabali Terminal plugin assets. Conditionally included so the
                // build works whether or not the addon is installed; the addon
                // drops these files in via install.sh.
                ...maybeAddon('resources/js/jabali-terminal.js'),
                ...maybeAddon('resources/css/jabali-terminal.css'),
            ],
            refresh: true,
        }),
        tailwindcss(),
    ],
});

// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

// https://astro.build/config
export default defineConfig({
	vite: {
		server: {
			allowedHosts: true,
		},
	},
	integrations: [
		starlight({
			title: 'Jabali Panel Documentation',
			description: 'Feature documentation and screenshots for the Jabali hosting panel.',
			sidebar: [
				{
					label: 'Getting Started',
					items: [{ label: 'Overview', slug: 'overview' }, { label: 'Installation', slug: 'install' }, { label: 'Quickstart', slug: 'quickstart' }, { label: 'Backups and Restore', slug: 'backups-restore' }, { label: 'Migrations', slug: 'migrations' }, { label: 'DNS and Mail', slug: 'dns-mail' }, { label: 'Operations', slug: 'operations' }, { label: 'Security', slug: 'security' }, { label: 'Troubleshooting', slug: 'troubleshooting' }],
				},
				{
					label: 'Admin Panel',
					autogenerate: { directory: 'admin' },
				},
				{
					label: 'User Panel',
					autogenerate: { directory: 'user' },
				},
				{
					label: 'Platform',
					autogenerate: { directory: 'platform' },
				},
			],
		}),
	],
});

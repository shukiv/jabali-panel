import { defineConfig } from 'vite';

export default defineConfig({
	server: {
		allowedHosts: ['jabali.lan', '.jabali.lan', '192.168.100.236', 'localhost', '127.0.0.1'],
	},
});

// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

export default defineConfig({
	integrations: [
		starlight({
			title: 'golyglot',
			description: 'Pure-Go SQL parsing and intelligence.',
			social: [{ icon: 'github', label: 'GitHub', href: 'https://github.com/renart-data/golyglot' }],
			sidebar: [
				{
					label: 'Start here',
					items: [{ label: 'Overview', slug: 'index' }, 'quickstart'],
				},
				{
					label: 'Guides',
					items: [{ autogenerate: { directory: 'guides' } }],
				},
				{
					label: 'Reference',
					items: [{ autogenerate: { directory: 'reference' } }],
				},
				{
					label: 'Try it',
					items: [{ label: 'Monaco demo', link: '/demo/' }],
				},
			],
		}),
	],
});

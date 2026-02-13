#!/usr/bin/env node
/**
 * Jabali Panel Screenshot Script
 *
 * Takes screenshots of admin and user panels for documentation.
 *
 * Usage: node tests/take-screenshots.cjs [--output-dir=/path/to/dir]
 *
 * Requires: puppeteer (npm install puppeteer)
 */

const puppeteer = require('puppeteer');
const path = require('path');

const args = process.argv.slice(2);
const getArg = (key, fallback) => {
  const arg = args.find((item) => item.startsWith(`--${key}=`));
  return arg ? arg.split('=').slice(1).join('=') : fallback;
};

const hasFlag = (flag) => args.includes(`--${flag}`);
let browser;

// Configuration
const viewportWidth = Number(getArg('viewport-width', '1400'));
const viewportHeight = Number(getArg('viewport-height', '800'));
const fullPage = hasFlag('full-page');
const impersonateUserId = getArg('impersonate-user-id', null);

const CONFIG = {
  baseUrl: getArg('base-url', 'https://mx.jabali-panel.com'),
  adminPath: getArg('admin-path', '/jabali-admin'),
  userPath: getArg('user-path', '/jabali-panel'),
  admin: {
    email: getArg('admin-email', 'admin@mx.jabali-panel.com'),
    password: getArg('admin-password', 'PycpS1dUuLvxMMQs')
  },
  user: {
    email: getArg('user-email', 'user@jabali-panel.com'),
    password: getArg('user-password', 'PycpS1dUuLvxMMQs')
  },
  viewport: { width: viewportWidth, height: viewportHeight },
  outputDir: getArg('output-dir', '/tmp'),
  skipAdmin: hasFlag('skip-admin'),
  skipUser: hasFlag('skip-user'),
  impersonateUserId
};

// Admin pages to screenshot
const ADMIN_PAGES = [
  { name: 'dashboard', path: '', description: 'Admin Dashboard' },
  { name: 'server-status', path: '/server-status', description: 'Server Status' },
  { name: 'server-settings', path: '/server-settings', description: 'Server Settings' },
  { name: 'security', path: '/security', description: 'Security Center' },
  { name: 'users', path: '/users', description: 'User Management' },
  { name: 'ssl-manager', path: '/ssl-manager', description: 'SSL Manager' },
  { name: 'dns-zones', path: '/dns-zones', description: 'DNS Zones' },
  { name: 'backups', path: '/backups', description: 'Backups' },
  { name: 'services', path: '/services', description: 'Services' },
];

// User pages to screenshot
const USER_PAGES = [
  { name: 'dashboard', path: '', description: 'User Dashboard' },
  { name: 'domains', path: '/domains', description: 'Domain Management' },
  { name: 'backups', path: '/backups', description: 'Backups' },
  { name: 'cpanel-migration', path: '/cpanel-migration', description: 'cPanel Migration' }
];

const wait = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

async function loginPanel(page, panelName, basePath, credentials) {
  console.log(`Logging in to ${panelName} panel...`);
  await page.goto(`${CONFIG.baseUrl}${basePath}/login`, { waitUntil: 'networkidle0' });
  await wait(1500);

  await page.type('input[type="email"]', credentials.email);
  await page.type('input[type="password"]', credentials.password);
  await page.click('button[type="submit"]');
  await wait(4000);

  const currentUrl = page.url();
  if (currentUrl.includes('/login')) {
    throw new Error(`${panelName} login failed - still on login page`);
  }
  console.log(`${panelName} login successful!\n`);
}

async function loginUserViaImpersonation(page) {
  if (!CONFIG.impersonateUserId) {
    throw new Error('Missing --impersonate-user-id for user screenshots');
  }

  await loginPanel(page, 'Admin', CONFIG.adminPath, CONFIG.admin);

  const impersonateUrl = `${CONFIG.baseUrl}/impersonate/start/${CONFIG.impersonateUserId}`;
  console.log(`Impersonating user via: ${impersonateUrl}`);
  await page.goto(impersonateUrl, { waitUntil: 'networkidle0' });
  await wait(2000);

  const currentUrl = page.url();
  if (currentUrl.includes('/login')) {
    throw new Error('Impersonation failed - still on login page');
  }
  console.log('User impersonation successful!\n');
}

async function capturePanel({ panelName, basePath, credentials, pages, outputPrefix }) {
  const context = await browser.createBrowserContext();
  const page = await context.newPage();
  await page.setViewport(CONFIG.viewport);

  try {
    if (panelName === 'User' && CONFIG.impersonateUserId) {
      await loginUserViaImpersonation(page);
    } else {
      await loginPanel(page, panelName, basePath, credentials);
    }

    for (const pageInfo of pages) {
      const url = `${CONFIG.baseUrl}${basePath}${pageInfo.path}`;
      const filename = `${outputPrefix}-${pageInfo.name}.png`;
      const filepath = path.join(CONFIG.outputDir, filename);

      console.log(`Taking screenshot: ${pageInfo.description}`);
      console.log(`  URL: ${url}`);

      try {
        await page.goto(url, { waitUntil: 'networkidle0', timeout: 30000 });
        await wait(3000);
        await page.screenshot({ path: filepath, fullPage });
        console.log(`  Saved: ${filepath}\n`);
      } catch (err) {
        console.log(`  Error: ${err.message}\n`);
      }
    }
  } catch (err) {
    console.error(`${panelName} screenshots failed:`, err.message);
  } finally {
    await context.close();
  }
}

async function takeScreenshots() {
  console.log('Starting Jabali Panel Screenshot Script...\n');

  browser = await puppeteer.launch({
    headless: 'new',
    args: ['--no-sandbox', '--disable-setuid-sandbox', '--ignore-certificate-errors']
  });

  try {
    if (!CONFIG.skipAdmin) {
      await capturePanel({
        panelName: 'Admin',
        basePath: CONFIG.adminPath,
        credentials: CONFIG.admin,
        pages: ADMIN_PAGES,
        outputPrefix: 'admin'
      });
    }

    if (!CONFIG.skipUser) {
      await capturePanel({
        panelName: 'User',
        basePath: CONFIG.userPath,
        credentials: CONFIG.user,
        pages: USER_PAGES,
        outputPrefix: 'user'
      });
    }

    console.log('Screenshot capture completed!');
    console.log(`Output directory: ${CONFIG.outputDir}`);
  } catch (err) {
    console.error('Error:', err.message);
    process.exit(1);
  } finally {
    await browser.close();
  }
}

takeScreenshots();

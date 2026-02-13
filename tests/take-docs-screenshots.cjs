#!/usr/bin/env node
/**
 * Generate documentation screenshots for every admin/user route and each tab.
 */

const fs = require('fs');
const path = require('path');
const { execSync } = require('child_process');
const puppeteer = require('puppeteer');

const args = process.argv.slice(2);
const getArg = (key, fallback) => {
  const arg = args.find((item) => item.startsWith(`--${key}=`));
  return arg ? arg.split('=').slice(1).join('=') : fallback;
};
const hasFlag = (flag) => args.includes(`--${flag}`);

const CONFIG = {
  baseUrl: getArg('base-url', 'https://jabali.lan'),
  outputDir: getArg('output-dir', '/var/www/jabali/docs/screenshots/full'),
  pagesJson: getArg('pages-json', '/var/www/jabali/tests/docs-pages.json'),
  admin: {
    email: getArg('admin-email', 'admin@jabali.lan'),
    password: getArg('admin-password', 'q1w2E#R$'),
    path: getArg('admin-path', '/jabali-admin'),
  },
  user: {
    path: getArg('user-path', '/jabali-panel'),
  },
  impersonateUserId: getArg('impersonate-user-id', '9'),
  skipAdmin: hasFlag('skip-admin'),
  skipUser: hasFlag('skip-user'),
  fullPage: hasFlag('full-page'),
  onlyMissing: hasFlag('only-missing'),
};

const wait = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

function slugify(value) {
  return value
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '');
}

function loadPages() {
  return JSON.parse(fs.readFileSync(CONFIG.pagesJson, 'utf-8'));
}

function queryId(table) {
  try {
    const out = execSync(
      `sqlite3 /var/www/jabali/database/database.sqlite "select id from ${table} limit 1;"`,
      { encoding: 'utf-8' }
    ).trim();
    return out || null;
  } catch (err) {
    return null;
  }
}

function applyRecordIds(uri) {
  if (!uri.includes('{record}')) return uri;
  const resourceMap = {
    'jabali-admin/users': 'users',
    'jabali-admin/hosting-packages': 'hosting_packages',
    'jabali-admin/geo-block-rules': 'geo_block_rules',
    'jabali-admin/webhook-endpoints': 'webhook_endpoints',
  };
  const base = uri.split('/{record}')[0];
  const table = resourceMap[base];
  if (!table) return uri;
  const id = queryId(table);
  if (!id) return uri;
  return uri.replace('{record}', id);
}

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
  await loginPanel(page, 'Admin', CONFIG.admin.path, CONFIG.admin);
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

async function clickTab(page, tabLabel) {
  const normalized = tabLabel.trim().toLowerCase();
  const clicked = await page.evaluate((label) => {
    const tabs = Array.from(document.querySelectorAll('[role="tab"], button'));
    const match = tabs.find((el) => {
      const text = (el.textContent || '').trim().toLowerCase();
      return text === label;
    }) || tabs.find((el) => {
      const text = (el.textContent || '').trim().toLowerCase();
      return text.includes(label);
    });
    if (match) {
      match.click();
      return true;
    }
    return false;
  }, normalized);
  if (clicked) {
    await wait(1500);
  }
  return clicked;
}

async function captureRoute(context, entry, prefix) {
  const page = await context.newPage();
  await page.setViewport({ width: 1400, height: 900 });

  const uri = applyRecordIds(entry.uri);
  const url = `${CONFIG.baseUrl}/${uri}`.replace(/\/+$/, '');
  const baseName = `${prefix}-${slugify(uri.replace(prefix === 'admin' ? 'jabali-admin' : 'jabali-panel', '')) || 'home'}`;
  const filename = `${baseName}.png`;
  const filepath = path.join(CONFIG.outputDir, filename);

  const skipBase = CONFIG.onlyMissing && fs.existsSync(filepath);

  try {
    await page.goto(url, { waitUntil: 'networkidle0', timeout: 45000 });
    await wait(2000);

    if (!skipBase) {
      await page.screenshot({ path: filepath, fullPage: CONFIG.fullPage });
      console.log(`Saved: ${filepath}`);
    }

    if (entry.tabs && entry.tabs.length) {
      for (const tab of entry.tabs) {
        const tabFile = `${baseName}--tab-${slugify(tab)}.png`;
        const tabPath = path.join(CONFIG.outputDir, tabFile);
        if (CONFIG.onlyMissing && fs.existsSync(tabPath)) {
          continue;
        }
        const clicked = await clickTab(page, tab);
        if (!clicked) {
          console.log(`  Tab not found: ${tab}`);
          continue;
        }
        await page.screenshot({ path: tabPath, fullPage: CONFIG.fullPage });
        console.log(`  Tab saved: ${tabPath}`);
      }
    }
  } catch (err) {
    console.log(`Error capturing ${url}: ${err.message}`);
  } finally {
    await page.close();
  }
}

async function captureAuthPages() {
  const pages = loadPages();
  const authUris = pages.filter((entry) => entry.uri.includes('login') || entry.uri.includes('password-reset') || entry.uri.includes('two-factor'));
  if (!authUris.length) return;

  const browser = await puppeteer.launch({
    headless: 'new',
    args: ['--no-sandbox', '--disable-setuid-sandbox', '--ignore-certificate-errors'],
  });

  const context = await browser.createBrowserContext();
  for (const entry of authUris) {
    const prefix = entry.panel === 'admin' ? 'admin' : 'user';
    await captureRoute(context, entry, prefix);
  }
  await browser.close();
}

async function main() {
  fs.mkdirSync(CONFIG.outputDir, { recursive: true });
  const pages = loadPages();

  await captureAuthPages();

  const browser = await puppeteer.launch({
    headless: 'new',
    args: ['--no-sandbox', '--disable-setuid-sandbox', '--ignore-certificate-errors'],
  });

  if (!CONFIG.skipAdmin) {
    const context = await browser.createBrowserContext();
    const page = await context.newPage();
    await page.setViewport({ width: 1400, height: 900 });
    await loginPanel(page, 'Admin', CONFIG.admin.path, CONFIG.admin);
    await page.close();

    for (const entry of pages.filter((e) => e.panel === 'admin')) {
      if (entry.uri.includes('login') || entry.uri.includes('password-reset') || entry.uri.includes('two-factor')) {
        continue;
      }
      await captureRoute(context, entry, 'admin');
    }

    await context.close();
  }

  if (!CONFIG.skipUser) {
    const context = await browser.createBrowserContext();
    const page = await context.newPage();
    await page.setViewport({ width: 1400, height: 900 });
    await loginUserViaImpersonation(page);
    await page.close();

    for (const entry of pages.filter((e) => e.panel === 'jabali')) {
      if (entry.uri.includes('login') || entry.uri.includes('password-reset') || entry.uri.includes('two-factor')) {
        continue;
      }
      await captureRoute(context, entry, 'user');
    }

    await context.close();
  }

  await browser.close();
  console.log('Documentation screenshots completed.');
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});

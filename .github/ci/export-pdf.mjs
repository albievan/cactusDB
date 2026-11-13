// Connects cactusdb to the SQLite file in DB_FILE, generates DB Relationships
// with every table's columns shown, and saves its PDF export as rel.pdf.
import { chromium } from 'playwright'

const browser = await chromium.launch()
const page = await browser.newPage({ viewport: { width: 1500, height: 1000 }, acceptDownloads: true })
const errors = []
page.on('pageerror', (e) => errors.push(e.message))
await page.goto('http://127.0.0.1:18093/')
await page.waitForSelector('#connectBtn', { state: 'attached' })
await page.evaluate((f) => { selectEngine('sqlite'); document.getElementById('dbFile').value = f; connect() }, process.env.DB_FILE)
await page.waitForSelector('#relTab:not([hidden])', { timeout: 60000 })
await page.click('#relTab')
await page.click('#relRefresh')
await page.waitForSelector('.relnode', { timeout: 60000 })
await page.click('#relOpenAll')
await page.waitForSelector('.relcols li .relct')
const dl = page.waitForEvent('download')
await page.click('#relPdf')
await (await dl).saveAs('rel.pdf')
await browser.close()
console.log(JSON.stringify({ saved: 'rel.pdf', errors }))
if (errors.length) process.exit(1)

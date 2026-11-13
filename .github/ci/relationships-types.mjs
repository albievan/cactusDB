// Opens DB Relationships in a headless Chromium for each database in DBS,
// expands every table and checks each column shows a type; writes what it
// read to rel-types.json and a screenshot per database (CI artifacts).
import { chromium } from 'playwright'
import { writeFileSync } from 'node:fs'

const DBS = JSON.parse(process.env.DBS)
const want = JSON.parse(process.env.WANT || '{}')
const browser = await chromium.launch()
const page = await browser.newPage({ viewport: { width: 1500, height: 1000 } })
const errors = []
page.on('pageerror', (e) => errors.push(e.message))
const out = {}
let failed = false
for (const d of DBS) {
  await page.goto('http://127.0.0.1:18093/')
  await page.waitForSelector('#connectBtn', { state: 'attached' })
  await page.evaluate((d) => {
    selectEngine(d.engine)
    for (const [id, v] of [['host', '127.0.0.1'], ['port', String(d.port)], ['user', d.user], ['pass', d.pass], ['dbName', d.db]]) document.getElementById(id).value = v
    connect()
  }, d)
  await page.waitForSelector('#relTab:not([hidden])', { timeout: 60000 })
  await page.click('#relTab')
  await page.click('#relRefresh')
  await page.waitForSelector('.relnode', { timeout: 120000 })
  await page.click('#relOpenAll')
  await page.waitForSelector('.relcols li .relct', { timeout: 30000 })
  const cols = await page.evaluate(() => Object.fromEntries([...document.querySelectorAll('.relcols li')].map((li) => {
    const t = li.querySelector('.relct')
    return [li.closest('.relnode').dataset.id + ' ' + (t ? li.textContent.replace(t.textContent, '') : li.textContent).trim(), t ? t.textContent : '']
  })))
  out[d.engine] = cols
  await page.screenshot({ path: `rel-${d.engine}.png` })
  for (const [k, v] of Object.entries(cols)) if (!v) { console.log(`${d.engine}: ${k} has no type`); failed = true }
  for (const [col, type] of Object.entries(want[d.engine] || {})) {
    const got = Object.entries(cols).find(([k]) => k.toLowerCase().endsWith(' ' + col.toLowerCase()))
    if (!got || got[1] !== type) { console.log(`${d.engine}: ${col} is ${got ? got[1] : 'missing'}, expected ${type}`); failed = true }
  }
}
writeFileSync('rel-types.json', JSON.stringify({ out, errors }, null, 1))
console.log(JSON.stringify({ out, errors }, null, 1))
await browser.close()
if (failed || errors.length) process.exit(1)

// Checks the project query designer's SQL <-> model round trips: the parser
// (pqParse) and writer (pqSql) are taken from web/index.html as shipped and
// run here, without a browser. node .github/ci/pq-roundtrip.mjs
import { readFileSync } from 'node:fs'
import vm from 'node:vm'

const html = readFileSync(new URL('../../web/index.html', import.meta.url), 'utf8')
const between = (a, b) => { const i = html.indexOf(a), j = html.indexOf(b, i); if (i < 0 || j < 0) throw new Error('not found: ' + a); return html.slice(i, j) }
const code = between('function qaTokens(src) {', '// qaParse finds') +
  between('// ---- the project query designer: model', '// ---- the project query designer: diagram') +
  '\n;globalThis.pqParse = pqParse; globalThis.pqSql = pqSql;'
const ctx = vm.createContext({})
vm.runInContext(code, ctx)
const { pqParse, pqSql } = ctx

const editable = [
  "SELECT c.name, o.order_date, i.amount\nFROM   fb.customer c\nJOIN   maria.orders o   ON o.customer_id = c.id\nLEFT JOIN ms.invoices i ON i.order_id = o.id\nWHERE  o.order_date >= '2026-01-01'\nORDER BY c.name",
  'SELECT DISTINCT c.city FROM fb.customer c WHERE c.city IS NOT NULL ORDER BY c.city DESC LIMIT 10',
  'SELECT c.name, COUNT(o.id) AS n, SUM(o.total) total FROM fb.customer c LEFT JOIN maria.orders o ON o.customer_id = c.id GROUP BY c.name HAVING COUNT(o.id) > 1 ORDER BY n',
  'SELECT * FROM fb.customer c, maria.orders o WHERE (c.id = 1 AND o.total > 5) OR (c.id = 2)',
  "SELECT c.name || ' ' || c.city AS label, upper(c.name) FROM fb.customer c WHERE c.id BETWEEN 1 AND 3",
  'SELECT o.* FROM maria.orders o RIGHT JOIN fb.customer c ON c.id = o.customer_id AND c.id <> 0 AND o.total > c.id',
  'SELECT "Order Details".qty FROM maria."Order Details"',
  'SELECT COUNT(*) FROM maria.orders',
  "SELECT c.name FROM fb.customer c FULL JOIN maria.orders o ON o.customer_id = c.id WHERE c.name LIKE 'A%' LIMIT 5 OFFSET 10",
  'SELECT s.STK_NO, c.CUST_ID FROM fb.STK s LEFT OUTER JOIN fb.CUST c ON c.CUST_ID = s.CUST_ID RIGHT OUTER JOIN maria.orders o ON o.id = s.STK_NO',
  "SELECT UPPER(c.name) AS n, strftime('%Y-%m-%d', o.order_date), ROUND(o.total, 2), c.city FROM fb.customer c JOIN maria.orders o ON o.customer_id = c.id WHERE c.name LIKE '%ES' AND o.total BETWEEN 10 AND 20 OR c.id NOT BETWEEN 1 AND 3",
  "SELECT LOWER(TRIM(c.name)) AS n, UPPER(strftime('%Y-%m-%d', o.d)) FROM fb.customer c JOIN maria.orders o ON o.cid = c.id WHERE LOWER(TRIM(c.name)) = lower(trim('aaa   ')) AND c.id IN (1, 2, 3) OR c.city NOT IN ('A', 'B') AND c.name GLOB 'A*' AND o.x IS NOT c.id",
  "SELECT c.name FROM fb.customer c WHERE UPPER(c.name) LIKE '%ES' AND c.id IS 3 AND c.city IS NOT NULL",
  "SELECT c.name, c.city AS Town, o.total FROM fb.customer c JOIN maria.orders o ON o.customer_id = c.id ORDER BY c.name\n-- group rows by: [\"Town\",\"name\"]",
  "SELECT cdb_title(TRIM(c.name)) AS name, cdb_camel(c.city), cdb_date(o.d, 'yyyy-MMM-dd ''at'' HH:mm') AS d, cdb_number(o.total, 'R #,##0.00') AS total FROM fb.customer c JOIN maria.orders o ON o.cid = c.id WHERE cdb_title(c.name) = 'Ann'",
  "SELECT strftime('%Y', o.d) || '-' || substr('JanFebMarAprMayJunJulAugSepOctNovDec', strftime('%m', o.d) * 3 - 2, 3) || '-' || strftime('%d', o.d) AS d FROM maria.orders o WHERE o.d BETWEEN '2026-01-01' AND '2026-12-31'",
]
const readOnly = ['WITH x AS (SELECT 1) SELECT * FROM x', 'SELECT * FROM maria.orders UNION SELECT * FROM maria.orders',
  'SELECT row_number() OVER (ORDER BY 1) FROM fb.customer', 'SELECT * FROM maria.orders o WHERE o.id IN (SELECT 1)',
  'SELECT * FROM maria.orders o JOIN fb.customer c USING (id)', 'SELECT * FROM orders', 'SELECT 1; SELECT 2']
let failed = 0
for (const q of editable) {
  const m1 = pqParse(q)
  if (m1.readOnly) { console.log('FAIL read-only:', m1.readOnly, '\n  ', q); failed++; continue }
  const s1 = pqSql(m1), m2 = pqParse(s1), s2 = pqSql(m2)
  if (JSON.stringify(m1) !== JSON.stringify(m2) || s1 !== s2) { console.log('FAIL round trip:\n', q, '\n->\n', s1, '\n->\n', s2); failed++ } else console.log('ok  ', q.replace(/\s+/g, ' ').slice(0, 70))
}
for (const q of readOnly) {
  const m = pqParse(q)
  if (!m.readOnly) { console.log('FAIL editable:', q); failed++ } else console.log('ok  read-only (' + m.readOnly + '):', q.slice(0, 50))
}
// Diagram edits: the join written as the acceptance test describes.
const m = pqParse('SELECT * FROM fb.customer c JOIN maria.orders o')
m.joins.o.on.push({ a: 'o', ac: 'customer_id', op: '=', b: 'c', bc: 'id' }); m.joins.o.type = 'INNER'
const s = pqSql(m)
if (!/JOIN\s+maria\.orders o ON o\.customer_id = c\.id/.test(s)) { console.log('FAIL join edit:', s); failed++ } else console.log('ok   join edit')
if (failed) { console.log(failed + ' failed'); process.exit(1) }
console.log('all passed')
